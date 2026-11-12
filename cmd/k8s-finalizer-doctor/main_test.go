package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestExitCodes(t *testing.T) {
	cases := []struct {
		name string
		args []string
		want int
	}{
		{"stuck objects exit 1", []string{"--snapshot", "../../testdata/stuck-cluster.json"}, 1},
		{"clean cluster exits 0", []string{"--snapshot", "../../testdata/clean-cluster.json"}, 0},
		{"json output keeps the exit code", []string{"--snapshot", "../../testdata/stuck-cluster.json", "-o", "json"}, 1},
		{"missing snapshot is a tool error", []string{"--snapshot", "../../testdata/nope.json"}, 2},
		{"bad output format is a tool error", []string{"--snapshot", "../../testdata/clean-cluster.json", "-o", "yaml"}, 2},
		{"version exits 0", []string{"--version"}, 0},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := run(c.args); got != c.want {
				t.Errorf("run(%v) = %d, want %d", c.args, got, c.want)
			}
		})
	}
}

func TestNamespaceFilterNarrowsTheScan(t *testing.T) {
	// Only the cluster-scoped Namespace object lives outside "app", so
	// filtering to a namespace with nothing stuck must exit clean.
	if got := run([]string{"--snapshot", "../../testdata/stuck-cluster.json", "-n", "kube-system"}); got != 0 {
		t.Errorf("exit = %d, want 0 for a namespace with nothing stuck", got)
	}
	if got := run([]string{"--snapshot", "../../testdata/stuck-cluster.json", "-n", "app"}); got != 1 {
		t.Errorf("exit = %d, want 1 for the namespace holding the stuck objects", got)
	}
}

func TestAllNamespacesOverridesNamespace(t *testing.T) {
	o, err := parseFlags([]string{"-n", "app", "-A"}, os.Stderr)
	if err != nil {
		t.Fatal(err)
	}
	if o.namespace != "" {
		t.Errorf("namespace = %q, want it cleared by -A", o.namespace)
	}
}

func TestDumpWritesASnapshot(t *testing.T) {
	out := filepath.Join(t.TempDir(), "dump.json")
	if got := run([]string{"--snapshot", "../../testdata/stuck-cluster.json", "--dump", out, "-o", "json"}); got != 1 {
		t.Fatalf("exit = %d, want 1", got)
	}
	info, err := os.Stat(out)
	if err != nil {
		t.Fatal(err)
	}
	if info.Size() == 0 {
		t.Error("dump file is empty")
	}
}

func TestOwnersFlag(t *testing.T) {
	if got := run([]string{"--snapshot", "../../testdata/stuck-cluster.json", "--owners", "../../testdata/owners.yaml"}); got != 1 {
		t.Errorf("exit = %d, want 1 with a valid owners file", got)
	}
	if got := run([]string{"--snapshot", "../../testdata/clean-cluster.json", "--owners", "../../testdata/nope.yaml"}); got != 2 {
		t.Errorf("exit = %d, want 2 for a missing owners file", got)
	}
	bad := filepath.Join(t.TempDir(), "owners.yaml")
	if err := os.WriteFile(bad, []byte("owners:\n  - finalizer: a.example.com\n    deployment: x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if got := run([]string{"--snapshot", "../../testdata/clean-cluster.json", "--owners", bad}); got != 2 {
		t.Errorf("exit = %d, want 2 for an owners file with an unknown key", got)
	}
}
