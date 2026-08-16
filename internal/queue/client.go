package queue

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hibiken/asynq"

	"github.com/MuunBob/hoodalpha/internal/config"
)

// Client enqueues tasks.
type Client struct {
	inner *asynq.Client
}

// NewClient builds an Asynq client against Redis.
func NewClient(cfg config.RedisConfig) *Client {
	return &Client{inner: asynq.NewClient(RedisOpt(cfg))}
}

// Close releases the client's Redis connections.
func (c *Client) Close() error { return c.inner.Close() }

// EnqueueOptions are the knobs callers actually use, so they do not have to
// import Asynq to schedule work.
type EnqueueOptions struct {
	Queue string
	// TaskID makes the enqueue idempotent: a second enqueue with the same ID
	// while the first is still known returns asynq.ErrTaskIDConflict.
	TaskID string
	// MaxRetry counts retries after the first attempt.
	MaxRetry int
	// Timeout bounds a single execution attempt.
	Timeout time.Duration
	// ProcessIn delays the first attempt.
	ProcessIn time.Duration
	// Retention keeps the completed task visible in Asynqmon for this long.
	Retention time.Duration
}

func (o EnqueueOptions) asynqOpts() []asynq.Option {
	opts := []asynq.Option{}
	if o.Queue != "" {
		opts = append(opts, asynq.Queue(o.Queue))
	}
	if o.TaskID != "" {
		opts = append(opts, asynq.TaskID(o.TaskID))
	}
	if o.MaxRetry > 0 {
		opts = append(opts, asynq.MaxRetry(o.MaxRetry))
	}
	if o.Timeout > 0 {
		opts = append(opts, asynq.Timeout(o.Timeout))
	}
	if o.ProcessIn > 0 {
		opts = append(opts, asynq.ProcessIn(o.ProcessIn))
	}
	if o.Retention > 0 {
		opts = append(opts, asynq.Retention(o.Retention))
	}
	return opts
}

// TaskInfo is the subset of Asynq's result the application cares about.
type TaskInfo struct {
	ID    string
	Type  string
	Queue string
}

// Enqueue serialises payload as JSON and schedules the task.
//
// Asynq delivers at-least-once. Handlers must therefore be safe to run twice;
// financial handlers must additionally key their state transitions so a repeat
// delivery cannot double-spend.
func (c *Client) Enqueue(ctx context.Context, taskType string, payload any, opts EnqueueOptions) (TaskInfo, error) {
	var body []byte
	if payload != nil {
		var err error
		body, err = json.Marshal(payload)
		if err != nil {
			return TaskInfo{}, fmt.Errorf("marshal %s payload: %w", taskType, err)
		}
	}
	info, err := c.inner.EnqueueContext(ctx, asynq.NewTask(taskType, body), opts.asynqOpts()...)
	if err != nil {
		return TaskInfo{}, fmt.Errorf("enqueue %s: %w", taskType, err)
	}
	return TaskInfo{ID: info.ID, Type: info.Type, Queue: info.Queue}, nil
}

// IsDuplicate reports whether an enqueue failed because the task ID is already
// in flight. Callers treat this as success: the work is already scheduled.
func IsDuplicate(err error) bool { return errors.Is(err, asynq.ErrTaskIDConflict) }
