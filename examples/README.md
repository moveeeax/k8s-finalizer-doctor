# Examples

`stuck-cluster.json` is a snapshot of a cluster with six objects stuck on six
different kinds of finalizer. It is the same fixture the tests run against, so
you can see every verdict without breaking a real cluster.

```console
$ k8s-finalizer-doctor --snapshot examples/stuck-cluster.json
```

What each object demonstrates:

| Object | Finalizer | Verdict | Why |
| --- | --- | --- | --- |
| `Namespace/argocd` | `resources-finalizer.argocd.argoproj.io` | `owner-missing` | Argo CD was uninstalled; nothing claims the domain, so the strip command is printed |
| `app Certificate/web-tls` | `finalizer.cert-manager.io` | `owner-unhealthy` | the cert-manager Deployment is 0/1 ready — fix it, do not strip |
| `app ExternalScaler/scaler` | `webhook.example.com/cleanup` | `owner-unhealthy` | the aggregated APIService is not `Available` |
| `app Bucket/assets` | `finalizer.gcp.crossplane.io` | `owner-healthy` | the Crossplane provider is up and still working |
| `app PVC/data-0` | `kubernetes.io/pvc-protection` | `owner-builtin` | the control plane holds it; a pod is still using the claim |
| `app Service/web` | `service.k8s.aws/resources` | `owner-missing` | safe to strip, with a warning that it may leak an AWS load balancer |

## Making your own snapshot

`--dump` writes the state the tool collected, so you can hand a stuck cluster to
someone else, attach it to a ticket, or turn an incident into a test fixture:

```console
$ k8s-finalizer-doctor --context prod --dump incident-4412.json
$ k8s-finalizer-doctor --snapshot incident-4412.json -o json | jq '.findings[].blockers[].verdict'
```
