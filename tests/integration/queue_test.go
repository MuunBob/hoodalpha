package integration

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hibiken/asynq"

	"github.com/MuunBob/hoodalpha/internal/config"
	"github.com/MuunBob/hoodalpha/internal/persistence/redis"
	"github.com/MuunBob/hoodalpha/internal/queue"
)

func TestRedisConnectAndHealth(t *testing.T) {
	ctx := testContext(t, 30*time.Second)
	client, err := redis.Connect(ctx, redisConfig(t))
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	defer client.Close()

	if err := client.Health(ctx); err != nil {
		t.Fatalf("Health() error = %v", err)
	}
}

// flushTestDB clears the dedicated test Redis database so leftover tasks from a
// previous run cannot make a test pass or fail spuriously.
func flushTestDB(t *testing.T, cfg config.RedisConfig) {
	t.Helper()
	ctx := testContext(t, 15*time.Second)
	client, err := redis.Connect(ctx, cfg)
	if err != nil {
		t.Fatalf("connect for flush: %v", err)
	}
	defer client.Close()
	if err := client.FlushDB(ctx).Err(); err != nil {
		t.Fatalf("flush test db: %v", err)
	}
}

// runServer starts a worker in the background and stops it when the test ends.
func runServer(t *testing.T, srv *queue.Server) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := srv.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("server.Run() error = %v", err)
		}
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(30 * time.Second):
			t.Error("worker did not shut down within 30s")
		}
	})
}

func testWorkerConfig() config.WorkerConfig {
	return config.WorkerConfig{
		Concurrency:     4,
		Queues:          config.DefaultQueues(),
		ShutdownTimeout: 10 * time.Second,
	}
}

func TestAsynqEnqueueAndProcess(t *testing.T) {
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	const taskType = "test:echo"
	type payload struct {
		Message string `json:"message"`
	}

	got := make(chan string, 1)
	srv := queue.NewServer(redisCfg, testWorkerConfig(), slog.Default())
	srv.Handle(taskType, func(ctx context.Context, body []byte) error {
		got <- string(body)
		return nil
	})
	runServer(t, srv)

	client := queue.NewClient(redisCfg)
	defer client.Close()

	ctx := testContext(t, 30*time.Second)
	info, err := client.Enqueue(ctx, taskType, payload{Message: "hello"}, queue.EnqueueOptions{
		Queue:    queue.QueueDefault,
		MaxRetry: 1,
		Timeout:  10 * time.Second,
	})
	if err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}
	if info.Queue != queue.QueueDefault {
		t.Errorf("queue = %q, want %q", info.Queue, queue.QueueDefault)
	}

	select {
	case body := <-got:
		if body != `{"message":"hello"}` {
			t.Errorf("payload = %s", body)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("task was not processed within 20s")
	}
}

// TestAsynqRetriesOnFailure proves a failing handler is actually retried, which
// is what lets a transient RPC outage resolve itself instead of dropping work.
func TestAsynqRetriesOnFailure(t *testing.T) {
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	const taskType = "test:flaky"
	var attempts atomic.Int32
	succeeded := make(chan struct{})
	var once sync.Once

	srv := queue.NewServer(redisCfg, testWorkerConfig(), slog.Default())
	srv.Handle(taskType, func(ctx context.Context, body []byte) error {
		if attempts.Add(1) < 2 {
			return errors.New("simulated transient failure")
		}
		once.Do(func() { close(succeeded) })
		return nil
	})
	runServer(t, srv)

	client := queue.NewClient(redisCfg)
	defer client.Close()

	ctx := testContext(t, time.Minute)
	if _, err := client.Enqueue(ctx, taskType, nil, queue.EnqueueOptions{
		Queue:    queue.QueueDefault,
		MaxRetry: 3,
		Timeout:  10 * time.Second,
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	// The server's backoff is 2^n seconds, so the first retry lands after ~2s.
	select {
	case <-succeeded:
	case <-time.After(45 * time.Second):
		t.Fatalf("task never succeeded after %d attempts", attempts.Load())
	}
	if n := attempts.Load(); n < 2 {
		t.Errorf("attempts = %d, want at least 2", n)
	}
}

// TestAsynqSkipRetryArchives proves a poison payload is archived rather than
// retried forever. Without this, one malformed message occupies a worker slot
// on every backoff cycle indefinitely.
func TestAsynqSkipRetryArchives(t *testing.T) {
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	const taskType = "test:poison"
	var attempts atomic.Int32

	srv := queue.NewServer(redisCfg, testWorkerConfig(), slog.Default())
	srv.Handle(taskType, func(ctx context.Context, body []byte) error {
		attempts.Add(1)
		return queue.SkipRetry(errors.New("permanently invalid payload"))
	})
	runServer(t, srv)

	client := queue.NewClient(redisCfg)
	defer client.Close()

	ctx := testContext(t, 30*time.Second)
	if _, err := client.Enqueue(ctx, taskType, nil, queue.EnqueueOptions{
		Queue:    queue.QueueDefault,
		MaxRetry: 5,
		Timeout:  10 * time.Second,
	}); err != nil {
		t.Fatalf("Enqueue() error = %v", err)
	}

	inspector := asynq.NewInspector(queue.RedisOpt(redisCfg))
	defer inspector.Close()

	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		tasks, err := inspector.ListArchivedTasks(queue.QueueDefault)
		if err == nil && len(tasks) > 0 {
			if n := attempts.Load(); n != 1 {
				t.Errorf("attempts = %d, want 1 (SkipRetry must not retry)", n)
			}
			return
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("task was not archived; attempts = %d", attempts.Load())
}

// TestAsynqTaskIDDeduplicates covers the idempotency mechanism financial
// handlers will rely on: the same task ID cannot be queued twice.
func TestAsynqTaskIDDeduplicates(t *testing.T) {
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	client := queue.NewClient(redisCfg)
	defer client.Close()

	ctx := testContext(t, 30*time.Second)
	opts := queue.EnqueueOptions{
		Queue:  queue.QueueDefault,
		TaskID: "dedup-key-1",
		// No worker is running, so the task stays pending and the ID stays taken.
		Timeout: 10 * time.Second,
	}

	if _, err := client.Enqueue(ctx, "test:dedup", nil, opts); err != nil {
		t.Fatalf("first Enqueue() error = %v", err)
	}
	_, err := client.Enqueue(ctx, "test:dedup", nil, opts)
	if err == nil {
		t.Fatal("second Enqueue() with the same TaskID succeeded")
	}
	if !queue.IsDuplicate(err) {
		t.Errorf("IsDuplicate(%v) = false, want true", err)
	}
}

// TestAsynqmonCanInspectQueues asserts the same Redis that Asynq writes to is
// readable through the Inspector API that Asynqmon itself uses. If this passes,
// the Asynqmon container pointed at this Redis will show the same queues.
func TestAsynqmonCanInspectQueues(t *testing.T) {
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	client := queue.NewClient(redisCfg)
	defer client.Close()

	ctx := testContext(t, 30*time.Second)
	for i := 0; i < 3; i++ {
		if _, err := client.Enqueue(ctx, "test:inspect", map[string]int{"n": i},
			queue.EnqueueOptions{Queue: queue.QueueMaintenance, Timeout: 10 * time.Second}); err != nil {
			t.Fatalf("Enqueue() error = %v", err)
		}
	}

	inspector := asynq.NewInspector(queue.RedisOpt(redisCfg))
	defer inspector.Close()

	queues, err := inspector.Queues()
	if err != nil {
		t.Fatalf("Queues() error = %v", err)
	}
	found := false
	for _, q := range queues {
		if q == queue.QueueMaintenance {
			found = true
		}
	}
	if !found {
		t.Fatalf("maintenance queue not visible to inspector; got %v", queues)
	}

	info, err := inspector.GetQueueInfo(queue.QueueMaintenance)
	if err != nil {
		t.Fatalf("GetQueueInfo() error = %v", err)
	}
	if info.Pending != 3 {
		t.Errorf("pending = %d, want 3", info.Pending)
	}
	t.Log(fmt.Sprintf("asynqmon-visible state: pending=%d active=%d",
		info.Pending, info.Active))
}

// TestWorkerShutsDownGracefully proves Run returns after cancellation instead of
// leaking a goroutine or hanging past the shutdown timeout.
func TestWorkerShutsDownGracefully(t *testing.T) {
	redisCfg := redisConfig(t)
	flushTestDB(t, redisCfg)

	srv := queue.NewServer(redisCfg, testWorkerConfig(), slog.Default())
	srv.Handle("test:noop", func(context.Context, []byte) error { return nil })

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- srv.Run(ctx) }()

	time.Sleep(2 * time.Second) // let the server actually start
	cancel()

	select {
	case err := <-done:
		if err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("Run() error = %v", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("worker did not shut down within 30s of cancellation")
	}
}
