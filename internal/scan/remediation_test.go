package scan

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResourceName(t *testing.T) {
	cases := []struct{ kind, resource, want string }{
		{"Namespace", "", "namespaces"},
		{"PersistentVolumeClaim", "", "persistentvolumeclaims"},
		{"Ingress", "", "ingresses"},
		{"NetworkPolicy", "", "networkpolicies"},
		{"Endpoints", "", "endpoints"},
		{"Gateway", "", "gateways"},
		// Discovery wins over the guess.
		{"Bucket", "buckets.v1beta1", "buckets.v1beta1"},
	}
	for _, c := range cases {
		if got := resourceName(Object{Kind: c.kind, Resource: c.resource}); got != c.want {
			t.Errorf("resourceName(%s) = %q, want %q", c.kind, got, c.want)
		}
	}
}

func TestRemediationQualifiesCustomResourcesWithTheirGroup(t *testing.T) {
	o := Object{
		APIVersion: "cert-manager.io/v1", Kind: "Certificate",
		Namespace: "app", Name: "web-tls",
		Finalizers: []string{"a", "finalizer.cert-manager.io"},
	}
	got := RemediationCommand(o, "finalizer.cert-manager.io")
	if !strings.HasPrefix(got, "kubectl patch certificates.cert-manager.io/web-tls -n app ") {
		t.Fatalf("got %q", got)
	}
	// The finalizer sits at index 1, and the test op must pin that index.
	if !strings.Contains(got, `"path":"/metadata/finalizers/1","value":"finalizer.cert-manager.io"`) {
		t.Errorf("patch does not pin index 1: %s", got)
	}
}

func TestRemediationOfAnAbsentFinalizerIsEmpty(t *testing.T) {
	o := Object{Kind: "Pod", Name: "p", Finalizers: []string{"a"}}
	if got := RemediationCommand(o, "b"); got != "" {
		t.Errorf("got %q, want empty", got)
	}
}

func TestSnapshotRoundTrip(t *testing.T) {
	in := fixture(t, "stuck-cluster.json")
	path := filepath.Join(t.TempDir(), "snap.json")
	if err := SaveSnapshot(path, in); err != nil {
		t.Fatal(err)
	}
	out, err := LoadSnapshot(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(out.Objects) != len(in.Objects) || len(out.Deployments) != len(in.Deployments) {
		t.Fatalf("round trip lost data: %d/%d objects", len(out.Objects), len(in.Objects))
	}
}

func TestLoadSnapshotRejectsGarbage(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{nope"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSnapshot(path); err == nil {
		t.Fatal("want a parse error")
	}
}
