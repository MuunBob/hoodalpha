package httpapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/persistence/postgres"
)

// MiniAppDeps are the use cases the Mini App endpoints call.
type MiniAppDeps struct {
	Auth    *application.MiniAppAuth
	Wallets *application.WalletService
	ChainID uint64
}

// contextKey is unexported so no other package can inject a fake identity into
// a request context.
type contextKey struct{ name string }

var identityKey = contextKey{"miniapp-identity"}

// mountMiniApp registers the Mini App backend under /api/miniapp.
//
// Every route sits behind requireInitData. There is no unauthenticated path
// that touches a wallet, and the identity used downstream comes from the
// verified signature — never from the request body or a query parameter.
func (s *Server) mountMiniApp(r chi.Router, d MiniAppDeps) {
	if d.Auth == nil {
		return
	}

	r.Route("/api/miniapp", func(r chi.Router) {
		r.Use(s.requireInitData(d.Auth))

		// Confirms the session and returns what the UI needs to render.
		r.Post("/auth", func(w http.ResponseWriter, r *http.Request) {
			id := identityFrom(r)
			writeJSON(w, http.StatusOK, map[string]any{
				"user_id":       id.User.ID.String(),
				"telegram_id":   int64(id.Telegram.TelegramID),
				"display_name":  id.Telegram.DisplayName(),
				"first_contact": id.FirstContact,
				"chain_id":      d.ChainID,
				"chain_name":    domain.ChainName(d.ChainID),
				// Stated explicitly so a UI cannot imply otherwise.
				"trading_available": false,
			})
		})

		r.Get("/wallets", func(w http.ResponseWriter, r *http.Request) {
			id := identityFrom(r)
			views, err := d.Wallets.List(r.Context(), id.User.ID)
			if err != nil {
				s.log.Error("list wallets failed", "error", err)
				writeError(w, http.StatusInternalServerError, "could not list wallets")
				return
			}
			out := make([]map[string]any, 0, len(views))
			for _, v := range views {
				out = append(out, walletJSON(v))
			}
			writeJSON(w, http.StatusOK, map[string]any{"wallets": out})
		})

		r.Post("/wallets", func(w http.ResponseWriter, r *http.Request) {
			id := identityFrom(r)

			var body struct {
				Address string `json:"address"`
				Role    string `json:"role"`
				Label   string `json:"label"`
			}
			if err := decodeJSON(r, &body); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}

			role := domain.WalletRole(strings.ToLower(strings.TrimSpace(body.Role)))
			if role == "" {
				role = domain.WalletRoleOwner
			}
			if !role.Valid() {
				writeError(w, http.StatusBadRequest, "invalid wallet role")
				return
			}

			wallet, err := d.Wallets.Link(r.Context(), application.LinkRequest{
				UserID:  id.User.ID,
				Address: body.Address,
				Role:    role,
				Label:   body.Label,
				ActorID: "telegram:" + itoa64(int64(id.Telegram.TelegramID)),
			})
			switch {
			case err == nil:
				writeJSON(w, http.StatusCreated, walletJSON(application.WalletView{Wallet: wallet}))
			case errors.Is(err, domain.ErrWalletAlreadyLinked):
				writeError(w, http.StatusConflict, "wallet already linked")
			case strings.Contains(err.Error(), "invalid evm address"),
				strings.Contains(err.Error(), "zero address"):
				writeError(w, http.StatusBadRequest, err.Error())
			default:
				s.log.Error("link wallet failed", "error", err)
				writeError(w, http.StatusInternalServerError, "could not link wallet")
			}
		})

		r.Get("/wallets/{walletID}", func(w http.ResponseWriter, r *http.Request) {
			id := identityFrom(r)
			view, err := d.Wallets.Get(r.Context(), id.User.ID, chi.URLParam(r, "walletID"))
			if err != nil {
				writeWalletError(w, s, err)
				return
			}
			writeJSON(w, http.StatusOK, walletJSON(view))
		})

		r.Put("/wallets/{walletID}/policy", func(w http.ResponseWriter, r *http.Request) {
			id := identityFrom(r)

			var body struct {
				MaxPositionPercent     float64 `json:"max_position_percent"`
				MaxOpenPositions       int     `json:"max_open_positions"`
				DailyLossLimitPercent  float64 `json:"daily_loss_limit_percent"`
				StopLossPercent        float64 `json:"stop_loss_percent"`
				CapitalRecoveryPercent float64 `json:"capital_recovery_percent"`
				MaxSlippageBPS         int     `json:"max_slippage_bps"`
				MinLiquidityUSD        float64 `json:"min_liquidity_usd"`
				TradingEnabled         bool    `json:"trading_enabled"`
			}
			if err := decodeJSON(r, &body); err != nil {
				writeError(w, http.StatusBadRequest, "invalid request body")
				return
			}

			policy := domain.WalletPolicy{
				MaxPositionPercent:     body.MaxPositionPercent,
				MaxOpenPositions:       body.MaxOpenPositions,
				DailyLossLimitPercent:  body.DailyLossLimitPercent,
				StopLossPercent:        body.StopLossPercent,
				CapitalRecoveryPercent: body.CapitalRecoveryPercent,
				MaxSlippageBPS:         body.MaxSlippageBPS,
				MinLiquidityUSD:        body.MinLiquidityUSD,
				TradingEnabled:         body.TradingEnabled,
			}
			// The policy is validated in the use case and again by database
			// constraints. A limit that bounds real money is worth checking
			// more than once, and the client is not trusted to have checked.
			updated, err := d.Wallets.UpdatePolicy(r.Context(), id.User.ID,
				chi.URLParam(r, "walletID"), policy,
				"telegram:"+itoa64(int64(id.Telegram.TelegramID)))
			switch {
			case err == nil:
				writeJSON(w, http.StatusOK, policyJSON(updated))
			case errors.Is(err, domain.ErrWalletNotOwned), errors.Is(err, postgres.ErrNotFound):
				// Same response for "not yours" and "does not exist": a
				// different one would let a client enumerate wallet IDs.
				writeError(w, http.StatusNotFound, "wallet not found")
			case strings.Contains(err.Error(), "invalid policy"):
				writeError(w, http.StatusUnprocessableEntity, err.Error())
			default:
				s.log.Error("update policy failed", "error", err)
				writeError(w, http.StatusInternalServerError, "could not update policy")
			}
		})
	})
}

// requireInitData authenticates a request from its Telegram init data.
//
// The payload is accepted from the Authorization header (preferred) or the
// X-Telegram-Init-Data header. It is never read from the query string: URLs
// end up in proxy logs and browser history, and this string is a credential.
func (s *Server) requireInitData(auth *application.MiniAppAuth) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			raw := r.Header.Get("X-Telegram-Init-Data")
			if raw == "" {
				if h := r.Header.Get("Authorization"); strings.HasPrefix(h, "tma ") {
					raw = strings.TrimSpace(strings.TrimPrefix(h, "tma "))
				}
			}
			if raw == "" {
				writeError(w, http.StatusUnauthorized, "missing init data")
				return
			}

			identity, err := auth.Authenticate(r.Context(), raw)
			if err != nil {
				// One generic message for every failure mode. Distinguishing
				// "bad signature" from "not allowlisted" would tell an
				// attacker which half of the problem they have solved.
				writeError(w, http.StatusUnauthorized, "unauthorized")
				return
			}

			ctx := contextWithIdentity(r.Context(), identity)
			next.ServeHTTP(w, r.WithContext(ctx))
		})
	}
}

func walletJSON(v application.WalletView) map[string]any {
	out := map[string]any{
		"id":         v.Wallet.ID,
		"address":    v.Wallet.Address.String(),
		"chain_id":   v.Wallet.ChainID,
		"chain_name": domain.ChainName(v.Wallet.ChainID),
		"role":       string(v.Wallet.Role),
		"status":     string(v.Wallet.Status),
		"label":      v.Wallet.Label,
		"created_at": v.Wallet.CreatedAt.Format(time.RFC3339),
	}
	if v.Wallet.VerifiedAt != nil {
		out["verified_at"] = v.Wallet.VerifiedAt.Format(time.RFC3339)
	}
	if v.BalanceError != "" {
		out["balance_available"] = false
	} else if !v.Balance.IsZero() || v.Wallet.IsUsable() {
		out["balance_available"] = true
		// Sent as strings: a wei value exceeds float64's exact integer range,
		// and JSON numbers are float64 in most clients.
		out["balance_wei"] = v.Balance.String()
		out["balance_eth"] = v.Balance.Ether()
	}
	if v.Policy.WalletID != "" {
		out["policy"] = policyJSON(v.Policy)
	}
	return out
}

func policyJSON(p domain.WalletPolicy) map[string]any {
	return map[string]any{
		"max_position_percent":     p.MaxPositionPercent,
		"max_open_positions":       p.MaxOpenPositions,
		"daily_loss_limit_percent": p.DailyLossLimitPercent,
		"stop_loss_percent":        p.StopLossPercent,
		"capital_recovery_percent": p.CapitalRecoveryPercent,
		"max_slippage_bps":         p.MaxSlippageBPS,
		"min_liquidity_usd":        p.MinLiquidityUSD,
		"trading_enabled":          p.TradingEnabled,
	}
}

func writeWalletError(w http.ResponseWriter, s *Server, err error) {
	switch {
	case errors.Is(err, domain.ErrWalletNotOwned), errors.Is(err, postgres.ErrNotFound):
		writeError(w, http.StatusNotFound, "wallet not found")
	default:
		s.log.Error("wallet request failed", "error", err)
		writeError(w, http.StatusInternalServerError, "request failed")
	}
}

func decodeJSON(r *http.Request, dst any) error {
	// Bounded so an oversized body cannot exhaust memory.
	dec := json.NewDecoder(http.MaxBytesReader(nil, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	return dec.Decode(dst)
}

func writeError(w http.ResponseWriter, code int, message string) {
	writeJSON(w, code, map[string]string{"error": message})
}

func itoa64(v int64) string {
	if v == 0 {
		return "0"
	}
	neg := v < 0
	if neg {
		v = -v
	}
	var buf [20]byte
	i := len(buf)
	for v > 0 {
		i--
		buf[i] = byte('0' + v%10)
		v /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
