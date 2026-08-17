// Package config loads and validates application configuration from the
// environment. Configuration is read once at startup; nothing mutates it after.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Config is the fully-resolved application configuration.
type Config struct {
	AppEnv   string
	LogLevel string
	LogJSON  bool

	HTTPAddr        string
	ShutdownTimeout time.Duration

	Chain    ChainConfig
	Postgres PostgresConfig
	Redis    RedisConfig
	Worker   WorkerConfig
	Risk     RiskConfig
	Telegram TelegramConfig
}

// TelegramConfig configures the control plane.
type TelegramConfig struct {
	// BotToken comes from BotFather. Secret: never logged, never audited.
	BotToken string
	// AllowedUserIDs is a closed allowlist of Telegram user IDs. Empty means
	// nobody may control the bot — an unconfigured deployment must be closed,
	// not open.
	AllowedUserIDs []int64
	// MiniAppURL is shown by /connect. Optional.
	MiniAppURL string
	// InitDataTTL bounds how old Mini App init data may be. Short windows
	// limit the value of a captured payload.
	InitDataTTL time.Duration
	// RateLimit is commands allowed per RateWindow, per user.
	RateLimit  int
	RateWindow time.Duration
}

// Enabled reports whether the Telegram control plane can start.
func (t TelegramConfig) Enabled() bool { return t.BotToken != "" }

// ChainConfig describes the Robinhood Chain endpoints the bot reads from.
type ChainConfig struct {
	ChainID        uint64
	RPCURL         string
	WSURL          string
	RequestTimeout time.Duration
	// MaxRetries bounds retry attempts per RPC call. Zero means no retry.
	MaxRetries   int
	RetryBackoff time.Duration
	// BlockStaleAfter marks the chain unhealthy when no new head arrives within it.
	BlockStaleAfter time.Duration
}

// PostgresConfig is the source-of-truth database. Money lives here, not in Redis.
type PostgresConfig struct {
	URL             string
	MaxConns        int32
	MinConns        int32
	MaxConnLifetime time.Duration
	MaxConnIdleTime time.Duration
	ConnectTimeout  time.Duration
}

// RedisConfig backs Asynq, cache, locks and rate limits. Never financial truth.
type RedisConfig struct {
	Addr     string
	Password string
	DB       int
	PoolSize int
}

// WorkerConfig configures the Asynq server.
type WorkerConfig struct {
	Concurrency     int
	Queues          map[string]int
	ShutdownTimeout time.Duration
}

// RiskConfig holds trading policy values. Loaded in Phase 0 so the policy is
// configuration from day one rather than constants buried in strategy code.
// No engine consumes these yet — the risk engine arrives in a later phase.
type RiskConfig struct {
	StopLossPercent        float64
	CapitalRecoveryPercent float64
	MaxPositionPercent     float64
	MaxOpenPositions       int
	DailyLossLimitPercent  float64
	MaxSlippageBPS         int
	MinLiquidityUSD        float64
	MinScore               float64
}

// DefaultQueues is the Asynq queue priority map. Higher weight = more workers.
func DefaultQueues() map[string]int {
	return map[string]int{
		"critical":      6,
		"default":       3,
		"analysis":      2,
		"market":        2,
		"notifications": 2,
		"maintenance":   1,
	}
}

// Load reads configuration from the environment and validates it.
func Load() (Config, error) {
	var errs []error
	e := func(err error) {
		if err != nil {
			errs = append(errs, err)
		}
	}

	cfg := Config{
		AppEnv:   getString("APP_ENV", "development"),
		LogLevel: getString("LOG_LEVEL", "info"),
	}

	var err error
	if cfg.LogJSON, err = getBool("LOG_JSON", false); err != nil {
		e(err)
	}
	cfg.HTTPAddr = getString("HTTP_ADDR", ":8080")
	if cfg.ShutdownTimeout, err = getDuration("SHUTDOWN_TIMEOUT", 20*time.Second); err != nil {
		e(err)
	}

	if cfg.Chain.ChainID, err = getUint("RH_CHAIN_ID", 4663); err != nil {
		e(err)
	}
	cfg.Chain.RPCURL = getString("RH_RPC_URL", "https://rpc.mainnet.chain.robinhood.com")
	cfg.Chain.WSURL = getString("RH_WS_URL", "")
	if cfg.Chain.RequestTimeout, err = getDuration("RH_REQUEST_TIMEOUT", 10*time.Second); err != nil {
		e(err)
	}
	if cfg.Chain.MaxRetries, err = getInt("RH_MAX_RETRIES", 3); err != nil {
		e(err)
	}
	if cfg.Chain.RetryBackoff, err = getDuration("RH_RETRY_BACKOFF", 250*time.Millisecond); err != nil {
		e(err)
	}
	if cfg.Chain.BlockStaleAfter, err = getDuration("RH_BLOCK_STALE_AFTER", 2*time.Minute); err != nil {
		e(err)
	}

	cfg.Postgres.URL = getString("POSTGRES_URL", "")
	if cfg.Postgres.MaxConns, err = getInt32("POSTGRES_MAX_CONNS", 10); err != nil {
		e(err)
	}
	if cfg.Postgres.MinConns, err = getInt32("POSTGRES_MIN_CONNS", 1); err != nil {
		e(err)
	}
	if cfg.Postgres.MaxConnLifetime, err = getDuration("POSTGRES_MAX_CONN_LIFETIME", time.Hour); err != nil {
		e(err)
	}
	if cfg.Postgres.MaxConnIdleTime, err = getDuration("POSTGRES_MAX_CONN_IDLE_TIME", 30*time.Minute); err != nil {
		e(err)
	}
	if cfg.Postgres.ConnectTimeout, err = getDuration("POSTGRES_CONNECT_TIMEOUT", 10*time.Second); err != nil {
		e(err)
	}

	cfg.Redis.Addr = getString("REDIS_ADDR", "localhost:6379")
	cfg.Redis.Password = os.Getenv("REDIS_PASSWORD")
	if cfg.Redis.DB, err = getInt("REDIS_DB", 0); err != nil {
		e(err)
	}
	if cfg.Redis.PoolSize, err = getInt("REDIS_POOL_SIZE", 10); err != nil {
		e(err)
	}

	if cfg.Worker.Concurrency, err = getInt("WORKER_CONCURRENCY", 10); err != nil {
		e(err)
	}
	cfg.Worker.Queues = DefaultQueues()
	if cfg.Worker.ShutdownTimeout, err = getDuration("WORKER_SHUTDOWN_TIMEOUT", 30*time.Second); err != nil {
		e(err)
	}

	if cfg.Risk.StopLossPercent, err = getFloat("STOP_LOSS_PERCENT", 5); err != nil {
		e(err)
	}
	if cfg.Risk.CapitalRecoveryPercent, err = getFloat("CAPITAL_RECOVERY_PERCENT", 100); err != nil {
		e(err)
	}
	if cfg.Risk.MaxPositionPercent, err = getFloat("MAX_POSITION_PERCENT", 5); err != nil {
		e(err)
	}
	if cfg.Risk.MaxOpenPositions, err = getInt("MAX_OPEN_POSITIONS", 5); err != nil {
		e(err)
	}
	if cfg.Risk.DailyLossLimitPercent, err = getFloat("DAILY_LOSS_LIMIT_PERCENT", 10); err != nil {
		e(err)
	}
	if cfg.Risk.MaxSlippageBPS, err = getInt("MAX_SLIPPAGE_BPS", 100); err != nil {
		e(err)
	}
	if cfg.Risk.MinLiquidityUSD, err = getFloat("MIN_LIQUIDITY_USD", 10000); err != nil {
		e(err)
	}
	if cfg.Risk.MinScore, err = getFloat("MIN_SCORE", 60); err != nil {
		e(err)
	}

	cfg.Telegram.BotToken = os.Getenv("TELEGRAM_BOT_TOKEN")
	if cfg.Telegram.AllowedUserIDs, err = getInt64List("TELEGRAM_ALLOWED_USER_IDS"); err != nil {
		e(err)
	}
	cfg.Telegram.MiniAppURL = getString("TELEGRAM_MINIAPP_URL", "")
	if cfg.Telegram.InitDataTTL, err = getDuration("TELEGRAM_INITDATA_TTL", 15*time.Minute); err != nil {
		e(err)
	}
	if cfg.Telegram.RateLimit, err = getInt("TELEGRAM_RATE_LIMIT", 20); err != nil {
		e(err)
	}
	if cfg.Telegram.RateWindow, err = getDuration("TELEGRAM_RATE_WINDOW", time.Minute); err != nil {
		e(err)
	}

	if len(errs) > 0 {
		return Config{}, errors.Join(errs...)
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) validate() error {
	var errs []error
	if c.Postgres.URL == "" {
		errs = append(errs, errors.New("POSTGRES_URL is required"))
	}
	if c.Chain.RPCURL == "" {
		errs = append(errs, errors.New("RH_RPC_URL is required"))
	}
	if c.Chain.ChainID == 0 {
		errs = append(errs, errors.New("RH_CHAIN_ID must be non-zero"))
	}
	if c.Redis.Addr == "" {
		errs = append(errs, errors.New("REDIS_ADDR is required"))
	}
	if c.Postgres.MaxConns < 1 {
		errs = append(errs, errors.New("POSTGRES_MAX_CONNS must be >= 1"))
	}
	if c.Postgres.MinConns < 0 || c.Postgres.MinConns > c.Postgres.MaxConns {
		errs = append(errs, errors.New("POSTGRES_MIN_CONNS must be between 0 and POSTGRES_MAX_CONNS"))
	}
	if c.Worker.Concurrency < 1 {
		errs = append(errs, errors.New("WORKER_CONCURRENCY must be >= 1"))
	}
	if c.Chain.MaxRetries < 0 {
		errs = append(errs, errors.New("RH_MAX_RETRIES must be >= 0"))
	}
	if c.Chain.RequestTimeout <= 0 {
		errs = append(errs, errors.New("RH_REQUEST_TIMEOUT must be > 0"))
	}
	if c.Risk.StopLossPercent <= 0 || c.Risk.StopLossPercent >= 100 {
		errs = append(errs, errors.New("STOP_LOSS_PERCENT must be in (0,100)"))
	}
	if c.Risk.CapitalRecoveryPercent <= 0 {
		errs = append(errs, errors.New("CAPITAL_RECOVERY_PERCENT must be > 0"))
	}
	if c.Risk.MaxOpenPositions < 1 {
		errs = append(errs, errors.New("MAX_OPEN_POSITIONS must be >= 1"))
	}
	if c.Risk.MaxSlippageBPS < 0 || c.Risk.MaxSlippageBPS > 10000 {
		errs = append(errs, errors.New("MAX_SLIPPAGE_BPS must be in [0,10000]"))
	}
	// A configured bot with an empty allowlist would answer nobody, which
	// looks identical to a broken deployment. Refuse it at startup instead.
	if c.Telegram.Enabled() && len(c.Telegram.AllowedUserIDs) == 0 {
		errs = append(errs, errors.New(
			"TELEGRAM_ALLOWED_USER_IDS is required when TELEGRAM_BOT_TOKEN is set"))
	}
	if c.Telegram.InitDataTTL <= 0 {
		errs = append(errs, errors.New("TELEGRAM_INITDATA_TTL must be > 0"))
	}
	if c.Telegram.RateLimit < 1 {
		errs = append(errs, errors.New("TELEGRAM_RATE_LIMIT must be >= 1"))
	}
	return errors.Join(errs...)
}

// IsProduction reports whether the app runs with production safety defaults.
func (c Config) IsProduction() bool { return strings.EqualFold(c.AppEnv, "production") }

func getString(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func getInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func getInt32(key string, def int32) (int32, error) {
	n, err := getInt(key, int(def))
	if err != nil {
		return 0, err
	}
	return int32(n), nil
}

func getUint(key string, def uint64) (uint64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseUint(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func getFloat(key string, def float64) (float64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}

func getBool(key string, def bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%s: %w", key, err)
	}
	return b, nil
}

// getInt64List parses a comma-separated list of integers. Used for the
// Telegram allowlist, where a malformed entry must fail startup rather than
// be silently dropped — a dropped ID locks its owner out.
func getInt64List(key string) ([]int64, error) {
	raw, ok := os.LookupEnv(key)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	var out []int64
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		n, err := strconv.ParseInt(part, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("%s: %q is not a valid id", key, part)
		}
		if n <= 0 {
			return nil, fmt.Errorf("%s: id %d must be positive", key, n)
		}
		out = append(out, n)
	}
	return out, nil
}

func getDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
