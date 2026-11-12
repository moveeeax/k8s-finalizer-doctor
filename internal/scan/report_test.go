package scan

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestWriteTableShowsVerdictsAndCommands(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTable(&buf, Resolve(fixture(t, "stuck-cluster.json"))); err != nil {
		t.Fatal(err)
	}
	out := buf.String()

	for _, want := range []string{
		"OBJECT", "FINALIZER", "VERDICT", "ACTION", "OWNER", "MATCHED-BY",
		"builtin", "heuristic",
		"Namespace/argocd",
		"app Certificate/web-tls",
		"owner-missing", "owner-unhealthy", "owner-healthy", "owner-builtin",
		"Deployment cert-manager/cert-manager",
		"kubectl patch namespaces/argocd",
		"warning: stripping may leak an AWS load balancer",
		"6 object(s) stuck: 2 safe to strip, 2 blocked on a broken controller, 2 still waiting",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("table output missing %q\n---\n%s", want, out)
		}
	}
}

func TestWriteTableOnACleanCluster(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteTable(&buf, Resolve(fixture(t, "clean-cluster.json"))); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "nothing terminating") {
		t.Errorf("want a clean-cluster line, got %q", buf.String())
	}
}

func TestWriteJSONRoundTrips(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, Resolve(fixture(t, "stuck-cluster.json"))); err != nil {
		t.Fatal(err)
	}
	var got Report
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("output is not valid JSON: %v", err)
	}
	if len(got.Findings) != 6 {
		t.Fatalf("findings = %d, want 6", len(got.Findings))
	}
	if got.Scanned != 6 {
		t.Errorf("scanned = %d, want 6", got.Scanned)
	}
	for _, f := range got.Findings {
		for _, b := range f.Blockers {
			if b.MatchedBy == "" {
				t.Errorf("%s/%s %s: matchedBy is empty", f.Kind, f.Name, b.Finalizer)
			}
		}
	}
	if got.Findings[0].Namespace != "" || got.Findings[0].Name != "argocd" {
		t.Errorf("findings are not sorted cluster-scoped first: %+v", got.Findings[0])
	}
}

func TestJSONOmitsEmptyRemediation(t *testing.T) {
	var buf bytes.Buffer
	r := Report{Findings: []Finding{{Kind: "Pod", Name: "p", Blockers: []Blocker{{
		Finalizer: "x", Verdict: OwnerHealthy, Owner: "o", Reason: "r",
	}}}}, Scanned: 1}
	if err := WriteJSON(&buf, r); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "remediation") || strings.Contains(buf.String(), "warning") {
		t.Errorf("empty fields should be omitted:\n%s", buf.String())
	}
}
