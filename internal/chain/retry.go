package chain

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/rpc"
)

// retryPolicy retries transient RPC failures with exponential backoff.
// Attempts are bounded: a tight infinite loop against a failing provider
// would burn rate limit and hide the outage instead of surfacing it.
type retryPolicy struct {
	MaxRetries int
	Backoff    time.Duration
}

// Do runs fn, retrying only retryable errors. It always respects ctx.
func (p retryPolicy) Do(ctx context.Context, fn func(context.Context) error) error {
	backoff := p.Backoff
	if backoff <= 0 {
		backoff = 250 * time.Millisecond
	}

	var err error
	for attempt := 0; ; attempt++ {
		if ctxErr := ctx.Err(); ctxErr != nil {
			if err != nil {
				return errors.Join(err, ctxErr)
			}
			return ctxErr
		}

		err = fn(ctx)
		if err == nil {
			return nil
		}
		if attempt >= p.MaxRetries || !retryable(err) {
			return err
		}

		wait := backoff << attempt
		if max := 5 * time.Second; wait > max {
			wait = max
		}
		t := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			t.Stop()
			return errors.Join(err, ctx.Err())
		case <-t.C:
		}
	}
}

// retryable reports whether an error is worth another attempt. A "not found"
// or a JSON-RPC application error is a real answer, not a transport failure.
func retryable(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, ethereum.NotFound) {
		return false
	}
	if errors.Is(err, context.Canceled) {
		return false
	}
	// Our own per-call timeout expired: the provider is slow, so retry.
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var rpcErr rpc.Error
	if errors.As(err, &rpcErr) {
		// -32005 is the common "limit exceeded" code across providers.
		return rpcErr.ErrorCode() == -32005
	}
	msg := strings.ToLower(err.Error())
	for _, s := range []string{
		"connection reset",
		"connection refused",
		"broken pipe",
		"eof",
		"timeout",
		"deadline exceeded",
		"too many requests",
		"429",
		"502", "503", "504",
		"temporarily unavailable",
		"i/o timeout",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}
