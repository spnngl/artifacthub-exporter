package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
	artifacthub "github.com/spnngl/artifacthub-exporter"
)

// viewDays is the length of the window of views_last_7d.
const viewDays = 7

// api is the part of the Artifact Hub client the poller uses.
type api interface {
	SearchPackages(ctx context.Context, q artifacthub.PackageQuery) ([]artifacthub.PackageSummary, error)
	SearchRepositories(ctx context.Context, q artifacthub.RepositoryQuery) ([]artifacthub.Repository, error)
	GetPackageDetails(ctx context.Context, kind artifacthub.Kind, repository, name string) (artifacthub.PackageDetails, error)
	GetViews(ctx context.Context, packageID string) (artifacthub.Views, error)
	GetHubStats(ctx context.Context) (artifacthub.HubStats, error)
}

// selfMetrics describe the exporter's own work.
type selfMetrics struct {
	lastSuccess *prometheus.GaugeVec
	errors      *prometheus.CounterVec
	duration    *prometheus.GaugeVec
	requests    *prometheus.CounterVec
}

func newSelfMetrics(reg prometheus.Registerer) *selfMetrics {
	f := promauto.With(reg)
	return &selfMetrics{
		lastSuccess: f.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "collection_last_success_timestamp_seconds",
			Help: "Unix time of the target's last successful collection.",
		}, []string{"target"}),
		errors: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "collection_errors_total",
			Help: "Failed collections of the target.",
		}, []string{"target"}),
		duration: f.NewGaugeVec(prometheus.GaugeOpts{
			Namespace: namespace, Name: "collection_duration_seconds",
			Help: "Duration of the target's last collection.",
		}, []string{"target"}),
		requests: f.NewCounterVec(prometheus.CounterOpts{
			Namespace: namespace, Name: "api_requests_total",
			Help: "Requests to the Artifact Hub API, by route and status code (\"error\" without a response).",
		}, []string{"endpoint", "code"}),
	}
}

// poller collects the targets on an interval and stores the snapshots.
type poller struct {
	api      api
	targets  []Target
	store    *store
	metrics  *selfMetrics
	interval time.Duration
	now      func() time.Time

	warnedKinds map[artifacthub.Kind]struct{}
}

func newPoller(a api, targets []Target, s *store, m *selfMetrics, interval time.Duration) *poller {
	for _, t := range targets {
		m.errors.WithLabelValues(t.String())
	}
	return &poller{
		api: a, targets: targets, store: s, metrics: m, interval: interval,
		now:         time.Now,
		warnedKinds: make(map[artifacthub.Kind]struct{}),
	}
}

// run collects once at once, then on every tick, until ctx is done.
func (p *poller) run(ctx context.Context) {
	ticker := time.NewTicker(p.interval)
	defer ticker.Stop()
	for {
		p.collectAll(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// collectAll collects every target. A target that fails keeps its previous
// snapshot: its metrics go stale rather than vanish.
func (p *poller) collectAll(ctx context.Context) {
	c := newCycle(p.store.packages())
	for _, t := range p.targets {
		start := p.now()
		snap, err := p.collect(ctx, c, t)
		if ctx.Err() != nil {
			return
		}
		label := t.String()
		p.metrics.duration.WithLabelValues(label).Set(p.now().Sub(start).Seconds())
		// A snapshot comes with an error when the collection was partial.
		if snap != nil {
			p.store.set(t, *snap)
		}
		if err != nil {
			p.metrics.errors.WithLabelValues(label).Inc()
			log.Printf("collect %s: %v", label, err)
			continue
		}
		p.metrics.lastSuccess.WithLabelValues(label).Set(float64(p.now().Unix()))
	}
}

// cycle caches what several targets of one collection may ask for.
type cycle struct {
	repoPackages map[string][]artifacthub.PackageSummary
	repositories map[string]artifacthub.Repository
	packages     map[string]packageData
	// previous is the packages of the stored snapshots, by ID, to fall back on.
	previous map[string]packageData
	// failure is the error that stopped the fetching of packages, if any.
	failure error
}

func newCycle(previous map[string]packageData) *cycle {
	return &cycle{
		repoPackages: make(map[string][]artifacthub.PackageSummary),
		repositories: make(map[string]artifacthub.Repository),
		packages:     make(map[string]packageData),
		previous:     previous,
	}
}

// collect collects a target. On an error the snapshot is nil, or partial when
// some of the target could be collected.
func (p *poller) collect(ctx context.Context, c *cycle, t Target) (*snapshot, error) {
	switch t := t.(type) {
	case OrgTarget:
		return p.collectOwner(ctx, c,
			artifacthub.PackageQuery{Org: t.Name}, artifacthub.RepositoryQuery{Org: t.Name})
	case UserTarget:
		return p.collectOwner(ctx, c,
			artifacthub.PackageQuery{User: t.Alias}, artifacthub.RepositoryQuery{User: t.Alias})
	case RepositoryTarget:
		return p.collectRepository(ctx, c, t)
	case PackageTarget:
		return p.collectPackage(ctx, c, t)
	case HubTarget:
		stats, err := p.api.GetHubStats(ctx)
		if err != nil {
			return nil, err
		}
		return &snapshot{hub: &stats}, nil
	default:
		return nil, fmt.Errorf("unsupported target %T", t)
	}
}

// collectOwner collects the packages and repositories of an organization or user.
func (p *poller) collectOwner(ctx context.Context, c *cycle, pq artifacthub.PackageQuery, rq artifacthub.RepositoryQuery) (*snapshot, error) {
	// An unknown owner is a search with no result, not a 404: a typo would
	// otherwise collect nothing, silently.
	repos, err := p.api.SearchRepositories(ctx, rq)
	if err != nil {
		return nil, err
	}
	if len(repos) == 0 {
		return nil, fmt.Errorf("no repository of %s%s: %w", rq.Org, rq.User, artifacthub.ErrNotFound)
	}
	summaries, err := p.api.SearchPackages(ctx, pq)
	if err != nil {
		return nil, err
	}
	packages, err := p.packageData(ctx, c, summaries)
	return &snapshot{packages: packages, repositories: repositoriesData(repos, summaries)}, err
}

func (p *poller) collectRepository(ctx context.Context, c *cycle, t RepositoryTarget) (*snapshot, error) {
	repo, err := c.repository(ctx, p.api, t.Name)
	if err != nil {
		return nil, err
	}
	summaries, err := c.packagesOf(ctx, p.api, t.Name)
	if err != nil {
		return nil, err
	}
	packages, err := p.packageData(ctx, c, summaries)
	return &snapshot{packages: packages, repositories: repositoriesData([]artifacthub.Repository{repo}, summaries)}, err
}

// collectPackage finds the package in its repository's, as the package's own
// endpoint lacks its stars and whether it is official.
func (p *poller) collectPackage(ctx context.Context, c *cycle, t PackageTarget) (*snapshot, error) {
	repo, err := c.repository(ctx, p.api, t.Repository)
	if err != nil {
		return nil, err
	}
	summaries, err := c.packagesOf(ctx, p.api, t.Repository)
	if err != nil {
		return nil, err
	}
	for _, s := range summaries {
		if s.Name != t.Name || s.Repository.Kind != t.Kind {
			continue
		}
		packages, err := p.packageData(ctx, c, []artifacthub.PackageSummary{s})
		// Only one package of the repository is collected: no package count.
		return &snapshot{packages: packages, repositories: []repositoryData{{repo: repo}}}, err
	}
	return nil, fmt.Errorf("package %s/%s/%s: %w", t.Kind, t.Repository, t.Name, artifacthub.ErrNotFound)
}

// repositoriesData pairs the repositories with how many of the summaries each holds.
func repositoriesData(repos []artifacthub.Repository, summaries []artifacthub.PackageSummary) []repositoryData {
	counts := make(map[string]int)
	for _, s := range summaries {
		counts[s.Repository.ID]++
	}
	data := make([]repositoryData, 0, len(repos))
	for _, r := range repos {
		n := counts[r.ID]
		data = append(data, repositoryData{repo: r, packages: &n})
	}
	return data
}

// repository returns the repository of that name.
func (c *cycle) repository(ctx context.Context, a api, name string) (artifacthub.Repository, error) {
	if r, ok := c.repositories[name]; ok {
		return r, nil
	}
	repos, err := a.SearchRepositories(ctx, artifacthub.RepositoryQuery{Name: name})
	if err != nil {
		return artifacthub.Repository{}, err
	}
	if len(repos) == 0 {
		return artifacthub.Repository{}, fmt.Errorf("repository %s: %w", name, artifacthub.ErrNotFound)
	}
	c.repositories[name] = repos[0]
	return repos[0], nil
}

// packagesOf returns the packages of the repository of that name.
func (c *cycle) packagesOf(ctx context.Context, a api, repository string) ([]artifacthub.PackageSummary, error) {
	if s, ok := c.repoPackages[repository]; ok {
		return s, nil
	}
	s, err := a.SearchPackages(ctx, artifacthub.PackageQuery{Repository: repository})
	if err != nil {
		return nil, err
	}
	c.repoPackages[repository] = s
	return s, nil
}

// packageData fetches the details and views of each package once per cycle.
//
// One package's failed fetch leaves the others collected: a collection of
// hundreds of packages would otherwise never complete. The error is returned
// along with the data, which holds, for such a package:
//   - no data if the hub no longer knows it (a 404);
//   - its last collected details and views, with fresh search fields, on any
//     other error. The hub is then likely rate limiting or down, so the
//     cycle stops fetching, and the later packages are served the same way.
//
// A package never collected before is left out in both cases.
func (p *poller) packageData(ctx context.Context, c *cycle, summaries []artifacthub.PackageSummary) ([]packageData, error) {
	var failure error
	data := make([]packageData, 0, len(summaries))
	for _, s := range summaries {
		if d, ok := c.packages[s.ID]; ok {
			data = append(data, d)
			continue
		}
		if c.failure == nil {
			d, err := p.fetchPackage(ctx, s)
			switch {
			case err == nil:
				c.packages[s.ID] = d
				data = append(data, d)
				continue
			case ctx.Err() != nil:
				return data, err
			case errors.Is(err, artifacthub.ErrNotFound):
				failure = errors.Join(failure, err)
				continue
			}
			c.failure = err
		}
		failure = c.failure
		if d, ok := c.previous[s.ID]; ok {
			d.summary = s
			data = append(data, d)
		}
	}
	return data, failure
}

// fetchPackage fetches the details and views of a package.
func (p *poller) fetchPackage(ctx context.Context, s artifacthub.PackageSummary) (packageData, error) {
	d := packageData{summary: s}
	details, err := p.api.GetPackageDetails(ctx, s.Repository.Kind, s.Repository.Name, s.Name)
	switch {
	case errors.Is(err, artifacthub.ErrUnknownKind):
		p.warnKind(s.Repository.Kind)
	case err != nil:
		return packageData{}, fmt.Errorf("package %s/%s: %w", s.Repository.Name, s.Name, err)
	default:
		d.details = &details
	}
	views, err := p.api.GetViews(ctx, s.ID)
	if err != nil {
		return packageData{}, fmt.Errorf("package %s/%s views: %w", s.Repository.Name, s.Name, err)
	}
	d.views = countViews(views, p.now())
	return d, nil
}

func (p *poller) warnKind(k artifacthub.Kind) {
	if _, ok := p.warnedKinds[k]; ok {
		return
	}
	p.warnedKinds[k] = struct{}{}
	log.Printf("kind %d is unknown to this version: the versions, subscriptions and webhooks of its packages are not collected", int(k))
}

// countViews sums the views of yesterday and of the 7 complete UTC days before
// today. Today's views are partial: counting them would make the window
// jump around.
func countViews(v artifacthub.Views, now time.Time) viewCounts {
	daily := v.Daily()
	today := now.UTC()
	var counts viewCounts
	for i := 1; i <= viewDays; i++ {
		n := daily[today.AddDate(0, 0, -i).Format(time.DateOnly)]
		if i == 1 {
			counts.yesterday = n
		}
		counts.last7d += n
	}
	return counts
}
