package chain

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ethereum/go-ethereum"
)

func TestRetryStopsOnSuccess(t *testing.T) {
	p := retryPolicy{MaxRetries: 5, Backoff: time.Millisecond}
	calls := 0
	err := p.Do(context.Background(), func(context.Context) error {
		calls++
		if calls < 3 {
			return errors.New("connection reset by peer")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("Do() error = %v", err)
	}
	if calls != 3 {
		t.Errorf("calls = %d, want 3", calls)
	}
}

func TestRetryBoundedByMaxRetries(t *testing.T) {
	p := retryPolicy{MaxRetries: 2, Backoff: time.Millisecond}
	calls := 0
	err := p.Do(context.Background(), func(context.Context) error {
		calls++
		return errors.New("connection refused")
	})
	if err == nil {
		t.Fatal("Do() succeeded, want error")
	}
	// 1 initial attempt + 2 retries. A tight infinite loop against a dead
	// provider is exactly what this bound exists to prevent.
	if calls != 3 {
		t.Errorf("calls = %d, want 3 (1 attempt + 2 retries)", calls)
	}
}

func TestRetrySkipsNonRetryableErrors(t *testing.T) {
	nonRetryable := []error{
		ethereum.NotFound,
		errors.New("execution reverted"),
		errors.New("invalid argument"),
		context.Canceled,
	}

	for _, target := range nonRetryable {
		calls := 0
		p := retryPolicy{MaxRetries: 5, Backoff: time.Millisecond}
		_ = p.Do(context.Background(), func(context.Context) error {
			calls++
			return target
		})
		if calls != 1 {
			t.Errorf("error %v retried %d times, want 1 attempt", target, calls)
		}
	}
}

func TestRetryRespectsContextCancellation(t *testing.T) {
	// A long backoff means the only way this test finishes quickly is if the
	// retry loop actually aborts on cancellation instead of sleeping through it.
	p := retryPolicy{MaxRetries: 10, Backoff: 10 * time.Second}
	ctx, cancel := context.WithCancel(context.Background())

	calls := 0
	done := make(chan error, 1)
	go func() {
		done <- p.Do(ctx, func(context.Context) error {
			calls++
			cancel()
			return errors.New("i/o timeout")
		})
	}()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("error = %v, want context.Canceled", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Do() did not return after context cancellation")
	}
	if calls != 1 {
		t.Errorf("calls = %d, want 1", calls)
	}
}

func TestRetryableClassification(t *testing.T) {
	retryableErrs := []string{
		"connection reset by peer",
		"read: connection refused",
		"429 Too Many Requests",
		"503 Service Unavailable",
		"context deadline exceeded",
		"unexpected EOF",
	}
	for _, msg := range retryableErrs {
		if !retryable(errors.New(msg)) {
			t.Errorf("retryable(%q) = false, want true", msg)
		}
	}

	notRetryable := []string{
		"execution reverted",
		"nonce too low",
		"invalid opcode",
	}
	for _, msg := range notRetryable {
		if retryable(errors.New(msg)) {
			t.Errorf("retryable(%q) = true, want false", msg)
		}
	}

	if retryable(nil) {
		t.Error("retryable(nil) = true")
	}
}
