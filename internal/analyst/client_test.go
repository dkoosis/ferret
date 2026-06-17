package analyst

import (
	"context"
	"net/http"
	"testing"
	"time"
)

// hangingDoer is a stub transport that never responds — it blocks until the
// request's context is cancelled, then returns that context error. It models a
// wedged/throttled API so the operator-deadline path can be exercised without a
// live network (ferret-c71).
type hangingDoer struct{}

func (hangingDoer) Do(req *http.Request) (*http.Response, error) {
	<-req.Context().Done()
	return nil, req.Context().Err()
}

// TestCompleteRespectsOperatorTimeout asserts a Config.Timeout bounds the whole
// call: against a transport that never responds, complete returns an error
// promptly (well inside the SDK's ~10min/attempt × ~3 retries) rather than
// wedging the CLI with no operator-settable ceiling.
func TestCompleteRespectsOperatorTimeout(t *testing.T) {
	cfg := Config{APIKey: "sk-test", Timeout: 50 * time.Millisecond, HTTPClient: hangingDoer{}}

	start := time.Now()
	_, _, err := complete(context.Background(), cfg, "system", "user")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("complete() err = nil; want a deadline error from the bounded context")
	}
	if elapsed > 5*time.Second {
		t.Errorf("complete() took %v against a hanging transport; the %v operator timeout did not bound it",
			elapsed, cfg.Timeout)
	}
}

// TestCompleteCancelsOnContextCancel asserts the threaded context cancels the
// call cooperatively — the seam SIGINT uses (signal.NotifyContext) to abort a
// wedged analyst run without a hard kill.
func TestCompleteCancelsOnContextCancel(t *testing.T) {
	cfg := Config{APIKey: "sk-test", HTTPClient: hangingDoer{}}
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	_, _, err := complete(ctx, cfg, "system", "user")
	if err == nil {
		t.Fatal("complete() err = nil; want a cancellation error")
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("complete() took %v; expected prompt return on context cancel", elapsed)
	}
}
