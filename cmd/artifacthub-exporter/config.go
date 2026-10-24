package main

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// minInterval is the shortest collection interval: the API caches its answers
// for 5 minutes, so polling faster only costs requests.
const minInterval = 5 * time.Minute

// Config gets its content from env (or flags) and passes it on to different packages.
type Config struct {
	WebAddr         string        `arg:"--web-addr,env:WEB_ADDR" help:"address to listen on"`
	WebPath         string        `arg:"--web-path,env:WEB_PATH" help:"path of the metrics"`
	CollectInterval time.Duration `arg:"--collect-interval,env:COLLECT_INTERVAL" help:"time between two collections, at least 5m"`

	URL          string `arg:"--artifacthub-url,env:ARTIFACTHUB_URL" help:"address of the Artifact Hub"`
	APIKeyID     string `arg:"--artifacthub-api-key-id,env:ARTIFACTHUB_API_KEY_ID" help:"API key id, with the secret"`
	APIKeySecret string `arg:"--artifacthub-api-key-secret,env:ARTIFACTHUB_API_KEY_SECRET" help:"API key secret, with the id"`

	Orgs         []string `arg:"--artifacthub-orgs,env:ARTIFACTHUB_ORGS" help:"organizations to collect"`
	Users        []string `arg:"--artifacthub-users,env:ARTIFACTHUB_USERS" help:"users (aliases) to collect"`
	Repositories []string `arg:"--artifacthub-repositories,env:ARTIFACTHUB_REPOSITORIES" help:"repositories to collect"`
	Packages     []string `arg:"--artifacthub-packages,env:ARTIFACTHUB_PACKAGES" help:"packages to collect, as kind/repository/name"`
	HubStats     bool     `arg:"--artifacthub-hub-stats,env:ARTIFACTHUB_HUB_STATS" help:"collect the hub's totals"`
}

// Version is what --version prints (go-arg's Versioned).
func (Config) Version() string {
	return "artifacthub-exporter " + version
}

// defaultConfig is the Config before the environment and the flags.
func defaultConfig() Config {
	return Config{
		WebAddr:         ":8080",
		WebPath:         "/metrics",
		CollectInterval: 15 * time.Minute,
		URL:             "https://artifacthub.io",
	}
}

// targets validates the configuration and returns what to collect: the
// organizations, then the users, repositories and packages, in the order
// given, each once, and the hub when asked to.
func (c Config) targets() ([]Target, error) {
	if !strings.HasPrefix(c.WebPath, "/") || c.WebPath == "/" {
		return nil, fmt.Errorf("web path %q: want a path other than /, starting with /", c.WebPath)
	}
	if c.CollectInterval < minInterval {
		return nil, fmt.Errorf("collect interval %s: want at least %s", c.CollectInterval, minInterval)
	}

	var targets []Target
	seen := make(map[string]struct{})
	add := func(t Target) {
		if _, dup := seen[t.String()]; !dup {
			seen[t.String()] = struct{}{}
			targets = append(targets, t)
		}
	}
	for _, name := range clean(c.Orgs) {
		add(OrgTarget{Name: name})
	}
	for _, alias := range clean(c.Users) {
		add(UserTarget{Alias: alias})
	}
	for _, name := range clean(c.Repositories) {
		add(RepositoryTarget{Name: name})
	}
	for _, s := range clean(c.Packages) {
		t, err := parsePackageTarget(s)
		if err != nil {
			return nil, err
		}
		add(t)
	}
	if c.HubStats {
		add(HubTarget{})
	}
	if len(targets) == 0 {
		return nil, errors.New("nothing to collect: set ARTIFACTHUB_ORGS, ARTIFACTHUB_USERS, ARTIFACTHUB_REPOSITORIES, ARTIFACTHUB_PACKAGES or ARTIFACTHUB_HUB_STATS")
	}
	return targets, nil
}

// clean trims the values and drops the empty ones.
func clean(values []string) []string {
	var out []string
	for _, v := range values {
		if v = strings.TrimSpace(v); v != "" {
			out = append(out, v)
		}
	}
	return out
}
