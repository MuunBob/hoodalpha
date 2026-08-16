package config_test

import (
	"strings"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/config"
)

// minimalEnv sets only the variables Load requires.
func minimalEnv(t *testing.T) {
	t.Helper()
	t.Setenv("POSTGRES_URL", "postgres://user:pass@localhost:5432/db")
	t.Setenv("REDIS_ADDR", "localhost:6379")
}

func TestLoadDefaults(t *testing.T) {
	minimalEnv(t)

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Chain.ChainID != 4663 {
		t.Errorf("ChainID = %d, want 4663 (Robinhood Chain mainnet)", cfg.Chain.ChainID)
	}
	if cfg.Risk.StopLossPercent != 5 {
		t.Errorf("StopLossPercent = %v, want 5", cfg.Risk.StopLossPercent)
	}
	if cfg.Risk.CapitalRecoveryPercent != 100 {
		t.Errorf("CapitalRecoveryPercent = %v, want 100", cfg.Risk.CapitalRecoveryPercent)
	}
	if cfg.Risk.MaxOpenPositions != 5 {
		t.Errorf("MaxOpenPositions = %d, want 5", cfg.Risk.MaxOpenPositions)
	}
	if cfg.AppEnv != "development" {
		t.Errorf("AppEnv = %q, want development", cfg.AppEnv)
	}
	if cfg.IsProduction() {
		t.Error("IsProduction() = true for development env")
	}
}

func TestLoadOverrides(t *testing.T) {
	minimalEnv(t)
	t.Setenv("APP_ENV", "production")
	t.Setenv("RH_CHAIN_ID", "46630")
	t.Setenv("RH_REQUEST_TIMEOUT", "3s")
	t.Setenv("STOP_LOSS_PERCENT", "7.5")
	t.Setenv("WORKER_CONCURRENCY", "4")
	t.Setenv("LOG_JSON", "true")

	cfg, err := config.Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	if cfg.Chain.ChainID != 46630 {
		t.Errorf("ChainID = %d, want 46630", cfg.Chain.ChainID)
	}
	if cfg.Chain.RequestTimeout != 3*time.Second {
		t.Errorf("RequestTimeout = %v, want 3s", cfg.Chain.RequestTimeout)
	}
	if cfg.Risk.StopLossPercent != 7.5 {
		t.Errorf("StopLossPercent = %v, want 7.5", cfg.Risk.StopLossPercent)
	}
	if cfg.Worker.Concurrency != 4 {
		t.Errorf("Concurrency = %d, want 4", cfg.Worker.Concurrency)
	}
	if !cfg.LogJSON {
		t.Error("LogJSON = false, want true")
	}
	if !cfg.IsProduction() {
		t.Error("IsProduction() = false for production env")
	}
}

func TestLoadRejectsMissingRequired(t *testing.T) {
	t.Setenv("POSTGRES_URL", "")
	t.Setenv("REDIS_ADDR", "localhost:6379")

	_, err := config.Load()
	if err == nil {
		t.Fatal("Load() succeeded without POSTGRES_URL")
	}
	if !strings.Contains(err.Error(), "POSTGRES_URL") {
		t.Errorf("error %q does not mention POSTGRES_URL", err)
	}
}

func TestLoadRejectsInvalidValues(t *testing.T) {
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{
			name: "unparseable int",
			env:  map[string]string{"WORKER_CONCURRENCY": "many"},
			want: "WORKER_CONCURRENCY",
		},
		{
			name: "unparseable duration",
			env:  map[string]string{"RH_REQUEST_TIMEOUT": "10 seconds"},
			want: "RH_REQUEST_TIMEOUT",
		},
		{
			name: "stop loss out of range",
			env:  map[string]string{"STOP_LOSS_PERCENT": "150"},
			want: "STOP_LOSS_PERCENT",
		},
		{
			name: "zero chain id",
			env:  map[string]string{"RH_CHAIN_ID": "0"},
			want: "RH_CHAIN_ID",
		},
		{
			name: "slippage above 100 percent",
			env:  map[string]string{"MAX_SLIPPAGE_BPS": "10001"},
			want: "MAX_SLIPPAGE_BPS",
		},
		{
			name: "min conns above max conns",
			env: map[string]string{
				"POSTGRES_MAX_CONNS": "2",
				"POSTGRES_MIN_CONNS": "5",
			},
			want: "POSTGRES_MIN_CONNS",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			minimalEnv(t)
			for k, v := range tt.env {
				t.Setenv(k, v)
			}
			_, err := config.Load()
			if err == nil {
				t.Fatalf("Load() succeeded with %v", tt.env)
			}
			if !strings.Contains(err.Error(), tt.want) {
				t.Errorf("error %q does not mention %s", err, tt.want)
			}
		})
	}
}

func TestDefaultQueuesCoverAllNames(t *testing.T) {
	q := config.DefaultQueues()
	for _, name := range []string{"critical", "default", "analysis", "market", "notifications", "maintenance"} {
		if w, ok := q[name]; !ok || w < 1 {
			t.Errorf("queue %q missing or has non-positive weight %d", name, w)
		}
	}
	if q["critical"] <= q["maintenance"] {
		t.Error("critical queue must outweigh maintenance")
	}
}
