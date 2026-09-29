package scan

import (
	"fmt"
	"sort"
	"strings"
)

// builtinFinalizers are handled by kube-controller-manager or the API server.
// They are never "owner-missing": the owner is the control plane itself, so the
// answer is always to remove whatever dependent object is holding them.
var builtinFinalizers = map[string]string{
	"kubernetes":                   "the namespace controller, waiting on remaining namespaced content",
	"foregroundDeletion":           "the garbage collector, waiting on dependents",
	"orphan":                       "the garbage collector, orphaning dependents",
	"kubernetes.io/pv-protection":  "the PV protection controller, waiting for the volume to be unbound",
	"kubernetes.io/pvc-protection": "the PVC protection controller, waiting for pods using the claim to go away",
	"service.kubernetes.io/load-balancer-cleanup":   "the cloud provider's service controller, waiting on the load balancer",
	"snapshot.storage.kubernetes.io/vsc-protection": "the snapshot controller, waiting on the volume snapshot",
}

// cloudWarnings flags finalizers that guard a resource living outside the
// cluster. Stripping these does unblock the delete, and then quietly leaves you
// paying for a load balancer nobody can find.
var cloudWarnings = map[string]string{
	"service.k8s.aws/resources":                   "may leak an AWS load balancer / target group",
	"elbv2.k8s.aws/group":                         "may leak an AWS ALB and its listeners",
	"service.kubernetes.io/load-balancer-cleanup": "may leak a cloud load balancer and its public IP",
	"kubernetes.io/pv-protection":                 "may leak the backing disk if the PV is Retain",
	"external-attacher/":                          "may leave a CSI volume attached to a dead node",
	"finalizer.ec2.aws.crossplane.io":             "may leak the EC2 resource managed by Crossplane",
}

// genericLabels are DNS labels that identify nobody. A finalizer domain reduced
// to only these carries no signal about which workload owns it.
var genericLabels = map[string]bool{
	"io": true, "com": true, "net": true, "org": true, "dev": true, "sh": true,
	"k8s": true, "kubernetes": true, "x-k8s": true, "cloud": true, "app": true,
	"apis": true, "api": true, "co": true, "cn": true, "internal": true,
}

// domain returns the DNS-ish part of a finalizer that identifies its owner.
//
// Two shapes are in the wild and both are legal. "service.k8s.aws/resources"
// puts the domain before the slash; cert-manager and Argo CD instead ship a
// single qualified name with no slash at all ("finalizer.cert-manager.io").
// Treating only the first shape as having a domain is what makes a naive
// resolver report every cert-manager object as unresolvable.
func domain(finalizer string) string {
	if i := strings.Index(finalizer, "/"); i >= 0 {
		return finalizer[:i]
	}
	if strings.Contains(finalizer, ".") {
		return finalizer
	}
	return ""
}

// domainSuffixes yields the finalizer domain and every suffix of it, longest
// first: "resources-finalizer.argocd.argoproj.io" also tries "argocd.argoproj.io"
// and "argoproj.io". Controllers routinely prefix their API group with a label,
// so an exact group match alone misses most real finalizers.
func domainSuffixes(d string) []string {
	if d == "" {
		return nil
	}
	parts := strings.Split(d, ".")
	out := make([]string, 0, len(parts))
	for i := range parts {
		out = append(out, strings.Join(parts[i:], "."))
	}
	return out
}

// identityTokens are the labels of a domain worth searching workload names for.
func identityTokens(d string) []string {
	var out []string
	for _, p := range strings.Split(d, ".") {
		for _, tok := range strings.Split(p, "-") {
			if len(tok) >= 3 && !genericLabels[tok] {
				out = append(out, tok)
			}
		}
	}
	// Longest first: "cert-manager" should beat "cert".
	sort.SliceStable(out, func(i, j int) bool { return len(out[i]) > len(out[j]) })
	return out
}

type resolver struct {
	crdsByGroup map[string][]CRD
	apisByGroup map[string][]APIService
	deployments []Deployment
}

func newResolver(s Snapshot) *resolver {
	r := &resolver{
		crdsByGroup: map[string][]CRD{},
		apisByGroup: map[string][]APIService{},
		deployments: s.Deployments,
	}
	for _, c := range s.CRDs {
		r.crdsByGroup[c.Group] = append(r.crdsByGroup[c.Group], c)
	}
	for _, a := range s.APIServices {
		r.apisByGroup[a.Group] = append(r.apisByGroup[a.Group], a)
	}
	return r
}

// findDeployment returns the workload most likely to be the controller behind a
// finalizer domain, matching identity tokens against deployment and namespace
// names. It is a heuristic, which is why the tool never strips anything itself.
func (r *resolver) findDeployment(d string) (Deployment, bool) {
	for _, tok := range identityTokens(d) {
		for _, dep := range r.deployments {
			if strings.Contains(dep.Name, tok) || strings.Contains(dep.Namespace, tok) {
				return dep, true
			}
		}
	}
	return Deployment{}, false
}

func (r *resolver) classify(obj Object, finalizer string) Blocker {
	b := Blocker{Finalizer: finalizer, Warning: warningFor(finalizer)}

	if owner, ok := builtinFinalizers[finalizer]; ok {
		b.Verdict = OwnerBuiltin
		b.Owner = "control plane"
		b.Reason = "built-in finalizer held by " + owner
		return b
	}

	dom := domain(finalizer)
	if dom == "" {
		// A dotless, non-core finalizer: nothing to trace, so we cannot claim
		// the owner is gone. Say so instead of guessing.
		b.Verdict = OwnerHealthy
		b.Owner = "unknown"
		b.Reason = "finalizer has no domain, owner cannot be resolved automatically"
		return b
	}

	var apiOwner string
	for _, suffix := range domainSuffixes(dom) {
		if crds := r.crdsByGroup[suffix]; len(crds) > 0 {
			apiOwner = "CRD " + crds[0].Name
			break
		}
		if apis := r.apisByGroup[suffix]; len(apis) > 0 {
			if !apis[0].Available {
				b.Verdict = OwnerUnhealthy
				b.Owner = "APIService " + apis[0].Name
				b.Reason = "the aggregated API serving this finalizer is not available"
				return b
			}
			apiOwner = "APIService " + apis[0].Name
			break
		}
	}

	dep, hasDep := r.findDeployment(dom)
	switch {
	case hasDep && dep.ReadyReplicas == 0:
		b.Verdict = OwnerUnhealthy
		b.Owner = fmt.Sprintf("Deployment %s/%s", dep.Namespace, dep.Name)
		b.Reason = fmt.Sprintf("controller has 0/%d ready replicas and will never clear this finalizer", dep.Replicas)
	case hasDep:
		b.Verdict = OwnerHealthy
		b.Owner = fmt.Sprintf("Deployment %s/%s", dep.Namespace, dep.Name)
		b.Reason = fmt.Sprintf("controller is up (%d/%d ready); it is still working or waiting on an external resource", dep.ReadyReplicas, dep.Replicas)
	case apiOwner != "":
		b.Verdict = OwnerHealthy
		b.Owner = apiOwner
		b.Reason = "the API that owns this finalizer is installed but no controller Deployment was matched"
	default:
		b.Verdict = OwnerMissing
		b.Owner = "none"
		b.Reason = fmt.Sprintf("no CRD, APIService or Deployment matches %q; whatever set this finalizer is gone", dom)
		b.Remediation = RemediationCommand(obj, finalizer)
	}
	return b
}

func warningFor(finalizer string) string {
	if w, ok := cloudWarnings[finalizer]; ok {
		return w
	}
	for prefix, w := range cloudWarnings {
		if strings.HasSuffix(prefix, "/") && strings.HasPrefix(finalizer, prefix) {
			return w
		}
	}
	return ""
}

// Resolve classifies every finalizer on every stuck object in the snapshot.
func Resolve(s Snapshot) Report {
	r := newResolver(s)
	rep := Report{Scanned: len(s.Objects), Findings: []Finding{}}
	for _, obj := range s.Objects {
		if len(obj.Finalizers) == 0 {
			continue
		}
		f := Finding{
			Kind:              obj.Kind,
			Namespace:         obj.Namespace,
			Name:              obj.Name,
			DeletionTimestamp: obj.DeletionTimestamp,
		}
		for _, fin := range obj.Finalizers {
			f.Blockers = append(f.Blockers, r.classify(obj, fin))
		}
		rep.Findings = append(rep.Findings, f)
	}
	sort.SliceStable(rep.Findings, func(i, j int) bool {
		a, b := rep.Findings[i], rep.Findings[j]
		if a.Namespace != b.Namespace {
			return a.Namespace < b.Namespace
		}
		if a.Kind != b.Kind {
			return a.Kind < b.Kind
		}
		return a.Name < b.Name
	})
	return rep
}
