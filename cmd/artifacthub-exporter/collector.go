package main

import (
	"strconv"
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	artifacthub "github.com/spnngl/artifacthub-exporter"
)

const namespace = "artifacthub"

// snapshot is what one collection of a target found. It is read-only once
// stored: scrapes read it while the next collection builds another.
type snapshot struct {
	packages     []packageData
	repositories []repositoryData
	hub          *artifacthub.HubStats
}

type packageData struct {
	summary artifacthub.PackageSummary
	// details is nil for a package of a kind the client doesn't know.
	details *artifacthub.PackageDetails
	views   viewCounts
}

// viewCounts are the views of the last complete UTC days; today's are partial.
type viewCounts struct {
	yesterday int
	last7d    int
}

type repositoryData struct {
	repo artifacthub.Repository
	// packages is how many packages were collected in the repository, nil when
	// the target doesn't list all of them.
	packages *int
}

// store holds the latest successful snapshot of each target.
type store struct {
	mu        sync.RWMutex
	order     []string
	snapshots map[string]snapshot
}

func newStore(targets []Target) *store {
	s := &store{snapshots: make(map[string]snapshot)}
	for _, t := range targets {
		s.order = append(s.order, t.String())
	}
	return s
}

func (s *store) set(t Target, snap snapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.snapshots[t.String()] = snap
}

// packages returns the packages of every snapshot, by ID.
func (s *store) packages() map[string]packageData {
	packages := make(map[string]packageData)
	for _, snap := range s.all() {
		for _, p := range snap.packages {
			packages[p.summary.ID] = p
		}
	}
	return packages
}

// all returns the snapshots in the order of the targets.
func (s *store) all() []snapshot {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var all []snapshot
	for _, key := range s.order {
		if snap, ok := s.snapshots[key]; ok {
			all = append(all, snap)
		}
	}
	return all
}

// collector exposes the stored snapshots as Prometheus metrics. It reads the
// store only, so a scrape is cheap whatever the targets' size.
type collector struct {
	store *store
	descs []*prometheus.Desc

	packageInfo            *prometheus.Desc
	packageStars           *prometheus.Desc
	packageLastRelease     *prometheus.Desc
	packageDeprecated      *prometheus.Desc
	packageSigned          *prometheus.Desc
	packageOfficial        *prometheus.Desc
	packageProductionOrgs  *prometheus.Desc
	packageVulnerabilities *prometheus.Desc
	packageVersions        *prometheus.Desc
	packageSubscriptions   *prometheus.Desc
	packageWebhooks        *prometheus.Desc
	packageViews7d         *prometheus.Desc
	packageViewsYesterday  *prometheus.Desc

	repositoryInfo         *prometheus.Desc
	repositoryLastTracking *prometheus.Desc
	repositoryTrackingErr  *prometheus.Desc
	repositoryLastScanning *prometheus.Desc
	repositoryScanningErr  *prometheus.Desc
	repositoryDisabled     *prometheus.Desc
	repositoryPackages     *prometheus.Desc

	hubPackages *prometheus.Desc
	hubReleases *prometheus.Desc
}

func newCollector(s *store) *collector {
	c := &collector{store: s}
	pkg := []string{"kind", "repository", "package"}
	repo := []string{"kind", "repository"}

	c.packageInfo = c.desc("package_info", "Always 1; the package's version and identity.",
		append(pkg, "package_id", "publisher", "version", "app_version", "license")...)
	c.packageStars = c.desc("package_stars", "Stars of the package.", pkg...)
	c.packageLastRelease = c.desc("package_last_release_timestamp_seconds", "Unix time of the package's latest release.", pkg...)
	c.packageDeprecated = c.desc("package_deprecated", "1 if the package is deprecated.", pkg...)
	c.packageSigned = c.desc("package_signed", "1 if the package is signed.", pkg...)
	c.packageOfficial = c.desc("package_official", "1 if the package is official.", pkg...)
	c.packageProductionOrgs = c.desc("package_production_organizations", "Organizations using the package in production.", pkg...)
	c.packageVulnerabilities = c.desc("package_vulnerabilities", "Vulnerabilities in the package's security report; absent without a report.",
		append(pkg, "severity")...)
	c.packageVersions = c.desc("package_versions", "Versions of the package.", pkg...)
	c.packageSubscriptions = c.desc("package_subscriptions", "Subscriptions to the package.", pkg...)
	c.packageWebhooks = c.desc("package_webhooks", "Webhooks on the package.", pkg...)
	c.packageViews7d = c.desc("package_views_last_7d", "Views of the package in the last 7 complete UTC days.", pkg...)
	c.packageViewsYesterday = c.desc("package_views_yesterday", "Views of the package in the last complete UTC day.", pkg...)

	c.repositoryInfo = c.desc("repository_info", "Always 1; the repository's identity.",
		append(repo, "repository_id", "publisher", "url", "verified_publisher", "official")...)
	c.repositoryLastTracking = c.desc("repository_last_tracking_timestamp_seconds", "Unix time of the repository's last tracking.", repo...)
	c.repositoryTrackingErr = c.desc("repository_last_tracking_errors", "1 if the repository's last tracking had errors.", repo...)
	c.repositoryLastScanning = c.desc("repository_last_scanning_timestamp_seconds", "Unix time of the repository's last security scan.", repo...)
	c.repositoryScanningErr = c.desc("repository_last_scanning_errors", "1 if the repository's last security scan had errors.", repo...)
	c.repositoryDisabled = c.desc("repository_disabled", "1 if the repository is disabled.", repo...)
	c.repositoryPackages = c.desc("repository_packages", "Packages collected in the repository; absent when only some of them are collected.", repo...)

	c.hubPackages = c.desc("hub_packages", "Packages in the hub.")
	c.hubReleases = c.desc("hub_releases", "Releases in the hub.")
	return c
}

func (c *collector) desc(name, help string, labels ...string) *prometheus.Desc {
	d := prometheus.NewDesc(prometheus.BuildFQName(namespace, "", name), help, labels, nil)
	c.descs = append(c.descs, d)
	return d
}

// Describe sends every descriptor Collect uses, or /metrics fails.
func (c *collector) Describe(ch chan<- *prometheus.Desc) {
	for _, d := range c.descs {
		ch <- d
	}
}

// Collect sends the metrics of the stored snapshots. Targets can overlap: a
// package or a repository found by several is sent once.
func (c *collector) Collect(ch chan<- prometheus.Metric) {
	seenPackages := make(map[string]struct{})
	repositories := make(map[string]repositoryData)
	var repositoryOrder []string
	var hub *artifacthub.HubStats

	for _, snap := range c.store.all() {
		for _, p := range snap.packages {
			if _, dup := seenPackages[p.summary.ID]; dup {
				continue
			}
			seenPackages[p.summary.ID] = struct{}{}
			c.collectPackage(ch, p)
		}
		for _, r := range snap.repositories {
			known, dup := repositories[r.repo.ID]
			if !dup {
				repositoryOrder = append(repositoryOrder, r.repo.ID)
			}
			// A target listing the whole repository knows its package count.
			if !dup || (known.packages == nil && r.packages != nil) {
				repositories[r.repo.ID] = r
			}
		}
		if snap.hub != nil {
			hub = snap.hub
		}
	}

	for _, id := range repositoryOrder {
		c.collectRepository(ch, repositories[id])
	}
	if hub != nil {
		gauge(ch, c.hubPackages, float64(hub.Packages))
		gauge(ch, c.hubReleases, float64(hub.Releases))
	}
}

func (c *collector) collectPackage(ch chan<- prometheus.Metric, p packageData) {
	s := p.summary
	labels := []string{s.Repository.Kind.String(), s.Repository.Name, s.Name}

	gauge(ch, c.packageInfo, 1, append(labels, s.ID, s.Repository.Publisher(), s.Version, s.AppVersion, s.License)...)
	gauge(ch, c.packageStars, float64(s.Stars), labels...)
	gauge(ch, c.packageLastRelease, float64(s.TS), labels...)
	gauge(ch, c.packageDeprecated, boolToFloat(s.Deprecated), labels...)
	gauge(ch, c.packageSigned, boolToFloat(s.Signed), labels...)
	gauge(ch, c.packageOfficial, boolToFloat(s.Official), labels...)
	gauge(ch, c.packageProductionOrgs, float64(s.ProductionOrganizationsCount), labels...)
	if r := s.SecurityReportSummary; r != nil {
		for severity, n := range map[string]int{
			"critical": r.Critical, "high": r.High, "medium": r.Medium, "low": r.Low, "unknown": r.Unknown,
		} {
			gauge(ch, c.packageVulnerabilities, float64(n), append(labels[:3:3], severity)...)
		}
	}
	if d := p.details; d != nil {
		gauge(ch, c.packageVersions, float64(len(d.AvailableVersions)), labels...)
		gauge(ch, c.packageSubscriptions, float64(d.Stats.Subscriptions), labels...)
		gauge(ch, c.packageWebhooks, float64(d.Stats.Webhooks), labels...)
	}
	gauge(ch, c.packageViews7d, float64(p.views.last7d), labels...)
	gauge(ch, c.packageViewsYesterday, float64(p.views.yesterday), labels...)
}

func (c *collector) collectRepository(ch chan<- prometheus.Metric, r repositoryData) {
	repo := r.repo
	labels := []string{repo.Kind.String(), repo.Name}

	gauge(ch, c.repositoryInfo, 1, append(labels, repo.ID, repo.Publisher(), repo.URL,
		strconv.FormatBool(repo.VerifiedPublisher), strconv.FormatBool(repo.Official))...)
	gauge(ch, c.repositoryLastTracking, float64(repo.LastTrackingTS), labels...)
	gauge(ch, c.repositoryTrackingErr, boolToFloat(repo.LastTrackingErrors != ""), labels...)
	gauge(ch, c.repositoryLastScanning, float64(repo.LastScanningTS), labels...)
	gauge(ch, c.repositoryScanningErr, boolToFloat(repo.LastScanningErrors != ""), labels...)
	gauge(ch, c.repositoryDisabled, boolToFloat(repo.Disabled), labels...)
	if r.packages != nil {
		gauge(ch, c.repositoryPackages, float64(*r.packages), labels...)
	}
}

func gauge(ch chan<- prometheus.Metric, d *prometheus.Desc, v float64, labels ...string) {
	ch <- prometheus.MustNewConstMetric(d, prometheus.GaugeValue, v, labels...)
}

func boolToFloat(b bool) float64 {
	if b {
		return 1
	}
	return 0
}
