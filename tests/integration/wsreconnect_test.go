package integration

import (
	"context"
	"log/slog"
	"os/exec"
	"testing"
	"time"

	"github.com/MuunBob/hoodalpha/internal/chain"
	"github.com/MuunBob/hoodalpha/internal/domain"
)

// TestWebSocketSurvivesNodeRestart is the real reconnect proof: it kills the
// node mid-subscription and requires heads to resume afterwards.
func TestWebSocketSurvivesNodeRestart(t *testing.T) {
	if testing.Short() {
		t.Skip("restarts a container")
	}
	url := wsURL(t)

	sub := chain.NewHeadSubscriber(chain.SubscriberOptions{
		WSURL:         url,
		ExpectedChain: expectedChainID(),
		DialTimeout:   10 * time.Second,
		ReconnectBase: 500 * time.Millisecond,
		ReconnectMax:  3 * time.Second,
		Logger:        slog.Default(),
	})

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	heads := make(chan domain.BlockRef, 64)
	go func() { _ = sub.Run(ctx, func(r domain.BlockRef) { heads <- r }) }()

	waitHead := func(what string) domain.BlockRef {
		t.Helper()
		select {
		case h := <-heads:
			return h
		case <-time.After(60 * time.Second):
			t.Fatalf("no head %s within 60s", what)
			return domain.BlockRef{}
		}
	}

	before := waitHead("before restart")
	t.Logf("head before restart: %d", before.Number)

	if out, err := exec.Command("docker", "restart", "anvil-test").CombinedOutput(); err != nil {
		t.Fatalf("restart node: %v (%s)", err, out)
	}
	t.Log("node restarted; subscription must recover on its own")

	// Drain anything buffered from the pre-restart connection.
	for len(heads) > 0 {
		<-heads
	}

	after := waitHead("after restart")
	t.Logf("head after restart: %d", after.Number)

	if !sub.Connected() {
		t.Error("Connected() = false after recovery")
	}
	last, seenAt := sub.Last()
	if last.Number == 0 || time.Since(seenAt) > time.Minute {
		t.Errorf("Last() = (%d, %v) after recovery", last.Number, seenAt)
	}
}
