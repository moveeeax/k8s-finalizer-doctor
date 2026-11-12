package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeOwners(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "owners.yaml")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func stuck(apiVersion, kind, ns, name, finalizer string) Object {
	return Object{
		APIVersion: apiVersion, Kind: kind, Namespace: ns, Name: name,
		DeletionTimestamp: "2026-11-01T00:00:00Z", Finalizers: []string{finalizer},
	}
}

func only(t *testing.T, r Report) Blocker {
	t.Helper()
	if len(r.Findings) != 1 || len(r.Findings[0].Blockers) != 1 {
		t.Fatalf("want exactly one blocker, got %+v", r.Findings)
	}
	return r.Findings[0].Blockers[0]
}

// The README caveat this file exists for: a controller whose name shares no
// label with its finalizer domain. The heuristic calls it owner-missing and
// prints a strip command; a config entry names the real owner.
func TestConfigNamesAControllerTheHeuristicCannotFind(t *testing.T) {
	s := Snapshot{
		Objects:     []Object{stuck("v1", "Service", "app", "web", "cleanup.platform.example.com")},
		Deployments: []Deployment{{Namespace: "infra", Name: "reaper", Replicas: 2, ReadyReplicas: 0}},
	}
	if b := only(t, Resolve(s)); b.Verdict != OwnerMissing || b.MatchedBy != MatchedByHeuristic {
		t.Fatalf("without config: %s by %s, want owner-missing by heuristic", b.Verdict, b.MatchedBy)
	}

	owners, err := LoadOwners("../../testdata/owners.yaml")
	if err != nil {
		t.Fatal(err)
	}
	b := only(t, ResolveWithOwners(s, owners))
	if b.Verdict != OwnerUnhealthy {
		t.Fatalf("verdict = %s, want %s", b.Verdict, OwnerUnhealthy)
	}
	if b.MatchedBy != MatchedByConfig {
		t.Errorf("matchedBy = %q, want config", b.MatchedBy)
	}
	if b.Owner != "Deployment infra/reaper" {
		t.Errorf("owner = %q", b.Owner)
	}
	if b.Remediation != "" {
		t.Errorf("a matched owner must not come with a strip command: %s", b.Remediation)
	}
	if !strings.Contains(b.Warning, "DNS records") {
		t.Errorf("warning = %q, want the config cloudLeak", b.Warning)
	}
}

func TestCatalogFindsTheArgoCDStatefulSet(t *testing.T) {
	s := Snapshot{
		Objects: []Object{stuck("argoproj.io/v1alpha1", "Application", "argocd", "web", "resources-finalizer.argocd.argoproj.io")},
		Deployments: []Deployment{
			{Namespace: "argocd", Name: "argocd-server", Replicas: 1, ReadyReplicas: 1},
			{Kind: "StatefulSet", Namespace: "argocd", Name: "argocd-application-controller", Replicas: 1, ReadyReplicas: 0},
		},
	}
	b := only(t, Resolve(s))
	// The heuristic alone would have picked the healthy argocd-server.
	if b.Verdict != OwnerUnhealthy || b.Owner != "StatefulSet argocd/argocd-application-controller" {
		t.Fatalf("got %s / %s, want the dead application controller", b.Verdict, b.Owner)
	}
	if b.MatchedBy != MatchedByCatalog {
		t.Errorf("matchedBy = %q, want catalog", b.MatchedBy)
	}
}

func TestConfigOverridesTheCatalog(t *testing.T) {
	owners, err := LoadOwners("../../testdata/owners.yaml")
	if err != nil {
		t.Fatal(err)
	}
	s := Snapshot{
		Objects: []Object{stuck("argoproj.io/v1alpha1", "Application", "gitops", "web", "resources-finalizer.argocd.argoproj.io")},
		Deployments: []Deployment{
			{Kind: "StatefulSet", Namespace: "argocd", Name: "argocd-application-controller", Replicas: 1, ReadyReplicas: 1},
			{Namespace: "gitops", Name: "argo-app-controller", Replicas: 1, ReadyReplicas: 1},
		},
	}
	b := only(t, ResolveWithOwners(s, owners))
	if b.Owner != "Deployment gitops/argo-app-controller" || b.MatchedBy != MatchedByConfig {
		t.Errorf("got %s by %s, want the config workload", b.Owner, b.MatchedBy)
	}
}

// A config entry that sets no cloudLeak must not silence the catalog's leak
// warning for the same finalizer.
func TestConfigWithoutCloudLeakKeepsTheCatalogWarning(t *testing.T) {
	owners := []Owner{{Finalizer: "karpenter.sh/termination", Workload: "autoscaling/karpenter"}}
	s := Snapshot{
		Objects:     []Object{stuck("v1", "Node", "", "ip-10-0-0-1", "karpenter.sh/termination")},
		Deployments: []Deployment{{Namespace: "autoscaling", Name: "karpenter", Replicas: 2, ReadyReplicas: 2}},
	}
	b := only(t, ResolveWithOwners(s, owners))
	if b.MatchedBy != MatchedByConfig || b.Verdict != OwnerHealthy {
		t.Fatalf("got %s by %s", b.Verdict, b.MatchedBy)
	}
	if !strings.Contains(b.Warning, "instance") {
		t.Errorf("warning = %q, want the catalog's leak warning", b.Warning)
	}
}

func TestCatalogWorkloadInAnotherNamespaceStillMatches(t *testing.T) {
	s := Snapshot{
		Objects:     []Object{stuck("keda.sh/v1alpha1", "ScaledObject", "app", "api", "finalizer.keda.sh")},
		Deployments: []Deployment{{Namespace: "autoscalers", Name: "keda-operator", Replicas: 1, ReadyReplicas: 1}},
	}
	b := only(t, Resolve(s))
	if b.Owner != "Deployment autoscalers/keda-operator" || b.MatchedBy != MatchedByCatalog {
		t.Errorf("got %s by %s", b.Owner, b.MatchedBy)
	}
}

// When the catalog's workload is not there, the heuristic still decides, and
// the reason says the expected controller is absent.
func TestMissingCatalogWorkloadFallsBackToTheHeuristic(t *testing.T) {
	r := Resolve(fixture(t, "stuck-cluster.json"))
	b := blockerFor(t, r, "web")
	if b.Verdict != OwnerMissing || b.MatchedBy != MatchedByHeuristic {
		t.Fatalf("got %s by %s, want owner-missing by heuristic", b.Verdict, b.MatchedBy)
	}
	if !strings.Contains(b.Reason, "kube-system/aws-load-balancer-controller") {
		t.Errorf("reason %q should name the workload the catalog expected", b.Reason)
	}
	if b.Remediation == "" {
		t.Error("owner-missing still prints the strip command")
	}
}

func TestFluxFinalizerResolvesByAPIGroup(t *testing.T) {
	deps := []Deployment{
		{Namespace: "flux-system", Name: "kustomize-controller", Replicas: 1, ReadyReplicas: 1},
		{Namespace: "flux-system", Name: "helm-controller", Replicas: 1, ReadyReplicas: 0},
	}
	cases := map[string]string{
		"kustomize.toolkit.fluxcd.io/v1": "Deployment flux-system/kustomize-controller",
		"helm.toolkit.fluxcd.io/v2":      "Deployment flux-system/helm-controller",
	}
	for apiVersion, want := range cases {
		s := Snapshot{Objects: []Object{stuck(apiVersion, "X", "app", "x", "finalizers.fluxcd.io")}, Deployments: deps}
		if b := only(t, Resolve(s)); b.Owner != want {
			t.Errorf("%s: owner = %q, want %q", apiVersion, b.Owner, want)
		}
	}
}

func TestKindFilterBeatsAnUnfilteredEntry(t *testing.T) {
	s := Snapshot{
		Objects: []Object{stuck("argoproj.io/v1alpha1", "ApplicationSet", "argocd", "all", "resources-finalizer.argocd.argoproj.io")},
		Deployments: []Deployment{
			{Kind: "StatefulSet", Namespace: "argocd", Name: "argocd-application-controller", Replicas: 1, ReadyReplicas: 1},
			{Namespace: "argocd", Name: "argocd-applicationset-controller", Replicas: 1, ReadyReplicas: 1},
		},
	}
	if b := only(t, Resolve(s)); b.Owner != "Deployment argocd/argocd-applicationset-controller" {
		t.Errorf("owner = %q, want the ApplicationSet controller", b.Owner)
	}
}

func TestGlobEntries(t *testing.T) {
	rook := Owner{Finalizer: "*.ceph.rook.io", Workload: "rook-ceph/rook-ceph-operator"}
	if !rook.matches(Object{}, "cephcluster.ceph.rook.io") {
		t.Error("*.ceph.rook.io should match cephcluster.ceph.rook.io")
	}
	if rook.matches(Object{}, "ceph.rook.io") {
		t.Error("*.ceph.rook.io should need a label in front")
	}
	group := Owner{Finalizer: "group.ingress.k8s.aws/*", Workload: "x"}
	if !group.matches(Object{}, "group.ingress.k8s.aws/public") {
		t.Error("glob should match an explicit ingress group finalizer")
	}
	exact := Owner{Finalizer: "group.ingress.k8s.aws/public", Workload: "y"}
	if got, _ := bestOwner([]Owner{group, exact}, Object{}, "group.ingress.k8s.aws/public"); got.Workload != "y" {
		t.Errorf("exact entry should beat the glob, got %+v", got)
	}
}

func TestBuiltinAndHeuristicAreRecorded(t *testing.T) {
	r := Resolve(fixture(t, "stuck-cluster.json"))
	if b := blockerFor(t, r, "data-0"); b.MatchedBy != MatchedByBuiltin {
		t.Errorf("pvc-protection matchedBy = %q, want builtin", b.MatchedBy)
	}
	if b := blockerFor(t, r, "web-tls"); b.MatchedBy != MatchedByHeuristic {
		t.Errorf("cert-manager matchedBy = %q, want heuristic", b.MatchedBy)
	}
}

func TestCatalogEntriesAreValidAndUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, o := range Catalog() {
		if err := o.validate(); err != nil {
			t.Errorf("%+v: %v", o, err)
		}
		key := o.Finalizer + "|" + o.Kind + "|" + o.Group
		if seen[key] {
			t.Errorf("duplicate catalog entry %s", key)
		}
		seen[key] = true
	}
	if len(seen) < 20 {
		t.Errorf("catalog has %d entries, want at least 20", len(seen))
	}
}

func TestLoadOwnersRejectsBadFiles(t *testing.T) {
	cases := map[string]string{
		"typo in a key":    "owners:\n  - finalizer: a.example.com\n    deployment: ns/x\n",
		"no finalizer":     "owners:\n  - workload: ns/x\n",
		"no workload":      "owners:\n  - finalizer: a.example.com\n",
		"too many slashes": "owners:\n  - finalizer: a.example.com\n    workload: a/b/c\n",
		"empty namespace":  "owners:\n  - finalizer: a.example.com\n    workload: /x\n",
		"bad glob":         "owners:\n  - finalizer: \"a[.example.com\"\n    workload: x\n",
		"not a list":       "owners: nope\n",
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := LoadOwners(writeOwners(t, body)); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestLoadOwnersAcceptsJSON(t *testing.T) {
	got, err := LoadOwners(writeOwners(t, `{"owners":[{"finalizer":"a.example.com","workload":"x"}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].Workload != "x" {
		t.Errorf("got %+v", got)
	}
}
