// Command artifacthub-exporter exposes Artifact Hub metrics to Prometheus.
package main

import (
	"context"
	"errors"
	"io/fs"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	arg "github.com/alexflint/go-arg"
	"github.com/joho/godotenv"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	artifacthub "github.com/spnngl/artifacthub-exporter"
)

// version is the release, set by the release build:
// go build -ldflags "-X main.version=<version>".
var version = "dev"

// shutdownTimeout bounds the scrapes in flight at shutdown, within
// Kubernetes' default 30 s grace period.
const shutdownTimeout = 20 * time.Second

// requestInterval paces the requests to the API, which a CloudFront rate
// limit answers with 429s past roughly 100 per minute (measured: the
// limits are not documented).
const requestInterval = time.Second

func main() {
	// .env is optional; one that can't be read is a configuration error.
	if err := godotenv.Load(); err != nil && !errors.Is(err, fs.ErrNotExist) {
		log.Fatalf("load .env: %v", err)
	}

	cfg := defaultConfig()
	p := arg.MustParse(&cfg)
	targets, err := cfg.targets()
	if err != nil {
		p.Fail(err.Error())
	}

	metrics := newSelfMetrics(prometheus.DefaultRegisterer)
	client, err := artifacthub.New(artifacthub.Config{
		BaseURL:         cfg.URL,
		APIKeyID:        cfg.APIKeyID,
		APIKeySecret:    cfg.APIKeySecret,
		UserAgent:       "artifacthub-exporter/" + version,
		RequestInterval: requestInterval,
		OnRequest: func(endpoint, code string) {
			metrics.requests.WithLabelValues(endpoint, code).Inc()
		},
	})
	if err != nil {
		p.Fail(err.Error())
	}
	log.Println("starting artifacthub-exporter", version, "collecting", len(targets), "targets every", cfg.CollectInterval)

	store := newStore(targets)
	prometheus.MustRegister(newCollector(store))

	mux := http.NewServeMux()
	mux.Handle(cfg.WebPath, promhttp.Handler())
	mux.HandleFunc("/{$}", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`<html>
			<head><title>Artifact Hub Exporter</title></head>
			<body>
			<h1>Artifact Hub Exporter</h1>
			<p><a href="` + cfg.WebPath + `">Metrics</a></p>
			</body>
			</html>`))
	})
	srv := &http.Server{Addr: cfg.WebAddr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}

	// On SIGTERM (docker stop, Kubernetes) or SIGINT: stop collecting and
	// accepting scrapes, finish those in flight, exit 0. stop() restores the
	// default handling, so a second signal kills at once.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	go newPoller(client, targets, store, metrics, cfg.CollectInterval).run(ctx)

	stopped := make(chan struct{})
	go func() {
		<-ctx.Done()
		stop()
		log.Println("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			log.Printf("shutdown: %v", err)
		}
		close(stopped)
	}()

	// ListenAndServe returns as Shutdown starts: wait for it to finish.
	if err := srv.ListenAndServe(); !errors.Is(err, http.ErrServerClosed) {
		log.Fatal(err)
	}
	<-stopped
}
