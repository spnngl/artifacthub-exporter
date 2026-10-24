package main

import (
	"reflect"
	"strings"
	"testing"
	"time"

	artifacthub "github.com/spnngl/artifacthub-exporter"
)

func TestParsePackageTarget(t *testing.T) {
	got, err := parsePackageTarget("helm/bitnami/redis")
	if err != nil {
		t.Fatal(err)
	}
	if want := (PackageTarget{Kind: 0, Repository: "bitnami", Name: "redis"}); got != want {
		t.Errorf("got %+v, want %+v", got, want)
	}
	if got.String() != "package:helm/bitnami/redis" {
		t.Errorf("String() = %q", got.String())
	}

	for _, bad := range []string{"", "redis", "helm/redis", "helm/bitnami/redis/x", "helm//redis", "nope/bitnami/redis"} {
		if _, err := parsePackageTarget(bad); err == nil {
			t.Errorf("parsePackageTarget(%q) succeeded", bad)
		}
	}
}

func TestTargets(t *testing.T) {
	c := defaultConfig()
	c.Orgs = []string{"a", " b ", "a", ""}
	c.Users = []string{"u"}
	c.Repositories = []string{"r"}
	c.Packages = []string{"helm/r/p"}
	c.HubStats = true

	got, err := c.targets()
	if err != nil {
		t.Fatal(err)
	}
	var labels []string
	for _, target := range got {
		labels = append(labels, target.String())
	}
	want := []string{"org:a", "org:b", "user:u", "repository:r", "package:helm/r/p", "hub"}
	if !reflect.DeepEqual(labels, want) {
		t.Errorf("got %v, want %v", labels, want)
	}
}

func TestTargetsErrors(t *testing.T) {
	tests := map[string]struct {
		edit func(*Config)
		want string
	}{
		"nothing":         {func(*Config) {}, "nothing to collect"},
		"interval":        {func(c *Config) { c.Orgs = []string{"a"}; c.CollectInterval = time.Minute }, "collect interval"},
		"root path":       {func(c *Config) { c.Orgs = []string{"a"}; c.WebPath = "/" }, "web path"},
		"relative path":   {func(c *Config) { c.Orgs = []string{"a"}; c.WebPath = "metrics" }, "web path"},
		"bad package":     {func(c *Config) { c.Packages = []string{"redis"} }, "kind/repository/name"},
		"blank only list": {func(c *Config) { c.Orgs = []string{" "} }, "nothing to collect"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			c := defaultConfig()
			tc.edit(&c)
			_, err := c.targets()
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
}

func TestTargetKinds(t *testing.T) {
	// Every Target is a distinct label.
	targets := []Target{OrgTarget{"x"}, UserTarget{"x"}, RepositoryTarget{"x"}, PackageTarget{Kind: artifacthub.Kind(0), Repository: "x", Name: "x"}, HubTarget{}}
	seen := make(map[string]bool)
	for _, target := range targets {
		if seen[target.String()] {
			t.Errorf("duplicate label %q", target)
		}
		seen[target.String()] = true
	}
}
