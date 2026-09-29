package scan

import (
	"strings"
	"testing"
)

func fixture(t *testing.T, name string) Snapshot {
	t.Helper()
	s, err := LoadSnapshot("../../testdata/" + name)
	if err != nil {
		t.Fatalf("load %s: %v", name, err)
	}
	return s
}

// blockerFor finds the single blocker for an object name in a report.
func blockerFor(t *testing.T, r Report, objName string) Blocker {
	t.Helper()
	for _, f := range r.Findings {
		if f.Name != objName {
			continue
		}
		if len(f.Blockers) != 1 {
			t.Fatalf("%s: want 1 blocker, got %d", objName, len(f.Blockers))
		}
		return f.Blockers[0]
	}
	t.Fatalf("no finding for %q", objName)
	return Blocker{}
}

// Acceptance: a namespace stuck on a finalizer whose CRD is uninstalled is
// reported owner-missing with a patch command clearing exactly that finalizer.
func TestUninstalledCRDIsSafeToStrip(t *testing.T) {
	r := Resolve(fixture(t, "stuck-cluster.json"))
	b := blockerFor(t, r, "argocd")

	if b.Verdict != OwnerMissing {
		t.Fatalf("verdict = %s, want %s", b.Verdict, OwnerMissing)
	}
	if b.Verdict.Action() != "strip" {
		t.Errorf("action = %q, want strip", b.Verdict.Action())
	}
	want := `kubectl patch namespaces/argocd --type=json -p '[{"op":"test","path":"/metadata/finalizers/0","value":"resources-finalizer.argocd.argoproj.io"},{"op":"remove","path":"/metadata/finalizers/0"}]'`
	if b.Remediation != want {
		t.Errorf("remediation:\n got %s\nwant %s", b.Remediation, want)
	}
}

// Acceptance: a finalizer whose controller Deployment has zero ready replicas is
// owner-unhealthy, names the Deployment, and offers no strip command.
func TestDeadControllerIsNamedNotStripped(t *testing.T) {
	r := Resolve(fixture(t, "stuck-cluster.json"))
	b := blockerFor(t, r, "web-tls")

	if b.Verdict != OwnerUnhealthy {
		t.Fatalf("verdict = %s, want %s", b.Verdict, OwnerUnhealthy)
	}
	if b.Owner != "Deployment cert-manager/cert-manager" {
		t.Errorf("owner = %q, want the cert-manager Deployment", b.Owner)
	}
	if b.Remediation != "" {
		t.Errorf("remediation = %q, want none for a fixable controller", b.Remediation)
	}
	if !strings.Contains(b.Reason, "0/1") {
		t.Errorf("reason %q should report the ready/desired counts", b.Reason)
	}
}

// Acceptance: a cluster with nothing terminating produces no findings.
func TestCleanClusterHasNoFindings(t *testing.T) {
	r := Resolve(fixture(t, "clean-cluster.json"))
	if r.Stuck() {
		t.Fatalf("clean cluster reported %d findings", len(r.Findings))
	}
}

func TestHealthyControllerMeansWait(t *testing.T) {
	r := Resolve(fixture(t, "stuck-cluster.json"))
	b := blockerFor(t, r, "assets")

	if b.Verdict != OwnerHealthy {
		t.Fatalf("verdict = %s, want %s", b.Verdict, OwnerHealthy)
	}
	if b.Owner != "Deployment crossplane-system/crossplane-provider-gcp" {
		t.Errorf("owner = %q, want the crossplane provider", b.Owner)
	}
}

func TestBuiltinFinalizerIsNotBlamedOnAMissingOwner(t *testing.T) {
	r := Resolve(fixture(t, "stuck-cluster.json"))
	b := blockerFor(t, r, "data-0")

	if b.Verdict != OwnerBuiltin {
		t.Fatalf("verdict = %s, want %s", b.Verdict, OwnerBuiltin)
	}
	if b.Remediation != "" {
		t.Error("built-in finalizers must never come with a strip command")
	}
}

func TestUnavailableAPIServiceIsUnhealthyNotMissing(t *testing.T) {
	r := Resolve(fixture(t, "stuck-cluster.json"))
	b := blockerFor(t, r, "scaler")

	if b.Verdict != OwnerUnhealthy {
		t.Fatalf("verdict = %s, want %s", b.Verdict, OwnerUnhealthy)
	}
	if b.Owner != "APIService v1.webhook.example.com" {
		t.Errorf("owner = %q, want the APIService", b.Owner)
	}
}

func TestCloudFinalizerCarriesALeakWarning(t *testing.T) {
	r := Resolve(fixture(t, "stuck-cluster.json"))
	b := blockerFor(t, r, "web")

	if b.Verdict != OwnerMissing {
		t.Fatalf("verdict = %s, want %s", b.Verdict, OwnerMissing)
	}
	if !strings.Contains(b.Warning, "load balancer") {
		t.Errorf("warning = %q, want a load balancer leak warning", b.Warning)
	}
}

func TestDomainSuffixesWalksUpTheDomain(t *testing.T) {
	got := domainSuffixes("resources-finalizer.argocd.argoproj.io")
	want := []string{
		"resources-finalizer.argocd.argoproj.io",
		"argocd.argoproj.io",
		"argoproj.io",
		"io",
	}
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if domainSuffixes("") != nil {
		t.Error("a bare finalizer has no domain suffixes")
	}
}

// cert-manager and Argo CD write finalizers with no slash at all. Those still
// carry a resolvable domain, and missing that is the difference between naming
// the controller and shrugging.
func TestSlashlessFinalizersStillHaveADomain(t *testing.T) {
	cases := map[string]string{
		"finalizer.cert-manager.io":              "finalizer.cert-manager.io",
		"resources-finalizer.argocd.argoproj.io": "resources-finalizer.argocd.argoproj.io",
		"service.k8s.aws/resources":              "service.k8s.aws",
		"orphan":                                 "",
		"foregroundDeletion":                     "",
	}
	for in, want := range cases {
		if got := domain(in); got != want {
			t.Errorf("domain(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIdentityTokensDropGenericLabels(t *testing.T) {
	got := identityTokens("finalizer.cert-manager.io")
	for _, tok := range got {
		if tok == "io" {
			t.Fatalf("generic label leaked into tokens: %v", got)
		}
	}
	if got[0] != "finalizer" {
		t.Errorf("tokens not longest-first: %v", got)
	}
}

func TestFilterNamespaceKeepsTheInventory(t *testing.T) {
	s := FilterNamespace(fixture(t, "stuck-cluster.json"), "app")
	for _, o := range s.Objects {
		if o.Namespace != "app" {
			t.Fatalf("object %s/%s survived the filter", o.Namespace, o.Name)
		}
	}
	if len(s.Deployments) == 0 || len(s.CRDs) == 0 {
		t.Error("filtering by namespace must not drop the owner inventory")
	}
}

func TestResolveSkipsObjectsWithoutFinalizers(t *testing.T) {
	s := Snapshot{Objects: []Object{
		{Kind: "Pod", Namespace: "app", Name: "gone", DeletionTimestamp: "2026-09-29T00:00:00Z"},
	}}
	if r := Resolve(s); r.Stuck() {
		t.Fatalf("an object with no finalizers is not stuck: %+v", r.Findings)
	}
}
