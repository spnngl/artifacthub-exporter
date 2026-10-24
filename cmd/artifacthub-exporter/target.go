package main

import (
	"fmt"
	"strings"

	artifacthub "github.com/spnngl/artifacthub-exporter"
)

// Target is what the exporter collects metrics about. Its String is the
// `target` label of the collection metrics.
type Target interface {
	fmt.Stringer
	isTarget()
}

// OrgTarget is the packages and repositories of an organization.
type OrgTarget struct{ Name string }

// UserTarget is the packages and repositories of a user.
type UserTarget struct{ Alias string }

// RepositoryTarget is a repository and its packages.
type RepositoryTarget struct{ Name string }

// PackageTarget is one package, and its repository.
type PackageTarget struct {
	Kind       artifacthub.Kind
	Repository string
	Name       string
}

// HubTarget is the hub's totals.
type HubTarget struct{}

func (OrgTarget) isTarget()        {}
func (UserTarget) isTarget()       {}
func (RepositoryTarget) isTarget() {}
func (PackageTarget) isTarget()    {}
func (HubTarget) isTarget()        {}

func (t OrgTarget) String() string        { return "org:" + t.Name }
func (t UserTarget) String() string       { return "user:" + t.Alias }
func (t RepositoryTarget) String() string { return "repository:" + t.Name }
func (HubTarget) String() string          { return "hub" }
func (t PackageTarget) String() string {
	return fmt.Sprintf("package:%s/%s/%s", t.Kind, t.Repository, t.Name)
}

// parsePackageTarget parses `kind/repository/name`, e.g. `helm/bitnami/redis`.
func parsePackageTarget(s string) (PackageTarget, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
		return PackageTarget{}, fmt.Errorf("package %q: want kind/repository/name, e.g. helm/bitnami/redis", s)
	}
	kind, ok := artifacthub.ParseKind(parts[0])
	if !ok {
		return PackageTarget{}, fmt.Errorf("package %q: unknown kind %q", s, parts[0])
	}
	return PackageTarget{Kind: kind, Repository: parts[1], Name: parts[2]}, nil
}
