// Package scan holds the cluster-independent half of the tool: the snapshot
// format, the finalizer owner resolver, and the reporters. Everything here runs
// without a cluster so it can be tested against fixtures.
package scan

// Object is a single API object that is stuck: it carries a deletionTimestamp
// and still has at least one finalizer.
type Object struct {
	APIVersion string `json:"apiVersion"`
	Kind       string `json:"kind"`
	// Resource is the plural lowercase name kubectl expects. Live collection
	// fills it from discovery; fixtures may leave it empty and it is derived.
	Resource          string   `json:"resource,omitempty"`
	Namespace         string   `json:"namespace,omitempty"`
	Name              string   `json:"name"`
	DeletionTimestamp string   `json:"deletionTimestamp,omitempty"`
	Finalizers        []string `json:"finalizers"`
}

// CRD is the part of a CustomResourceDefinition the resolver cares about.
type CRD struct {
	Name  string `json:"name"`
	Group string `json:"group"`
}

// APIService describes an aggregated API and whether the API server currently
// considers it available.
type APIService struct {
	Name      string `json:"name"`
	Group     string `json:"group"`
	Available bool   `json:"available"`
}

// Deployment is a candidate controller workload.
type Deployment struct {
	Namespace     string `json:"namespace"`
	Name          string `json:"name"`
	Replicas      int32  `json:"replicas"`
	ReadyReplicas int32  `json:"readyReplicas"`
}

// Snapshot is everything the resolver needs. It is the on-disk fixture format
// and what --dump writes out of a live cluster.
type Snapshot struct {
	Objects     []Object     `json:"objects"`
	CRDs        []CRD        `json:"crds,omitempty"`
	APIServices []APIService `json:"apiServices,omitempty"`
	Deployments []Deployment `json:"deployments,omitempty"`
}

// Verdict is the answer to "can I strip this finalizer".
type Verdict string

const (
	// OwnerMissing means nothing in the cluster claims the finalizer's domain:
	// no CRD, no APIService, no plausible controller. Stripping is safe.
	OwnerMissing Verdict = "owner-missing"
	// OwnerUnhealthy means the owner exists but cannot run, so it will never
	// clear the finalizer. Fix the controller instead of stripping.
	OwnerUnhealthy Verdict = "owner-unhealthy"
	// OwnerHealthy means the owner is up and is presumably still working.
	OwnerHealthy Verdict = "owner-healthy"
	// OwnerBuiltin covers core finalizers handled by kube-controller-manager or
	// the API server itself. These clear on their own once the real blocker
	// (a mounted PVC, a dependent object) goes away.
	OwnerBuiltin Verdict = "owner-builtin"
)

// Action is the one-word recommendation shown in the table.
func (v Verdict) Action() string {
	switch v {
	case OwnerMissing:
		return "strip"
	case OwnerUnhealthy:
		return "fix-controller"
	case OwnerBuiltin:
		return "wait-on-dependent"
	default:
		return "wait"
	}
}

// Blocker is one finalizer on one object, resolved.
type Blocker struct {
	Finalizer string  `json:"finalizer"`
	Verdict   Verdict `json:"verdict"`
	// Owner is a human label for whoever is supposed to remove the finalizer,
	// e.g. "CRD applications.argoproj.io" or "Deployment argocd/argocd-controller".
	Owner string `json:"owner"`
	// Reason explains the verdict in one sentence.
	Reason string `json:"reason"`
	// Warning is set for finalizers known to guard a real cloud resource, where
	// stripping leaks something that costs money.
	Warning string `json:"warning,omitempty"`
	// Remediation is the exact command to run, and is only set for OwnerMissing.
	Remediation string `json:"remediation,omitempty"`
}

// Finding groups all blockers on a single stuck object.
type Finding struct {
	Kind              string    `json:"kind"`
	Namespace         string    `json:"namespace,omitempty"`
	Name              string    `json:"name"`
	DeletionTimestamp string    `json:"deletionTimestamp,omitempty"`
	Blockers          []Blocker `json:"blockers"`
}

// Report is the whole result, and what --output json prints.
type Report struct {
	Findings []Finding `json:"findings"`
	Scanned  int       `json:"scanned"`
}

// Stuck reports whether anything at all is blocked, which drives the exit code.
func (r Report) Stuck() bool { return len(r.Findings) > 0 }
