package domain

import "time"

// HealthStatus is the coarse state of a dependency or of the whole process.
type HealthStatus string

const (
	// HealthUp means the dependency answered correctly.
	HealthUp HealthStatus = "up"
	// HealthDegraded means the dependency answered but something is off
	// (e.g. chain head is stale). The process keeps running.
	HealthDegraded HealthStatus = "degraded"
	// HealthDown means the dependency is unusable.
	HealthDown HealthStatus = "down"
)

// ComponentHealth is one dependency's result.
type ComponentHealth struct {
	Name    string        `json:"name"`
	Status  HealthStatus  `json:"status"`
	Latency time.Duration `json:"-"`
	// LatencyMS is the serialised form of Latency.
	LatencyMS int64 `json:"latency_ms"`
	// Error is a redacted human-readable failure reason, empty when healthy.
	Error string `json:"error,omitempty"`
	// Details carries small, non-sensitive facts (chain id, block number).
	Details map[string]string `json:"details,omitempty"`
}

// HealthReport aggregates all component results.
type HealthReport struct {
	Status     HealthStatus      `json:"status"`
	CheckedAt  time.Time         `json:"checked_at"`
	Version    string            `json:"version"`
	Components []ComponentHealth `json:"components"`
}

// Aggregate returns the worst status across components. Any down component
// makes the whole report down; any degraded one makes it degraded.
func Aggregate(components []ComponentHealth) HealthStatus {
	worst := HealthUp
	for _, c := range components {
		switch c.Status {
		case HealthDown:
			return HealthDown
		case HealthDegraded:
			worst = HealthDegraded
		}
	}
	return worst
}
