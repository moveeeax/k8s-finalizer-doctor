package kube

import (
	"testing"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

func obj(m map[string]any) *unstructured.Unstructured {
	return &unstructured.Unstructured{Object: m}
}

func TestStuckObjectNeedsBothATimestampAndAFinalizer(t *testing.T) {
	cases := []struct {
		name string
		meta map[string]any
		want bool
	}{
		{"terminating with a finalizer", map[string]any{
			"name": "argocd", "deletionTimestamp": "2026-09-24T11:02:19Z",
			"finalizers": []any{"resources-finalizer.argocd.argoproj.io"},
		}, true},
		{"terminating with no finalizer is just slow", map[string]any{
			"name": "gone", "deletionTimestamp": "2026-09-24T11:02:19Z",
		}, false},
		{"a finalizer on a live object is not stuck", map[string]any{
			"name": "live", "finalizers": []any{"finalizer.cert-manager.io"},
		}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			u := obj(map[string]any{
				"apiVersion": "v1", "kind": "Namespace", "metadata": c.meta,
			})
			got, ok := stuckObject(u, "namespaces")
			if ok != c.want {
				t.Fatalf("stuck = %v, want %v", ok, c.want)
			}
			if ok && got.Resource != "namespaces" {
				t.Errorf("resource = %q, want the discovered plural", got.Resource)
			}
		})
	}
}

func TestAPIServiceAvailable(t *testing.T) {
	cases := []struct {
		name  string
		conds []any
		want  bool
	}{
		{"available", []any{map[string]any{"type": "Available", "status": "True"}}, true},
		{"down", []any{map[string]any{"type": "Available", "status": "False"}}, false},
		// A freshly created APIService has no conditions yet. Calling that
		// "down" would blame every new aggregated API for a stuck delete.
		{"no conditions yet", nil, true},
		{"unrelated condition only", []any{map[string]any{"type": "Other", "status": "False"}}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := map[string]any{"status": map[string]any{}}
			if c.conds != nil {
				m["status"] = map[string]any{"conditions": c.conds}
			}
			if got := apiServiceAvailable(m); got != c.want {
				t.Errorf("available = %v, want %v", got, c.want)
			}
		})
	}
}

func TestContains(t *testing.T) {
	verbs := []string{"get", "list", "watch"}
	if !contains(verbs, "list") {
		t.Error("list should be found")
	}
	if contains(verbs, "deletecollection") {
		t.Error("deletecollection should not be found")
	}
}

// A DaemonSet has no spec.replicas. Reading it like a Deployment would report
// every healthy node agent as 0/0 ready and blame it for a stuck delete.
func TestWorkloadReadsDaemonSetCounts(t *testing.T) {
	ds := obj(map[string]any{
		"metadata": map[string]any{"namespace": "velero", "name": "node-agent"},
		"status":   map[string]any{"desiredNumberScheduled": int64(3), "numberReady": int64(2)},
	})
	got := workload(ds, "DaemonSet")
	if got.Kind != "DaemonSet" || got.Replicas != 3 || got.ReadyReplicas != 2 {
		t.Errorf("daemonset = %+v, want DaemonSet 2/3", got)
	}

	sts := obj(map[string]any{
		"metadata": map[string]any{"namespace": "argocd", "name": "argocd-application-controller"},
		"spec":     map[string]any{"replicas": int64(1)},
		"status":   map[string]any{"readyReplicas": int64(1)},
	})
	got = workload(sts, "StatefulSet")
	if got.Kind != "StatefulSet" || got.Replicas != 1 || got.ReadyReplicas != 1 {
		t.Errorf("statefulset = %+v, want StatefulSet 1/1", got)
	}

	dep := obj(map[string]any{
		"metadata": map[string]any{"namespace": "cert-manager", "name": "cert-manager"},
		"spec":     map[string]any{"replicas": int64(1)},
	})
	if got = workload(dep, "Deployment"); got.Kind != "" {
		t.Errorf("deployment kind = %q, want empty so old snapshots stay identical", got.Kind)
	}
}
