package scan

import (
	"fmt"
	"os"
	"path"
	"strings"

	"sigs.k8s.io/yaml"
)

// Owner maps a finalizer to the workload that is supposed to remove it. It is
// one entry of the built-in catalog or of an --owners file.
type Owner struct {
	// Finalizer is the exact finalizer string, or a glob where "*" matches
	// anything except "/": "group.ingress.k8s.aws/*", "*.ceph.rook.io".
	Finalizer string `json:"finalizer"`
	// Workload is "namespace/name" or a bare "name" of the Deployment,
	// StatefulSet or DaemonSet that clears the finalizer.
	Workload string `json:"workload"`
	// Kind and Group optionally narrow the entry to stuck objects of that kind
	// or API group, for finalizer strings several controllers share.
	Kind  string `json:"kind,omitempty"`
	Group string `json:"group,omitempty"`
	// CloudLeak is what stripping the finalizer leaves behind outside the
	// cluster, printed as a warning. Empty keeps the built-in warning, if any.
	CloudLeak string `json:"cloudLeak,omitempty"`
}

// ownersFile is the on-disk shape of --owners.
type ownersFile struct {
	Owners []Owner `json:"owners"`
}

// Match sources recorded on a Blocker.
const (
	MatchedByConfig    = "config"
	MatchedByCatalog   = "catalog"
	MatchedByHeuristic = "heuristic"
	MatchedByBuiltin   = "builtin"
	MatchedByNone      = "none"
)

// catalog is the built-in owner map. Every finalizer string and default
// workload name here was checked against the controller's own source or
// install manifests; installs in a non-default namespace still match because a
// "namespace/name" entry falls back to the same name in any namespace.
var catalog = []Owner{
	// Argo CD. The application controller is a StatefulSet in the upstream
	// install manifests, not a Deployment.
	{Finalizer: "resources-finalizer.argocd.argoproj.io", Workload: "argocd/argocd-application-controller"},
	{Finalizer: "resources-finalizer.argocd.argoproj.io/*", Workload: "argocd/argocd-application-controller"},
	{Finalizer: "resources-finalizer.argocd.argoproj.io", Kind: "ApplicationSet", Group: "argoproj.io", Workload: "argocd/argocd-applicationset-controller"},
	{Finalizer: "pre-delete-finalizer.argocd.argoproj.io", Workload: "argocd/argocd-application-controller"},
	{Finalizer: "post-delete-finalizer.argocd.argoproj.io", Workload: "argocd/argocd-application-controller"},
	{Finalizer: "argocd.argoproj.io/hook-finalizer", Workload: "argocd/argocd-application-controller"},

	// Argo Workflows.
	{Finalizer: "workflows.argoproj.io/artifact-gc", Workload: "argo/workflow-controller"},

	// Flux: one finalizer string, a different controller per API group.
	{Finalizer: "finalizers.fluxcd.io", Group: "kustomize.toolkit.fluxcd.io", Workload: "flux-system/kustomize-controller"},
	{Finalizer: "finalizers.fluxcd.io", Group: "helm.toolkit.fluxcd.io", Workload: "flux-system/helm-controller"},
	{Finalizer: "finalizers.fluxcd.io", Group: "source.toolkit.fluxcd.io", Workload: "flux-system/source-controller"},
	{Finalizer: "finalizers.fluxcd.io", Group: "notification.toolkit.fluxcd.io", Workload: "flux-system/notification-controller"},

	// cert-manager ACME orders and challenges.
	{Finalizer: "finalizer.acme.cert-manager.io", Workload: "cert-manager/cert-manager"},
	{Finalizer: "acme.cert-manager.io/finalizer", Workload: "cert-manager/cert-manager"},

	// Crossplane composite resources.
	{Finalizer: "composite.apiextensions.crossplane.io", Workload: "crossplane-system/crossplane"},

	// AWS Load Balancer Controller.
	{Finalizer: "service.k8s.aws/resources", Workload: "kube-system/aws-load-balancer-controller", CloudLeak: "may leak an AWS load balancer / target group"},
	{Finalizer: "ingress.k8s.aws/resources", Workload: "kube-system/aws-load-balancer-controller", CloudLeak: "may leak an AWS ALB and its listeners"},
	{Finalizer: "group.ingress.k8s.aws/*", Workload: "kube-system/aws-load-balancer-controller", CloudLeak: "may leak an AWS ALB and its listeners"},
	{Finalizer: "elbv2.k8s.aws/resources", Workload: "kube-system/aws-load-balancer-controller", CloudLeak: "may leave targets registered in an AWS target group"},
	{Finalizer: "gateway.k8s.aws/*", Workload: "kube-system/aws-load-balancer-controller", CloudLeak: "may leak an AWS load balancer created for a Gateway"},
	{Finalizer: "aga.k8s.aws/resources", Workload: "kube-system/aws-load-balancer-controller", CloudLeak: "may leak an AWS Global Accelerator"},

	// AWS EBS CSI driver: the external-attacher sidecar runs in the controller
	// Deployment and names its finalizer after the sanitized driver name.
	{Finalizer: "external-attacher/ebs-csi-aws-com", Workload: "kube-system/ebs-csi-controller"},

	// Karpenter, on Nodes and NodeClaims.
	{Finalizer: "karpenter.sh/termination", Workload: "kube-system/karpenter", CloudLeak: "may leak the cloud instance behind the node"},

	// Velero. Data movement and pod volume backups run in the node-agent
	// DaemonSet, restores in the server Deployment.
	{Finalizer: "restores.velero.io/external-resources-finalizer", Workload: "velero/velero"},
	{Finalizer: "velero.io/data-upload-download-finalizer", Workload: "velero/node-agent"},
	{Finalizer: "velero.io/pod-volume-finalizer", Workload: "velero/node-agent"},

	// Rook Ceph: every CR gets "<lowercase kind>.ceph.rook.io".
	{Finalizer: "*.ceph.rook.io", Workload: "rook-ceph/rook-ceph-operator"},

	// KEDA.
	{Finalizer: "finalizer.keda.sh", Workload: "keda/keda-operator"},

	// Calico via the Tigera operator.
	{Finalizer: "tigera.io/operator-cleanup", Workload: "tigera-operator/tigera-operator"},

	// Cluster API core and the kubeadm control plane provider.
	{Finalizer: "cluster.cluster.x-k8s.io", Workload: "capi-system/capi-controller-manager", CloudLeak: "may leak the workload cluster's infrastructure"},
	{Finalizer: "machine.cluster.x-k8s.io", Workload: "capi-system/capi-controller-manager", CloudLeak: "may leak the machine's VM"},
	{Finalizer: "machinepool.cluster.x-k8s.io", Workload: "capi-system/capi-controller-manager"},
	{Finalizer: "cluster.x-k8s.io/machineset", Workload: "capi-system/capi-controller-manager"},
	{Finalizer: "cluster.x-k8s.io/machinedeployment", Workload: "capi-system/capi-controller-manager"},
	{Finalizer: "machineset.topology.cluster.x-k8s.io", Workload: "capi-system/capi-controller-manager"},
	{Finalizer: "machinedeployment.topology.cluster.x-k8s.io", Workload: "capi-system/capi-controller-manager"},
	{Finalizer: "addons.cluster.x-k8s.io", Workload: "capi-system/capi-controller-manager"},
	{Finalizer: "kubeadm.controlplane.cluster.x-k8s.io", Workload: "capi-kubeadm-control-plane-system/capi-kubeadm-control-plane-controller-manager"},
}

// Catalog returns a copy of the built-in owner map.
func Catalog() []Owner { return append([]Owner(nil), catalog...) }

// LoadOwners reads an --owners file. YAML and JSON both parse; unknown keys
// are an error so a typo cannot silently turn an entry into a no-op.
func LoadOwners(file string) ([]Owner, error) {
	raw, err := os.ReadFile(file)
	if err != nil {
		return nil, err
	}
	var f ownersFile
	if err := yaml.UnmarshalStrict(raw, &f); err != nil {
		return nil, fmt.Errorf("parse %s: %w", file, err)
	}
	for i, o := range f.Owners {
		if err := o.validate(); err != nil {
			return nil, fmt.Errorf("%s: owners[%d]: %w", file, i, err)
		}
	}
	return f.Owners, nil
}

func (o Owner) validate() error {
	if o.Finalizer == "" {
		return fmt.Errorf("finalizer is required")
	}
	if _, err := path.Match(o.Finalizer, ""); err != nil {
		return fmt.Errorf("finalizer %q is not a valid pattern: %w", o.Finalizer, err)
	}
	ns, name := o.workloadRef()
	if name == "" || strings.Count(o.Workload, "/") > 1 || (strings.Contains(o.Workload, "/") && ns == "") {
		return fmt.Errorf("workload %q must be \"namespace/name\" or \"name\"", o.Workload)
	}
	return nil
}

func (o Owner) workloadRef() (namespace, name string) {
	if i := strings.Index(o.Workload, "/"); i >= 0 {
		return o.Workload[:i], o.Workload[i+1:]
	}
	return "", o.Workload
}

// specificity ranks entries that match the same finalizer: an exact string
// beats a glob, and a kind or group filter beats an unfiltered entry.
func (o Owner) specificity() int {
	s := 0
	if !strings.ContainsAny(o.Finalizer, "*?[") {
		s += 4
	}
	if o.Kind != "" {
		s += 2
	}
	if o.Group != "" {
		s++
	}
	return s
}

func (o Owner) matches(obj Object, finalizer string) bool {
	if ok, _ := path.Match(o.Finalizer, finalizer); !ok {
		return false
	}
	if o.Kind != "" && o.Kind != obj.Kind {
		return false
	}
	if o.Group != "" && o.Group != apiGroup(obj.APIVersion) {
		return false
	}
	return true
}

// bestOwner returns the most specific entry in owners that covers finalizer on
// obj. Ties go to the earlier entry.
func bestOwner(owners []Owner, obj Object, finalizer string) (Owner, bool) {
	var best Owner
	found := false
	for _, o := range owners {
		if !o.matches(obj, finalizer) {
			continue
		}
		if !found || o.specificity() > best.specificity() {
			best, found = o, true
		}
	}
	return best, found
}

// apiGroup returns the group of an apiVersion, "" for the core group.
func apiGroup(apiVersion string) string {
	if i := strings.Index(apiVersion, "/"); i >= 0 {
		return apiVersion[:i]
	}
	return ""
}
