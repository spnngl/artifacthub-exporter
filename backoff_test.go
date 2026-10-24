package artifacthub

import (
	"net/http"
	"testing"
	"time"
)

func TestRateLimitBackoffIsLongerThanServerErrorBackoff(t *testing.T) {
	const retryDelay = 10 * time.Millisecond
	for name, tc := range map[string]struct {
		status int
		min    time.Duration
		max    time.Duration
	}{
		"429": {http.StatusTooManyRequests, retryDelay * rateLimitFactor, time.Hour},
		"502": {http.StatusBadGateway, retryDelay, retryDelay * rateLimitFactor},
	} {
		t.Run(name, func(t *testing.T) {
			var calls int
			c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
				calls++
				if calls == 1 {
					w.WriteHeader(tc.status)
					return
				}
				_, _ = w.Write([]byte(`{}`))
			}, func(cfg *Config) { cfg.RetryDelay = retryDelay })

			start := time.Now()
			if _, err := c.GetHubStats(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := time.Since(start); got < tc.min || got >= tc.max {
				t.Errorf("waited %s, want within [%s, %s)", got, tc.min, tc.max)
			}
		})
	}
}
