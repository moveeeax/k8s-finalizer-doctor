// Package kube turns a live cluster into a scan.Snapshot. It is the only part
// of the tool that talks to an API server, and it never writes: every call here
// is a list.
package kube

import (
	"context"
	"fmt"
	"strings"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"

	"github.com/moveeeax/k8s-finalizer-doctor/internal/scan"
)

// skipResources are high-cardinality resources that never carry an interesting
// finalizer. Listing every Event on a busy cluster is slow and pointless.
var skipResources = map[string]bool{
	"events":                    true,
	"events.events.k8s.io":      true,
	"componentstatuses":         true,
	"tokenreviews":              true,
	"subjectaccessreviews":      true,
	"selfsubjectaccessreviews":  true,
	"localsubjectaccessreviews": true,
	"selfsubjectrulesreviews":   true,
	"bindings":                  true,
}

var (
	crdGVR = schema.GroupVersionResource{Group: "apiextensions.k8s.io", Version: "v1", Resource: "customresourcedefinitions"}
	apiGVR = schema.GroupVersionResource{Group: "apiregistration.k8s.io", Version: "v1", Resource: "apiservices"}
	depGVR = schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "deployments"}
)

// Client bundles the two clients the collector needs.
type Client struct {
	dyn  dynamic.Interface
	disc discovery.DiscoveryInterface
}

// New builds a client from the standard kubeconfig loading rules, honouring an
// explicit kubeconfig path and context override.
func New(kubeconfig, kubeContext string) (*Client, error) {
	rules := clientcmd.NewDefaultClientConfigLoadingRules()
	if kubeconfig != "" {
		rules.ExplicitPath = kubeconfig
	}
	overrides := &clientcmd.ConfigOverrides{}
	if kubeContext != "" {
		overrides.CurrentContext = kubeContext
	}
	cfg, err := clientcmd.NewNonInteractiveDeferredLoadingClientConfig(rules, overrides).ClientConfig()
	if err != nil {
		return nil, fmt.Errorf("load kubeconfig: %w", err)
	}
	dyn, err := dynamic.NewForConfig(cfg)
	if err != nil {
		return nil, err
	}
	disc, err := discovery.NewDiscoveryClientForConfig(cfg)
	if err != nil {
		return nil, err
	}
	return &Client{dyn: dyn, disc: disc}, nil
}

// Collect walks every listable resource and returns the objects that are
// terminating, plus the inventory needed to resolve their finalizers.
//
// When namespace is set, namespaced resources are listed only there;
// cluster-scoped objects are always collected, because a stuck Namespace or PV
// is exactly what people are looking for and it belongs to no namespace.
func (c *Client) Collect(ctx context.Context, namespace string) (scan.Snapshot, []error) {
	var snap scan.Snapshot
	var warnings []error

	lists, err := c.disc.ServerPreferredResources()
	if err != nil {
		// Partial discovery is normal when an aggregated API is down, which is
		// itself a symptom worth surviving rather than aborting on.
		warnings = append(warnings, fmt.Errorf("partial discovery: %w", err))
	}

	for _, list := range lists {
		if list == nil {
			continue
		}
		gv, err := schema.ParseGroupVersion(list.GroupVersion)
		if err != nil {
			continue
		}
		for _, res := range list.APIResources {
			if strings.Contains(res.Name, "/") || skipResources[res.Name] || !contains(res.Verbs, "list") {
				continue
			}
			gvr := gv.WithResource(res.Name)
			var ri dynamic.ResourceInterface = c.dyn.Resource(gvr)
			if res.Namespaced && namespace != "" {
				ri = c.dyn.Resource(gvr).Namespace(namespace)
			}
			items, err := ri.List(ctx, metav1.ListOptions{})
			if err != nil {
				warnings = append(warnings, fmt.Errorf("list %s: %w", gvr.String(), err))
				continue
			}
			for i := range items.Items {
				if obj, ok := stuckObject(&items.Items[i], res.Name); ok {
					snap.Objects = append(snap.Objects, obj)
				}
			}
		}
	}

	snap.CRDs = c.collectCRDs(ctx, &warnings)
	snap.APIServices = c.collectAPIServices(ctx, &warnings)
	snap.Deployments = c.collectDeployments(ctx, &warnings)
	return snap, warnings
}

func stuckObject(u *unstructured.Unstructured, resource string) (scan.Object, bool) {
	ts := u.GetDeletionTimestamp()
	if ts == nil {
		return scan.Object{}, false
	}
	fin := u.GetFinalizers()
	if len(fin) == 0 {
		return scan.Object{}, false
	}
	return scan.Object{
		APIVersion:        u.GetAPIVersion(),
		Kind:              u.GetKind(),
		Resource:          resource,
		Namespace:         u.GetNamespace(),
		Name:              u.GetName(),
		DeletionTimestamp: ts.UTC().Format("2006-01-02T15:04:05Z"),
		Finalizers:        fin,
	}, true
}

func (c *Client) collectCRDs(ctx context.Context, warnings *[]error) []scan.CRD {
	items, err := c.dyn.Resource(crdGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		*warnings = append(*warnings, fmt.Errorf("list CRDs: %w", err))
		return nil
	}
	var out []scan.CRD
	for i := range items.Items {
		group, _, _ := unstructured.NestedString(items.Items[i].Object, "spec", "group")
		out = append(out, scan.CRD{Name: items.Items[i].GetName(), Group: group})
	}
	return out
}

func (c *Client) collectAPIServices(ctx context.Context, warnings *[]error) []scan.APIService {
	items, err := c.dyn.Resource(apiGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		*warnings = append(*warnings, fmt.Errorf("list APIServices: %w", err))
		return nil
	}
	var out []scan.APIService
	for i := range items.Items {
		obj := items.Items[i].Object
		group, _, _ := unstructured.NestedString(obj, "spec", "group")
		if group == "" {
			continue
		}
		out = append(out, scan.APIService{
			Name:      items.Items[i].GetName(),
			Group:     group,
			Available: apiServiceAvailable(obj),
		})
	}
	return out
}

func apiServiceAvailable(obj map[string]any) bool {
	conds, _, _ := unstructured.NestedSlice(obj, "status", "conditions")
	for _, c := range conds {
		m, ok := c.(map[string]any)
		if !ok {
			continue
		}
		if m["type"] == "Available" {
			return m["status"] == "True"
		}
	}
	// No condition yet: treat as available rather than accusing it of being down.
	return true
}

func (c *Client) collectDeployments(ctx context.Context, warnings *[]error) []scan.Deployment {
	items, err := c.dyn.Resource(depGVR).List(ctx, metav1.ListOptions{})
	if err != nil {
		*warnings = append(*warnings, fmt.Errorf("list Deployments: %w", err))
		return nil
	}
	var out []scan.Deployment
	for i := range items.Items {
		obj := items.Items[i].Object
		replicas, _, _ := unstructured.NestedInt64(obj, "spec", "replicas")
		ready, _, _ := unstructured.NestedInt64(obj, "status", "readyReplicas")
		out = append(out, scan.Deployment{
			Namespace:     items.Items[i].GetNamespace(),
			Name:          items.Items[i].GetName(),
			Replicas:      int32(replicas),
			ReadyReplicas: int32(ready),
		})
	}
	return out
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}
