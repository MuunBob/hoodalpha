package chain

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethclient"

	"github.com/MuunBob/hoodalpha/internal/domain"
)

// HeadSubscriber streams new block heads over WebSocket and reconnects when the
// connection drops. It records the last head it saw so a health check can tell
// a quiet chain from a wedged socket.
type HeadSubscriber struct {
	wsURL         string
	expectedChain uint64
	dialTimeout   time.Duration
	// reconnectBase is the first backoff delay; it doubles up to reconnectMax.
	reconnectBase time.Duration
	reconnectMax  time.Duration
	log           *slog.Logger

	mu        sync.RWMutex
	last      domain.BlockRef
	lastSeen  time.Time
	connected bool
}

// SubscriberOptions configure a HeadSubscriber.
type SubscriberOptions struct {
	WSURL         string
	ExpectedChain uint64
	DialTimeout   time.Duration
	ReconnectBase time.Duration
	ReconnectMax  time.Duration
	Logger        *slog.Logger
}

// NewHeadSubscriber builds a subscriber. It does not connect until Run.
func NewHeadSubscriber(opts SubscriberOptions) *HeadSubscriber {
	if opts.DialTimeout <= 0 {
		opts.DialTimeout = 10 * time.Second
	}
	if opts.ReconnectBase <= 0 {
		opts.ReconnectBase = time.Second
	}
	if opts.ReconnectMax <= 0 {
		opts.ReconnectMax = 30 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	return &HeadSubscriber{
		wsURL:         opts.WSURL,
		expectedChain: opts.ExpectedChain,
		dialTimeout:   opts.DialTimeout,
		reconnectBase: opts.ReconnectBase,
		reconnectMax:  opts.ReconnectMax,
		log:           opts.Logger.With("component", "head_subscriber"),
	}
}

// Last returns the most recent head observed and when it was observed.
// The zero BlockRef means no head has arrived yet.
func (s *HeadSubscriber) Last() (domain.BlockRef, time.Time) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.last, s.lastSeen
}

// Connected reports whether a subscription is currently live.
func (s *HeadSubscriber) Connected() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.connected
}

// Run blocks until ctx is cancelled, maintaining the subscription across
// disconnects. onHead, when non-nil, is called for every new head; it must not
// block for long since it runs on the subscription goroutine.
func (s *HeadSubscriber) Run(ctx context.Context, onHead func(domain.BlockRef)) error {
	if s.wsURL == "" {
		return errors.New("websocket url is empty")
	}

	backoff := s.reconnectBase
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}

		err := s.session(ctx, onHead)
		s.setConnected(false)

		if ctx.Err() != nil {
			return ctx.Err()
		}
		s.log.Warn("websocket session ended, reconnecting",
			"error", err, "backoff", backoff.String())

		t := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
		if backoff *= 2; backoff > s.reconnectMax {
			backoff = s.reconnectMax
		}
	}
}

// session holds one connection for as long as it stays healthy.
func (s *HeadSubscriber) session(ctx context.Context, onHead func(domain.BlockRef)) error {
	dialCtx, cancel := context.WithTimeout(ctx, s.dialTimeout)
	client, err := ethclient.DialContext(dialCtx, s.wsURL)
	cancel()
	if err != nil {
		return fmt.Errorf("dial websocket: %w", err)
	}
	defer client.Close()

	if s.expectedChain != 0 {
		idCtx, cancel := context.WithTimeout(ctx, s.dialTimeout)
		id, err := client.ChainID(idCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("verify chain id: %w", err)
		}
		if !id.IsUint64() || id.Uint64() != s.expectedChain {
			// Wrong network is not transient; surface it loudly every attempt.
			return fmt.Errorf("%w: websocket reports %s, expected %d",
				domain.ErrWrongChain, id, s.expectedChain)
		}
	}

	headers := make(chan *types.Header, 16)
	subCtx, cancelSub := context.WithCancel(ctx)
	defer cancelSub()

	sub, err := client.SubscribeNewHead(subCtx, headers)
	if err != nil {
		return fmt.Errorf("subscribe new head: %w", err)
	}
	defer sub.Unsubscribe()

	s.setConnected(true)
	s.log.Info("websocket subscription established")

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case err := <-sub.Err():
			if err == nil {
				err = errors.New("subscription closed")
			}
			return err
		case h := <-headers:
			ref := headerToRef(h)
			s.mu.Lock()
			s.last = ref
			s.lastSeen = time.Now().UTC()
			s.mu.Unlock()
			if onHead != nil {
				onHead(ref)
			}
		}
	}
}

func (s *HeadSubscriber) setConnected(v bool) {
	s.mu.Lock()
	s.connected = v
	s.mu.Unlock()
}
