package artifacthub

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"sync"
	"testing"
	"time"
)

// fixture reads a response captured from the real API.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	root, err := os.OpenRoot("testdata")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = root.Close() }()
	b, err := root.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// newTestClient returns a client of a server that answers with handler.
func newTestClient(t *testing.T, handler http.HandlerFunc, mutate ...func(*Config)) (*Client, *requestLog) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	log := &requestLog{}
	cfg := Config{BaseURL: srv.URL, UserAgent: "test/1", RetryDelay: time.Millisecond, OnRequest: log.record}
	for _, m := range mutate {
		m(&cfg)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return c, log
}

type requestLog struct {
	mu    sync.Mutex
	codes []string
}

func (l *requestLog) record(endpoint, code string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.codes = append(l.codes, endpoint+" "+code)
}

func (l *requestLog) all() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.codes...)
}

func TestNew(t *testing.T) {
	for name, cfg := range map[string]Config{
		"relative url":    {BaseURL: "artifacthub.io"},
		"ftp":             {BaseURL: "ftp://artifacthub.io"},
		"key id only":     {BaseURL: "https://artifacthub.io", APIKeyID: "id"},
		"key secret only": {BaseURL: "https://artifacthub.io", APIKeySecret: "secret"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := New(cfg); err == nil {
				t.Fatal("want an error")
			}
		})
	}
}

func TestSearchPackages(t *testing.T) {
	var query map[string][]string
	c, log := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/packages/search" {
			t.Errorf("path = %s", r.URL.Path)
		}
		query = r.URL.Query()
		w.Header().Set("Pagination-Total-Count", "2")
		_, _ = w.Write(fixture(t, "package-search.json"))
	})

	pkgs, err := c.SearchPackages(t.Context(), PackageQuery{Org: "artifacthub"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != 2 {
		t.Fatalf("got %d packages, want 2", len(pkgs))
	}
	p := pkgs[0]
	if p.Name != "artifact-hub" || p.Stars != 96 || p.Repository.Kind != 0 || p.Repository.Publisher() != "artifacthub" ||
		p.SecurityReportSummary == nil || p.SecurityReportSummary.Critical != 9 || p.ProductionOrganizationsCount != 2 {
		t.Errorf("unexpected package %+v", p)
	}
	want := map[string]string{"org": "artifacthub", "deprecated": "true", "facets": "false", "limit": "60", "offset": "0"}
	for k, v := range want {
		if got := query[k]; len(got) != 1 || got[0] != v {
			t.Errorf("query %s = %v, want %s", k, got, v)
		}
	}
	if got := log.all(); len(got) != 1 || got[0] != EndpointPackageSearch+" 200" {
		t.Errorf("requests = %v: the total count should end the search", got)
	}
}

// The API's order isn't stable between pages: a package seen twice counts once.
func TestSearchPackagesPaginatesAndDeduplicates(t *testing.T) {
	const total = 70
	pkg := func(i int) string { return fmt.Sprintf(`{"package_id":"id-%d","name":"p%d"}`, i, i) }
	c, log := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		offset, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		w.Header().Set("Pagination-Total-Count", strconv.Itoa(total))
		var items []json.RawMessage
		for i := offset; i < min(offset+60, total); i++ {
			items = append(items, json.RawMessage(pkg(i)))
		}
		if offset > 0 {
			// The first page's last package moved to the second.
			items = append(items, json.RawMessage(pkg(59)))
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"packages": items})
	})

	pkgs, err := c.SearchPackages(t.Context(), PackageQuery{User: "alice"})
	if err != nil {
		t.Fatal(err)
	}
	if len(pkgs) != total {
		t.Errorf("got %d packages, want %d", len(pkgs), total)
	}
	if got := len(log.all()); got != 2 {
		t.Errorf("%d requests, want 2", got)
	}
}

// Without a total, an empty page ends the search; so does one that adds nothing.
func TestSearchPackagesWithoutTotal(t *testing.T) {
	for name, ignoreOffset := range map[string]bool{"empty page": false, "offset ignored": true} {
		t.Run(name, func(t *testing.T) {
			c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("offset") != "0" && !ignoreOffset {
					_, _ = w.Write([]byte(`{"packages":[]}`))
					return
				}
				_, _ = w.Write([]byte(`{"packages":[{"package_id":"a"}]}`))
			})
			pkgs, err := c.SearchPackages(t.Context(), PackageQuery{Repository: "r"})
			if err != nil {
				t.Fatal(err)
			}
			if len(pkgs) != 1 {
				t.Errorf("got %d packages, want 1", len(pkgs))
			}
		})
	}
}

func TestSearchRepositoriesMatchesNameExactly(t *testing.T) {
	var name string
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		name = r.URL.Query().Get("name")
		// The API takes the name as a regular expression: `ah` also matches artifact-hub's neighbours.
		_, _ = w.Write([]byte(`[
			{"repository_id":"1","name":"ah","kind":12,"organization_name":"artifacthub","last_tracking_ts":5,"last_tracking_errors":"boom"},
			{"repository_id":"2","name":"ahoy","kind":0}]`))
	})

	repos, err := c.SearchRepositories(t.Context(), RepositoryQuery{Name: "ah"})
	if err != nil {
		t.Fatal(err)
	}
	if name != "ah" {
		t.Errorf("name param = %q", name)
	}
	if len(repos) != 1 || repos[0].ID != "1" || repos[0].LastTrackingTS != 5 || repos[0].LastTrackingErrors != "boom" {
		t.Errorf("repos = %+v", repos)
	}
}

func TestSearchRepositoriesFixture(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Pagination-Total-Count", "2")
		_, _ = w.Write(fixture(t, "repository-search.json"))
	})
	repos, err := c.SearchRepositories(t.Context(), RepositoryQuery{Org: "artifacthub"})
	if err != nil {
		t.Fatal(err)
	}
	if len(repos) != 2 || repos[0].Kind != 12 || repos[0].LastScanningTS == 0 || repos[0].Publisher() != "artifacthub" {
		t.Errorf("repos = %+v", repos)
	}
}

func TestGetPackageDetails(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/packages/helm/artifact-hub/artifact-hub" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write(fixture(t, "package.json"))
	})
	d, err := c.GetPackageDetails(t.Context(), 0, "artifact-hub", "artifact-hub")
	if err != nil {
		t.Fatal(err)
	}
	if len(d.AvailableVersions) != 3 || d.Stats.Subscriptions != 21 || d.Stats.Webhooks != 4 {
		t.Errorf("details = %+v", d)
	}
}

func TestGetPackageDetailsUnknownKind(t *testing.T) {
	c, log := newTestClient(t, func(http.ResponseWriter, *http.Request) {})
	_, err := c.GetPackageDetails(t.Context(), 999, "r", "p")
	if !errors.Is(err, ErrUnknownKind) {
		t.Errorf("err = %v, want ErrUnknownKind", err)
	}
	if len(log.all()) != 0 {
		t.Error("an unknown kind sent a request")
	}
}

func TestGetViews(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/packages/abc/views" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write(fixture(t, "views.json"))
	})
	v, err := c.GetViews(t.Context(), "abc")
	if err != nil {
		t.Fatal(err)
	}
	daily := v.Daily()
	if daily["2026-10-22"] != 7 || daily["2026-10-20"] != 3 || daily["2026-10-23"] != 1 || len(daily) != 3 {
		t.Errorf("daily = %v", daily)
	}
}

func TestGetHubStats(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(fixture(t, "hub-stats.json"))
	})
	s, err := c.GetHubStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s.Packages != 21922 || s.Releases != 508242 {
		t.Errorf("stats = %+v", s)
	}
}

func TestSendsHeaders(t *testing.T) {
	var got http.Header
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header
		_, _ = w.Write([]byte(`{}`))
	}, func(cfg *Config) { cfg.APIKeyID, cfg.APIKeySecret = "id", "secret" })
	if _, err := c.GetViews(t.Context(), "abc"); err != nil {
		t.Fatal(err)
	}
	for k, v := range map[string]string{"X-Api-Key-Id": "id", "X-Api-Key-Secret": "secret", "User-Agent": "test/1"} {
		if got.Get(k) != v {
			t.Errorf("header %s = %q, want %q", k, got.Get(k), v)
		}
	}
}

func TestNoKeyNoHeaders(t *testing.T) {
	var got http.Header
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Header
		_, _ = w.Write([]byte(`{}`))
	})
	if _, err := c.GetViews(t.Context(), "abc"); err != nil {
		t.Fatal(err)
	}
	if _, ok := got["X-Api-Key-Id"]; ok {
		t.Error("sent an API key header without a key")
	}
}

func TestNotFound(t *testing.T) {
	c, log := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { http.NotFound(w, nil) })
	_, err := c.GetPackageDetails(t.Context(), 0, "r", "p")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("err = %v, want ErrNotFound", err)
	}
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 404 {
		t.Errorf("err = %v, want a 404 StatusError", err)
	}
	if got := log.all(); len(got) != 1 {
		t.Errorf("requests = %v: a 404 isn't retried", got)
	}
}

func TestRetriesTooManyRequestsAndServerErrors(t *testing.T) {
	var calls int
	c, log := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls++
		switch calls {
		case 1:
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(`{"packages":1,"releases":2}`))
		}
	})
	start := time.Now()
	s, err := c.GetHubStats(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if s.Releases != 2 {
		t.Errorf("stats = %+v", s)
	}
	if time.Since(start) < time.Second {
		t.Error("Retry-After wasn't honoured")
	}
	want := []string{"/packages/stats 429", "/packages/stats 502", "/packages/stats 200"}
	if got := log.all(); fmt.Sprint(got) != fmt.Sprint(want) {
		t.Errorf("requests = %v, want %v", got, want)
	}
}

func TestGivesUpAfterRetries(t *testing.T) {
	c, log := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	_, err := c.GetHubStats(t.Context())
	var se *StatusError
	if !errors.As(err, &se) || se.Code != 500 {
		t.Errorf("err = %v, want a 500 StatusError", err)
	}
	if got := len(log.all()); got != maxRetries+1 {
		t.Errorf("%d requests, want %d", got, maxRetries+1)
	}
}

func TestRetriesNetworkErrors(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close() // Nothing listens.
	log := &requestLog{}
	c, err := New(Config{BaseURL: srv.URL, RetryDelay: time.Millisecond, OnRequest: log.record})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.GetHubStats(t.Context()); err == nil {
		t.Fatal("want an error")
	}
	if got := log.all(); len(got) != maxRetries+1 || got[0] != "/packages/stats error" {
		t.Errorf("requests = %v", got)
	}
}

func TestCancelStopsRetries(t *testing.T) {
	c, log := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}, func(cfg *Config) { cfg.RetryDelay = time.Hour })
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_, err := c.GetHubStats(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's", err)
	}
	if got := len(log.all()); got != 1 {
		t.Errorf("%d requests, want 1", got)
	}
}

func TestBadJSON(t *testing.T) {
	c, log := newTestClient(t, func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`<html>`)) })
	if _, err := c.GetHubStats(t.Context()); err == nil {
		t.Fatal("want an error")
	}
	if got := len(log.all()); got != 1 {
		t.Errorf("%d requests, want 1: a bad body isn't retried", got)
	}
}

func TestBaseURLWithPath(t *testing.T) {
	c, _ := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/hub/api/v1/packages/stats" {
			t.Errorf("path = %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(`{}`))
	}, func(cfg *Config) { cfg.BaseURL += "/hub/" })
	if _, err := c.GetHubStats(t.Context()); err != nil {
		t.Fatal(err)
	}
}

func TestKind(t *testing.T) {
	for i := range kindSlugs {
		k := Kind(i)
		slug, ok := k.Slug()
		if !ok || slug == "" {
			t.Errorf("kind %d has no slug", i)
			continue
		}
		if back, ok := ParseKind(slug); !ok || back != k {
			t.Errorf("ParseKind(%q) = %d, %v; want %d", slug, back, ok, i)
		}
	}
	if s, _ := Kind(0).Slug(); s != "helm" {
		t.Errorf("kind 0 = %q", s)
	}
	if s, _ := Kind(12).Slug(); s != "container" {
		t.Errorf("kind 12 = %q", s)
	}
	for _, k := range []Kind{-1, Kind(len(kindSlugs)), 99} {
		if _, ok := k.Slug(); ok {
			t.Errorf("kind %d has a slug", k)
		}
	}
	if got := Kind(99).String(); got != "kind-99" {
		t.Errorf("String() = %q", got)
	}
	if _, ok := ParseKind("nope"); ok {
		t.Error("parsed an unknown slug")
	}
}
