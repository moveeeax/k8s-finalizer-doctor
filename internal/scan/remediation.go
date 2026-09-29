package scan

import (
	"encoding/json"
	"fmt"
	"strings"
)

// irregularPlurals covers the kinds whose kubectl resource name is not just
// lowercase kind + "s".
var irregularPlurals = map[string]string{
	"Ingress":                  "ingresses",
	"NetworkPolicy":            "networkpolicies",
	"PodSecurityPolicy":        "podsecuritypolicies",
	"StorageClass":             "storageclasses",
	"PriorityClass":            "priorityclasses",
	"Policy":                   "policies",
	"Gateway":                  "gateways",
	"IngressClass":             "ingressclasses",
	"CustomResourceDefinition": "customresourcedefinitions",
	"Endpoints":                "endpoints",
}

// resourceName is what you type after `kubectl patch`. Live collection fills
// Object.Resource from discovery, which is authoritative; this is the fallback
// for hand-written snapshots.
func resourceName(o Object) string {
	if o.Resource != "" {
		return o.Resource
	}
	if p, ok := irregularPlurals[o.Kind]; ok {
		return p
	}
	low := strings.ToLower(o.Kind)
	switch {
	case strings.HasSuffix(low, "s"), strings.HasSuffix(low, "x"), strings.HasSuffix(low, "ch"), strings.HasSuffix(low, "sh"):
		return low + "es"
	case strings.HasSuffix(low, "y"):
		return low[:len(low)-1] + "ies"
	default:
		return low + "s"
	}
}

// target is the "resource.group/name" form, which disambiguates a CRD kind that
// collides with a built-in one.
func target(o Object) string {
	res := resourceName(o)
	if i := strings.Index(o.APIVersion, "/"); i > 0 {
		res += "." + o.APIVersion[:i]
	}
	return res + "/" + o.Name
}

// RemediationCommand builds the exact kubectl invocation that removes one
// finalizer and nothing else.
//
// The patch carries a `test` op before the `remove`. Without it, a JSON Patch
// index is a race: if the controller (or another operator) removes a different
// finalizer between your get and your patch, index 0 is now a different string
// and you silently delete the wrong one. The test op makes the API server
// reject the patch instead.
func RemediationCommand(o Object, finalizer string) string {
	idx := -1
	for i, f := range o.Finalizers {
		if f == finalizer {
			idx = i
			break
		}
	}
	if idx < 0 {
		return ""
	}
	patch := []map[string]any{
		{"op": "test", "path": fmt.Sprintf("/metadata/finalizers/%d", idx), "value": finalizer},
		{"op": "remove", "path": fmt.Sprintf("/metadata/finalizers/%d", idx)},
	}
	body, err := json.Marshal(patch)
	if err != nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("kubectl patch ")
	sb.WriteString(target(o))
	if o.Namespace != "" {
		sb.WriteString(" -n " + o.Namespace)
	}
	sb.WriteString(" --type=json -p '")
	sb.Write(body)
	sb.WriteString("'")
	return sb.String()
}
