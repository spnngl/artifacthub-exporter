package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
	artifacthub "github.com/spnngl/artifacthub-exporter"
)

// fakeAPI is an in-memory Artifact Hub. Err fails every call.
type fakeAPI struct {
	repos    []artifacthub.Repository
	packages []artifacthub.PackageSummary
	views    artifacthub.Views
	hub      artifacthub.HubStats
	err      error
	// detailErr fails the details of the packages of that name.
	detailErr map[string]error

	calls map[string]int
}

func (f *fakeAPI) count(name string) {
	if f.calls == nil {
		f.calls = make(map[string]int)
	}
	f.calls[name]++
}

func (f *fakeAPI) SearchPackages(_ context.Context, q artifacthub.PackageQuery) ([]artifacthub.PackageSummary, error) {
	f.count("SearchPackages")
	if f.err != nil {
		return nil, f.err
	}
	var out []artifacthub.PackageSummary
	for _, p := range f.packages {
		r := p.Repository
		if (q.Org != "" && r.OrganizationName == q.Org) || (q.User != "" && r.UserAlias == q.User) ||
			(q.Repository != "" && r.Name == q.Repository) {
			out = append(out, p)
		}
	}
	return out, nil
}

func (f *fakeAPI) SearchRepositories(_ context.Context, q artifacthub.RepositoryQuery) ([]artifacthub.Repository, error) {
	f.count("SearchRepositories")
	if f.err != nil {
		return nil, f.err
	}
	var out []artifacthub.Repository
	for _, r := range f.repos {
		if (q.Org != "" && r.OrganizationName == q.Org) || (q.User != "" && r.UserAlias == q.User) ||
			(q.Name != "" && r.Name == q.Name) {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeAPI) GetPackageDetails(_ context.Context, kind artifacthub.Kind, _, name string) (artifacthub.PackageDetails, error) {
	f.count("GetPackageDetails")
	if f.err != nil {
		return artifacthub.PackageDetails{}, f.err
	}
	if err := f.detailErr[name]; err != nil {
		return artifacthub.PackageDetails{}, err
	}
	if _, ok := kind.Slug(); !ok {
		return artifacthub.PackageDetails{}, artifacthub.ErrUnknownKind
	}
	var d artifacthub.PackageDetails
	d.AvailableVersions = make([]struct {
		Version string `json:"version"`
	}, 3)
	d.Stats.Subscriptions = 4
	d.Stats.Webhooks = 2
	return d, nil
}

func (f *fakeAPI) GetViews(context.Context, string) (artifacthub.Views, error) {
	f.count("GetViews")
	return f.views, f.err
}

func (f *fakeAPI) GetHubStats(context.Context) (artifacthub.HubStats, error) {
	f.count("GetHubStats")
	return f.hub, f.err
}

var now = time.Date(2024, 5, 10, 12, 0, 0, 0, time.UTC)

func repo(id, name, org string) artifacthub.Repository {
	return artifacthub.Repository{
		RepositorySummary: artifacthub.RepositorySummary{ID: id, Kind: 0, Name: name, OrganizationName: org, URL: "https://example.com/" + name},
		LastTrackingTS:    100,
	}
}

func pkg(id, name string, r artifacthub.Repository) artifacthub.PackageSummary {
	return artifacthub.PackageSummary{ID: id, Name: name, Version: "1.0.0", Stars: 7, TS: 50, Repository: r.RepositorySummary}
}

func newFixture(t *testing.T, targets ...Target) (*poller, *fakeAPI, *prometheus.Registry) {
	t.Helper()
	r := repo("r1", "charts", "acme")
	f := &fakeAPI{
		repos:    []artifacthub.Repository{r, repo("r2", "other", "acme")},
		packages: []artifacthub.PackageSummary{pkg("p1", "web", r), pkg("p2", "db", r)},
		views: artifacthub.Views{
			"1.0.0": {"2024-05-10": 100, "2024-05-09": 5, "2024-05-03": 1, "2024-05-02": 1000},
			"0.9.0": {"2024-05-09": 2},
		},
		hub: artifacthub.HubStats{Packages: 10, Releases: 20},
	}
	s := newStore(targets)
	reg := prometheus.NewRegistry()
	m := newSelfMetrics(reg)
	reg.MustRegister(newCollector(s))
	p := newPoller(f, targets, s, m, time.Hour)
	p.now = func() time.Time { return now }
	return p, f, reg
}

// find returns the value of the metric whose labels include all of want.
func find(t *testing.T, reg *prometheus.Registry, name string, want map[string]string) (float64, bool) {
	t.Helper()
	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if mf.GetName() != name {
			continue
		}
		for _, m := range mf.GetMetric() {
			if matches(m, want) {
				switch {
				case m.GetGauge() != nil:
					return m.GetGauge().GetValue(), true
				case m.GetCounter() != nil:
					return m.GetCounter().GetValue(), true
				}
			}
		}
	}
	return 0, false
}

func matches(m *dto.Metric, want map[string]string) bool {
	have := make(map[string]string)
	for _, l := range m.GetLabel() {
		have[l.GetName()] = l.GetValue()
	}
	for k, v := range want {
		if have[k] != v {
			return false
		}
	}
	return true
}

func mustValue(t *testing.T, reg *prometheus.Registry, name string, want map[string]string, expected float64) {
	t.Helper()
	got, ok := find(t, reg, name, want)
	if !ok {
		t.Fatalf("%s%v: missing", name, want)
	}
	if got != expected {
		t.Errorf("%s%v = %v, want %v", name, want, got, expected)
	}
}

func TestCountViews(t *testing.T) {
	v := artifacthub.Views{
		"a": {"2024-05-10": 100, "2024-05-09": 5, "2024-05-03": 1, "2024-05-02": 1000},
		"b": {"2024-05-09": 2},
	}
	got := countViews(v, now)
	if want := (viewCounts{yesterday: 7, last7d: 8}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got := countViews(nil, now); got != (viewCounts{}) {
		t.Errorf("no views: got %+v", got)
	}
	// 23:59 UTC is still the same day.
	if got := countViews(v, time.Date(2024, 5, 10, 23, 59, 0, 0, time.UTC)); got.last7d != 8 {
		t.Errorf("late in the day: got %+v", got)
	}
}

func TestOrgTarget(t *testing.T) {
	p, _, reg := newFixture(t, OrgTarget{Name: "acme"})
	p.collectAll(t.Context())

	web := map[string]string{"kind": "helm", "repository": "charts", "package": "web"}
	mustValue(t, reg, "artifacthub_package_stars", web, 7)
	mustValue(t, reg, "artifacthub_package_versions", web, 3)
	mustValue(t, reg, "artifacthub_package_subscriptions", web, 4)
	mustValue(t, reg, "artifacthub_package_webhooks", web, 2)
	mustValue(t, reg, "artifacthub_package_views_last_7d", web, 8)
	mustValue(t, reg, "artifacthub_package_views_yesterday", web, 7)
	mustValue(t, reg, "artifacthub_package_info", map[string]string{"package": "web", "version": "1.0.0", "publisher": "acme"}, 1)
	mustValue(t, reg, "artifacthub_repository_packages", map[string]string{"repository": "charts"}, 2)
	mustValue(t, reg, "artifacthub_repository_packages", map[string]string{"repository": "other"}, 0)
	mustValue(t, reg, "artifacthub_repository_last_tracking_timestamp_seconds", map[string]string{"repository": "charts"}, 100)
	mustValue(t, reg, "artifacthub_collection_last_success_timestamp_seconds", map[string]string{"target": "org:acme"}, float64(now.Unix()))
	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "org:acme"}, 0)

	if _, ok := find(t, reg, "artifacthub_package_vulnerabilities", nil); ok {
		t.Error("vulnerabilities exported without a security report")
	}
	if _, ok := find(t, reg, "artifacthub_hub_packages", nil); ok {
		t.Error("hub stats exported without the hub target")
	}
}

func TestUnknownOwnerIsAnError(t *testing.T) {
	p, _, reg := newFixture(t, OrgTarget{Name: "nope"}, UserTarget{Alias: "nope"})
	p.collectAll(t.Context())

	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "org:nope"}, 1)
	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "user:nope"}, 1)
}

func TestVulnerabilities(t *testing.T) {
	p, f, reg := newFixture(t, RepositoryTarget{Name: "charts"})
	f.packages[0].SecurityReportSummary = &artifacthub.SecurityReportSummary{Critical: 1, High: 2, Medium: 3, Low: 4, Unknown: 5}
	p.collectAll(t.Context())

	for severity, n := range map[string]float64{"critical": 1, "high": 2, "medium": 3, "low": 4, "unknown": 5} {
		mustValue(t, reg, "artifacthub_package_vulnerabilities", map[string]string{"package": "web", "severity": severity}, n)
	}
	if _, ok := find(t, reg, "artifacthub_package_vulnerabilities", map[string]string{"package": "db"}); ok {
		t.Error("vulnerabilities exported for the package without a report")
	}
}

func TestPackageTarget(t *testing.T) {
	p, _, reg := newFixture(t, PackageTarget{Kind: 0, Repository: "charts", Name: "web"})
	p.collectAll(t.Context())

	mustValue(t, reg, "artifacthub_package_stars", map[string]string{"package": "web"}, 7)
	if _, ok := find(t, reg, "artifacthub_package_stars", map[string]string{"package": "db"}); ok {
		t.Error("other package of the repository exported")
	}
	mustValue(t, reg, "artifacthub_repository_info", map[string]string{"repository": "charts"}, 1)
	if _, ok := find(t, reg, "artifacthub_repository_packages", nil); ok {
		t.Error("repository package count exported for a single package")
	}
}

func TestPackageTargetNotFound(t *testing.T) {
	for name, target := range map[string]PackageTarget{
		"name":       {Kind: 0, Repository: "charts", Name: "nope"},
		"kind":       {Kind: 1, Repository: "charts", Name: "web"},
		"repository": {Kind: 0, Repository: "nope", Name: "web"},
	} {
		t.Run(name, func(t *testing.T) {
			p, _, reg := newFixture(t, target)
			p.collectAll(t.Context())
			mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": target.String()}, 1)
			if _, ok := find(t, reg, "artifacthub_package_info", nil); ok {
				t.Error("metrics exported for a missing package")
			}
			if _, ok := find(t, reg, "artifacthub_collection_last_success_timestamp_seconds", nil); ok {
				t.Error("success recorded")
			}
		})
	}
}

func TestOverlappingTargetsExportOnce(t *testing.T) {
	p, f, reg := newFixture(t, OrgTarget{Name: "acme"}, RepositoryTarget{Name: "charts"}, PackageTarget{Kind: 0, Repository: "charts", Name: "web"})
	p.collectAll(t.Context())

	// A duplicate would fail the gather.
	if n := testutil.CollectAndCount(newCollector(p.store), "artifacthub_package_info"); n != 2 {
		t.Errorf("package_info series = %d, want 2", n)
	}
	if _, err := reg.Gather(); err != nil {
		t.Fatal(err)
	}
	// The package count comes from a target that lists the whole repository.
	mustValue(t, reg, "artifacthub_repository_packages", map[string]string{"repository": "charts"}, 2)
	// Details and views are fetched once per package and cycle.
	if f.calls["GetViews"] != 2 || f.calls["GetPackageDetails"] != 2 {
		t.Errorf("calls = %v, want one details and views per package", f.calls)
	}
}

func TestPackageTargetFirstDoesNotHidePackageCount(t *testing.T) {
	p, _, reg := newFixture(t, PackageTarget{Kind: 0, Repository: "charts", Name: "web"}, RepositoryTarget{Name: "charts"})
	p.collectAll(t.Context())
	mustValue(t, reg, "artifacthub_repository_packages", map[string]string{"repository": "charts"}, 2)
}

func TestFailedTargetKeepsPreviousSnapshot(t *testing.T) {
	p, f, reg := newFixture(t, OrgTarget{Name: "acme"})
	p.collectAll(t.Context())

	f.err = errors.New("boom")
	later := now.Add(time.Hour)
	p.now = func() time.Time { return later }
	p.collectAll(t.Context())

	mustValue(t, reg, "artifacthub_package_stars", map[string]string{"package": "web"}, 7)
	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "org:acme"}, 1)
	mustValue(t, reg, "artifacthub_collection_last_success_timestamp_seconds", map[string]string{"target": "org:acme"}, float64(now.Unix()))
}

func TestFailedPackageFallsBackToItsPreviousData(t *testing.T) {
	p, f, reg := newFixture(t, RepositoryTarget{Name: "charts"})
	p.collectAll(t.Context())

	// The search is fresh, the details of "web" fail: the cycle stops fetching.
	f.packages[0].Stars = 8
	f.packages[1].Stars = 9
	f.detailErr = map[string]error{"web": errors.New("boom")}
	f.calls = nil
	later := now.Add(time.Hour)
	p.now = func() time.Time { return later }
	p.collectAll(t.Context())

	web := map[string]string{"package": "web"}
	db := map[string]string{"package": "db"}
	mustValue(t, reg, "artifacthub_package_stars", web, 8)
	mustValue(t, reg, "artifacthub_package_stars", db, 9)
	mustValue(t, reg, "artifacthub_package_versions", web, 3)
	mustValue(t, reg, "artifacthub_package_versions", db, 3)
	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "repository:charts"}, 1)
	mustValue(t, reg, "artifacthub_collection_last_success_timestamp_seconds", map[string]string{"target": "repository:charts"}, float64(now.Unix()))
	if f.calls["GetPackageDetails"] != 1 || f.calls["GetViews"] != 0 {
		t.Errorf("calls = %v, want the fetching to stop at the first failure", f.calls)
	}

	// The next cycle, with the hub back, collects everything again.
	f.detailErr = nil
	p.collectAll(t.Context())
	mustValue(t, reg, "artifacthub_collection_last_success_timestamp_seconds", map[string]string{"target": "repository:charts"}, float64(later.Unix()))
}

func TestFailedNewPackageIsLeftOut(t *testing.T) {
	p, f, reg := newFixture(t, RepositoryTarget{Name: "charts"})
	f.detailErr = map[string]error{"web": errors.New("boom")}
	p.collectAll(t.Context())

	if _, ok := find(t, reg, "artifacthub_package_stars", map[string]string{"package": "web"}); ok {
		t.Error("package without data exported")
	}
	mustValue(t, reg, "artifacthub_repository_info", map[string]string{"repository": "charts"}, 1)
	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "repository:charts"}, 1)
}

func TestPackageNotFoundIsSkipped(t *testing.T) {
	p, f, reg := newFixture(t, RepositoryTarget{Name: "charts"})
	p.collectAll(t.Context())

	// The hub dropped "web" since the search: the other package is still fetched.
	f.detailErr = map[string]error{"web": fmt.Errorf("package: %w", artifacthub.ErrNotFound)}
	f.packages[1].Stars = 9
	p.collectAll(t.Context())

	mustValue(t, reg, "artifacthub_package_stars", map[string]string{"package": "db"}, 9)
	if _, ok := find(t, reg, "artifacthub_package_stars", map[string]string{"package": "web"}); ok {
		t.Error("package the hub dropped is still exported")
	}
	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "repository:charts"}, 1)
}

func TestFailureIsPerTarget(t *testing.T) {
	p, _, reg := newFixture(t, RepositoryTarget{Name: "nope"}, HubTarget{})
	p.collectAll(t.Context())

	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "repository:nope"}, 1)
	mustValue(t, reg, "artifacthub_hub_packages", nil, 10)
	mustValue(t, reg, "artifacthub_hub_releases", nil, 20)
}

func TestErrorCountersStartAtZero(t *testing.T) {
	_, _, reg := newFixture(t, OrgTarget{Name: "acme"}, HubTarget{})
	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "hub"}, 0)
}

func TestUnknownKind(t *testing.T) {
	p, f, reg := newFixture(t, RepositoryTarget{Name: "charts"})
	f.packages[0].Repository.Kind = 999
	p.collectAll(t.Context())

	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "repository:charts"}, 0)
	pk := map[string]string{"kind": "kind-999", "package": "web"}
	mustValue(t, reg, "artifacthub_package_stars", pk, 7)
	mustValue(t, reg, "artifacthub_package_views_last_7d", pk, 8)
	if _, ok := find(t, reg, "artifacthub_package_versions", pk); ok {
		t.Error("versions exported for a package without details")
	}
}

func TestCancelledCollectionIsNotAnError(t *testing.T) {
	p, _, reg := newFixture(t, OrgTarget{Name: "acme"})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	p.collectAll(ctx)
	mustValue(t, reg, "artifacthub_collection_errors_total", map[string]string{"target": "org:acme"}, 0)
}

func TestMetricNames(t *testing.T) {
	p, f, reg := newFixture(t, OrgTarget{Name: "acme"}, HubTarget{})
	f.packages[0].SecurityReportSummary = &artifacthub.SecurityReportSummary{}
	p.collectAll(t.Context())

	families, err := reg.Gather()
	if err != nil {
		t.Fatal(err)
	}
	for _, mf := range families {
		if !strings.HasPrefix(mf.GetName(), "artifacthub_") {
			t.Errorf("metric %q lacks the artifacthub_ prefix", mf.GetName())
		}
	}
}
