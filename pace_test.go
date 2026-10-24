package artifacthub

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"
)

func TestRequestInterval(t *testing.T) {
	const interval = 50 * time.Millisecond
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"packages":1,"releases":2}`))
	}, func(cfg *Config) { cfg.RequestInterval = interval })

	start := time.Now()
	for range 4 {
		if _, err := c.GetHubStats(t.Context()); err != nil {
			t.Fatal(err)
		}
	}
	// The first request goes at once, each other one waits its turn.
	if got := time.Since(start); got < 3*interval {
		t.Errorf("4 requests took %s, want at least %s", got, 3*interval)
	}
}

func TestRequestIntervalCancel(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{}`))
	}, func(cfg *Config) { cfg.RequestInterval = time.Hour })
	if _, err := c.GetHubStats(t.Context()); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()
	if _, err := c.GetHubStats(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want deadline exceeded", err)
	}
}
