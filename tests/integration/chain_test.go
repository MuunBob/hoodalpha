package integration

import (
	"context"
	"errors"
	"log/slog"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/chain"
	"github.com/MuunBob/hoodalpha/internal/domain"
)

func dialChain(t *testing.T) *chain.Client {
	t.Helper()
	ctx := testContext(t, 30*time.Second)
	c, err := chain.Dial(ctx, chain.Options{
		RPCURL:         rpcURL(t),
		ExpectedChain:  expectedChainID(),
		RequestTimeout: 20 * time.Second,
		MaxRetries:     3,
		RetryBackoff:   500 * time.Millisecond,
	})
	if err != nil {
		t.Fatalf("Dial() error = %v", err)
	}
	t.Cleanup(c.Close)
	return c
}

func TestChainIDVerification(t *testing.T) {
	c := dialChain(t)
	ctx := testContext(t, 30*time.Second)

	id, err := c.ChainID(ctx)
	if err != nil {
		t.Fatalf("ChainID() error = %v", err)
	}
	if id != expectedChainID() {
		t.Errorf("chain id = %d, want %d", id, expectedChainID())
	}
	t.Logf("connected to %s (chain id %d)", domain.ChainName(id), id)
}

// TestDialRejectsWrongChain is the safety property that matters most here:
// pointing the bot at the wrong network must fail at startup, not silently
// produce meaningless balances.
func TestDialRejectsWrongChain(t *testing.T) {
	ctx := testContext(t, 30*time.Second)
	c, err := chain.Dial(ctx, chain.Options{
		RPCURL:         rpcURL(t),
		ExpectedChain:  expectedChainID() + 1, // deliberately wrong
		RequestTimeout: 20 * time.Second,
		MaxRetries:     1,
		RetryBackoff:   200 * time.Millisecond,
	})
	if err == nil {
		c.Close()
		t.Fatal("Dial() succeeded against the wrong chain id")
	}
	if !errors.Is(err, domain.ErrWrongChain) {
		t.Errorf("error = %v, want domain.ErrWrongChain", err)
	}
}

func TestLatestBlock(t *testing.T) {
	c := dialChain(t)
	ctx := testContext(t, 30*time.Second)

	block, err := c.LatestBlock(ctx)
	if err != nil {
		t.Fatalf("LatestBlock() error = %v", err)
	}
	if block.Number == 0 {
		t.Error("latest block number = 0")
	}
	if _, err := domain.ParseHash(block.Hash.String()); err != nil {
		t.Errorf("block hash is not canonical: %v", err)
	}
	if block.Time.IsZero() {
		t.Error("block time is zero")
	}
	// A live chain's head should not be hours old.
	if age := time.Since(block.Time); age > time.Hour {
		t.Errorf("head is %v old; endpoint may be stale or archival", age)
	}
	t.Logf("head: block %d at %s", block.Number, block.Time.Format(time.RFC3339))
}

func TestBlockByNumberMatchesParentLink(t *testing.T) {
	c := dialChain(t)
	ctx := testContext(t, 30*time.Second)

	head, err := c.LatestBlock(ctx)
	if err != nil {
		t.Fatalf("LatestBlock() error = %v", err)
	}
	if head.Number == 0 {
		t.Skip("chain has only the genesis block")
	}

	parent, err := c.BlockByNumber(ctx, head.Number-1)
	if err != nil {
		t.Fatalf("BlockByNumber() error = %v", err)
	}
	if parent.Number != head.Number-1 {
		t.Errorf("block number = %d, want %d", parent.Number, head.Number-1)
	}
	// The head's ParentHash must be the previous block's hash. This is the
	// invariant a reorg detector will later rely on.
	if head.ParentHash != parent.Hash {
		t.Errorf("head.ParentHash = %s, parent.Hash = %s", head.ParentHash, parent.Hash)
	}
}

func TestBalanceAt(t *testing.T) {
	c := dialChain(t)
	ctx := testContext(t, 30*time.Second)

	// The zero address accumulates burned/mis-sent value on most chains and
	// always exists, so it is a safe read target that needs no funded wallet.
	zero, err := domain.ParseAddress("0x0000000000000000000000000000000000000000")
	if err != nil {
		t.Fatal(err)
	}

	bal, err := c.BalanceAt(ctx, zero, nil)
	if err != nil {
		t.Fatalf("BalanceAt() error = %v", err)
	}
	if bal.BigInt().Sign() < 0 {
		t.Error("balance is negative")
	}
	t.Logf("zero-address balance: %s wei (%s ETH)", bal.String(), bal.Ether())

	if _, err := c.BalanceAt(ctx, domain.Address("not-an-address"), nil); err == nil {
		t.Error("BalanceAt() accepted a malformed address")
	}
}

func TestTransactionAndReceiptLookup(t *testing.T) {
	c := dialChain(t)
	ctx := testContext(t, time.Minute)

	// Walk back from the head until a block with transactions turns up.
	head, err := c.LatestBlock(ctx)
	if err != nil {
		t.Fatalf("LatestBlock() error = %v", err)
	}

	var txHash domain.Hash
	for n := head.Number; n > 0 && n+50 > head.Number; n-- {
		logs, err := c.FilterLogs(ctx, chain.LogQuery{FromBlock: &n, ToBlock: &n})
		if err != nil {
			t.Fatalf("FilterLogs() error = %v", err)
		}
		if len(logs) > 0 {
			txHash = logs[0].TxHash
			break
		}
	}
	if txHash == "" {
		t.Skip("no transactions found in the last 50 blocks")
	}

	tx, err := c.TransactionByHash(ctx, txHash)
	if err != nil {
		t.Fatalf("TransactionByHash(%s) error = %v", txHash, err)
	}
	if tx.Hash != txHash {
		t.Errorf("tx hash = %s, want %s", tx.Hash, txHash)
	}
	if _, err := domain.ParseAddress(tx.From.String()); err != nil {
		t.Errorf("sender is not canonical: %v", err)
	}

	receipt, err := c.ReceiptByHash(ctx, txHash)
	if err != nil {
		t.Fatalf("ReceiptByHash() error = %v", err)
	}
	if receipt.TxHash != txHash {
		t.Errorf("receipt hash = %s, want %s", receipt.TxHash, txHash)
	}
	if receipt.BlockNumber == 0 {
		t.Error("receipt block number = 0")
	}
	t.Logf("tx %s: status=%d gasUsed=%d", txHash, receipt.Status, receipt.GasUsed)
}

func TestLookupOfUnknownHashIsNotFound(t *testing.T) {
	c := dialChain(t)
	ctx := testContext(t, 30*time.Second)

	unknown := domain.Hash("0x" +
		"deadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeefdeadbeef")

	if _, err := c.TransactionByHash(ctx, unknown); !errors.Is(err, chain.ErrNotFound) {
		t.Errorf("TransactionByHash error = %v, want ErrNotFound", err)
	}
	if _, err := c.ReceiptByHash(ctx, unknown); !errors.Is(err, chain.ErrNotFound) {
		t.Errorf("ReceiptByHash error = %v, want ErrNotFound", err)
	}
}

func TestFilterLogs(t *testing.T) {
	c := dialChain(t)
	ctx := testContext(t, time.Minute)

	head, err := c.LatestBlock(ctx)
	if err != nil {
		t.Fatalf("LatestBlock() error = %v", err)
	}
	from := head.Number
	if from > 10 {
		from = head.Number - 10
	}

	logs, err := c.FilterLogs(ctx, chain.LogQuery{FromBlock: &from, ToBlock: &head.Number})
	if err != nil {
		t.Fatalf("FilterLogs() error = %v", err)
	}
	t.Logf("%d logs across blocks %d..%d", len(logs), from, head.Number)

	for _, l := range logs {
		if _, err := domain.ParseAddress(l.Address.String()); err != nil {
			t.Fatalf("log address is not canonical: %v", err)
		}
		if l.BlockNumber < from || l.BlockNumber > head.Number {
			t.Fatalf("log block %d outside requested range %d..%d",
				l.BlockNumber, from, head.Number)
		}
	}
}

// TestRPCFailsSafelyWhenUnavailable covers the "infrastructure is down" path:
// the client must return an error promptly, not hang or spin.
func TestRPCFailsSafelyWhenUnavailable(t *testing.T) {
	// Port 1 on loopback refuses connections immediately.
	ctx := testContext(t, 30*time.Second)

	start := time.Now()
	_, err := chain.Dial(ctx, chain.Options{
		RPCURL:         "http://127.0.0.1:1",
		ExpectedChain:  expectedChainID(),
		RequestTimeout: 2 * time.Second,
		MaxRetries:     2,
		RetryBackoff:   100 * time.Millisecond,
	})
	if err == nil {
		t.Fatal("Dial() succeeded against a dead endpoint")
	}
	// 1 attempt + 2 retries with 100ms/200ms backoff must finish well inside
	// the bound; an unbounded loop would run until the test context expires.
	if elapsed := time.Since(start); elapsed > 15*time.Second {
		t.Errorf("Dial() took %v to fail; retry loop is not bounded", elapsed)
	}
}

// TestWebSocketReceivesHeads verifies the push subscription actually delivers.
func TestWebSocketReceivesHeads(t *testing.T) {
	url := wsURL(t)

	sub := chain.NewHeadSubscriber(chain.SubscriberOptions{
		WSURL:         url,
		ExpectedChain: expectedChainID(),
		DialTimeout:   20 * time.Second,
		Logger:        slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	heads := make(chan domain.BlockRef, 4)
	go func() { _ = sub.Run(ctx, func(ref domain.BlockRef) { heads <- ref }) }()

	select {
	case ref := <-heads:
		if ref.Number == 0 {
			t.Error("received head with block number 0")
		}
		if _, err := domain.ParseHash(ref.Hash.String()); err != nil {
			t.Errorf("head hash is not canonical: %v", err)
		}
		t.Logf("received head %d", ref.Number)
	case <-ctx.Done():
		t.Fatal("no head received within 90s")
	}

	if !sub.Connected() {
		t.Error("Connected() = false after receiving a head")
	}
	last, seenAt := sub.Last()
	if last.Number == 0 || seenAt.IsZero() {
		t.Errorf("Last() = (%d, %v); want a recorded head", last.Number, seenAt)
	}
}

// TestWebSocketReconnectsAfterFailure points the subscriber at a dead endpoint
// and confirms it keeps retrying with backoff rather than giving up or spinning.
func TestWebSocketReconnectsAfterFailure(t *testing.T) {
	sub := chain.NewHeadSubscriber(chain.SubscriberOptions{
		WSURL:         "ws://127.0.0.1:1",
		DialTimeout:   time.Second,
		ReconnectBase: 200 * time.Millisecond,
		ReconnectMax:  time.Second,
		Logger:        slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- sub.Run(ctx, nil) }()

	select {
	case err := <-done:
		// Run must survive repeated dial failures and only exit on cancellation.
		if !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			t.Errorf("Run() returned %v; want it to keep retrying until ctx ends", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run() did not return after context expiry")
	}
	if sub.Connected() {
		t.Error("Connected() = true after only failed dials")
	}
}

// TestWebSocketRejectsWrongChain proves the subscriber verifies the network too,
// not just the HTTP client.
func TestWebSocketRejectsWrongChain(t *testing.T) {
	url := wsURL(t)

	sub := chain.NewHeadSubscriber(chain.SubscriberOptions{
		WSURL:         url,
		ExpectedChain: expectedChainID() + 1,
		DialTimeout:   20 * time.Second,
		ReconnectBase: 100 * time.Millisecond,
		ReconnectMax:  200 * time.Millisecond,
		Logger:        slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	received := make(chan domain.BlockRef, 1)
	go func() { _ = sub.Run(ctx, func(r domain.BlockRef) { received <- r }) }()

	select {
	case ref := <-received:
		t.Fatalf("received head %d from the wrong chain", ref.Number)
	case <-time.After(8 * time.Second):
		// Expected: it never establishes a usable subscription.
	}
	if sub.Connected() {
		t.Error("Connected() = true against the wrong chain")
	}
}
