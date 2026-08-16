// Package httpapi serves the operational HTTP surface: liveness, readiness and
// build info. It is not a public API and carries no trading endpoints.
package httpapi

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/MuunBob/hoodalpha/internal/application"
	"github.com/MuunBob/hoodalpha/internal/domain"
	"github.com/MuunBob/hoodalpha/internal/observability/buildinfo"
)

// Server wraps net/http with graceful shutdown.
type Server struct {
	http *http.Server
	log  *slog.Logger
}

// New builds the operational server.
func New(addr string, health *application.HealthChecker, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "http")

	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.Recoverer)
	r.Use(middleware.Timeout(15 * time.Second))

	// Liveness: the process is running. Deliberately dependency-free so a
	// database blip does not cause an orchestrator to kill a healthy process
	// that is correctly refusing to trade.
	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Readiness: dependencies are usable.
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		report := health.Check(r.Context())
		code := http.StatusOK
		if report.Status == domain.HealthDown {
			code = http.StatusServiceUnavailable
		}
		writeJSON(w, code, report)
	})

	r.Get("/version", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, buildinfo.Get())
	})

	return &Server{
		http: &http.Server{
			Addr:              addr,
			Handler:           r,
			ReadHeaderTimeout: 5 * time.Second,
			ReadTimeout:       15 * time.Second,
			WriteTimeout:      30 * time.Second,
			IdleTimeout:       60 * time.Second,
		},
		log: log,
	}
}

// Run serves until ctx is cancelled, then drains connections within timeout.
func (s *Server) Run(ctx context.Context, shutdownTimeout time.Duration) error {
	errCh := make(chan error, 1)
	go func() {
		s.log.Info("http server listening", "addr", s.http.Addr)
		if err := s.http.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- fmt.Errorf("http server: %w", err)
			return
		}
		errCh <- nil
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	if err := s.http.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("http shutdown: %w", err)
	}
	return nil
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}
