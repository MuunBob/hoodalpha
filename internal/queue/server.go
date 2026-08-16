package queue

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/hibiken/asynq"

	"github.com/MuunBob/hoodalpha/internal/config"
)

// Handler processes one task. Payload is the raw JSON body.
//
// Returning an error retries the task subject to its MaxRetry. Returning
// asynq.SkipRetry (wrapped) archives it immediately — use that for payloads
// that will never succeed, so a poison message cannot loop forever.
type Handler func(ctx context.Context, payload []byte) error

// Server runs registered handlers against the configured queues.
type Server struct {
	inner *asynq.Server
	mux   *asynq.ServeMux
	log   *slog.Logger
}

// NewServer builds an Asynq server. Handlers are registered with Handle before
// Run is called.
func NewServer(redisCfg config.RedisConfig, workerCfg config.WorkerConfig, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	log = log.With("component", "worker")

	srv := asynq.NewServer(RedisOpt(redisCfg), asynq.Config{
		Concurrency:     workerCfg.Concurrency,
		Queues:          workerCfg.Queues,
		StrictPriority:  false,
		ShutdownTimeout: workerCfg.ShutdownTimeout,
		Logger:          slogAdapter{log},
		// Exponential backoff with a ceiling, so a failing dependency is
		// retried patiently instead of hammered.
		RetryDelayFunc: asynq.RetryDelayFunc(func(n int, _ error, _ *asynq.Task) time.Duration {
			d := time.Duration(1<<uint(n)) * time.Second
			if d > 10*time.Minute {
				d = 10 * time.Minute
			}
			return d
		}),
		ErrorHandler: asynq.ErrorHandlerFunc(func(ctx context.Context, task *asynq.Task, err error) {
			retried, _ := asynq.GetRetryCount(ctx)
			maxRetry, _ := asynq.GetMaxRetry(ctx)
			log.Error("task failed",
				"type", task.Type(),
				"retry", retried,
				"max_retry", maxRetry,
				"error", err)
		}),
	})

	return &Server{inner: srv, mux: asynq.NewServeMux(), log: log}
}

// Handle registers a handler for a task type. It panics on a duplicate
// registration, which is a programming error surfaced at boot.
func (s *Server) Handle(taskType string, h Handler) {
	s.mux.HandleFunc(taskType, func(ctx context.Context, t *asynq.Task) error {
		start := time.Now()
		err := h(ctx, t.Payload())
		s.log.Debug("task processed",
			"type", t.Type(),
			"duration", time.Since(start).String(),
			"ok", err == nil)
		if err != nil {
			return fmt.Errorf("%s: %w", t.Type(), err)
		}
		return nil
	})
}

// Run starts processing and blocks until ctx is cancelled, then drains
// in-flight tasks within the configured shutdown timeout.
func (s *Server) Run(ctx context.Context) error {
	if err := s.inner.Start(s.mux); err != nil {
		return fmt.Errorf("start asynq server: %w", err)
	}
	<-ctx.Done()
	s.log.Info("draining in-flight tasks")
	s.inner.Shutdown()
	return nil
}

// SkipRetry wraps err so Asynq archives the task instead of retrying it.
func SkipRetry(err error) error {
	return fmt.Errorf("%w: %w", asynq.SkipRetry, err)
}

// ErrSkipRetry is exported for tests and callers that need to compare.
var ErrSkipRetry = asynq.SkipRetry

// IsSkipRetry reports whether an error asks Asynq to stop retrying.
func IsSkipRetry(err error) bool { return errors.Is(err, asynq.SkipRetry) }

// slogAdapter satisfies asynq.Logger using the project logger.
type slogAdapter struct{ l *slog.Logger }

func (a slogAdapter) Debug(args ...any) { a.l.Debug(fmt.Sprint(args...)) }
func (a slogAdapter) Info(args ...any)  { a.l.Info(fmt.Sprint(args...)) }
func (a slogAdapter) Warn(args ...any)  { a.l.Warn(fmt.Sprint(args...)) }
func (a slogAdapter) Error(args ...any) { a.l.Error(fmt.Sprint(args...)) }
func (a slogAdapter) Fatal(args ...any) { a.l.Error(fmt.Sprint(args...)) }
