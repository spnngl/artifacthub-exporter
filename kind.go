package artifacthub

import "strconv"

// Kind is the kind of a repository (and of its packages), as the API numbers it.
type Kind int

// kindSlugs holds the URL slug of each kind, indexed by the API's number
// (artifacthub/hub internal/hub/repo.go, GetKindName).
var kindSlugs = [...]string{
	0:  "helm",
	1:  "falco",
	2:  "opa",
	3:  "olm",
	4:  "tbaction",
	5:  "krew",
	6:  "helm-plugin",
	7:  "tekton-task",
	8:  "keda-scaler",
	9:  "coredns",
	10: "keptn",
	11: "tekton-pipeline",
	12: "container",
	13: "kubewarden",
	14: "gatekeeper",
	15: "kyverno",
	16: "knative-client-plugin",
	17: "backstage",
	18: "argo-template",
	19: "kubearmor",
	20: "kcl",
	21: "headlamp",
	22: "inspektor-gadget",
	23: "tekton-stepaction",
	24: "meshery",
	25: "opencost",
	26: "radius",
	27: "bootc",
	28: "kagent",
}

// Slug returns the kind's URL slug. It reports false for a kind this client
// doesn't know, one the API added after it was written.
func (k Kind) Slug() (string, bool) {
	if k < 0 || int(k) >= len(kindSlugs) {
		return "", false
	}
	return kindSlugs[k], true
}

// String returns the slug, or `kind-<n>` for an unknown kind.
func (k Kind) String() string {
	if slug, ok := k.Slug(); ok {
		return slug
	}
	return "kind-" + strconv.Itoa(int(k))
}

// ParseKind returns the kind with the given slug.
func ParseKind(slug string) (Kind, bool) {
	for i, s := range kindSlugs {
		if s == slug {
			return Kind(i), true
		}
	}
	return 0, false
}
