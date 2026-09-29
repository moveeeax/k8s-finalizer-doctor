# k8s-finalizer-doctor

[![ci](https://github.com/moveeeax/k8s-finalizer-doctor/actions/workflows/ci.yml/badge.svg)](https://github.com/moveeeax/k8s-finalizer-doctor/actions/workflows/ci.yml)
[![Go](https://img.shields.io/badge/go-1.23%2B-00ADD8?logo=go&logoColor=white)](https://go.dev/)
[![License: MIT](https://img.shields.io/badge/license-MIT-green.svg)](LICENSE)

A namespace stuck `Terminating` for three days is always a finalizer nobody can
name. This names it: which finalizer, which controller is supposed to remove it,
and whether stripping it is safe.

Read-only. It prints the `kubectl patch` command; it never runs it.

## Why

`kubectl get ns argocd` says `Terminating` and nothing else. The real blocker is
a finalizer whose controller is gone, crashlooping, or waiting on a cloud API.
The usual fix is to force-patch the whole `finalizers` array off, which sometimes
leaks a load balancer or a disk that nobody notices for a month.

`k8s-finalizer-doctor` splits that into four different situations, because they
have four different fixes:

| Verdict | Means | Do |
| --- | --- | --- |
| `owner-missing` | no CRD, APIService or Deployment claims the finalizer's domain | strip it — the command is printed for you |
| `owner-unhealthy` | the owner exists but has 0 ready replicas, or its APIService is down | fix the controller; stripping hides a real outage |
| `owner-healthy` | the owner is up and still working | wait, or look at the controller's logs |
| `owner-builtin` | `kubernetes.io/pvc-protection` and friends, held by the control plane | remove the dependent object, not the finalizer |

## Install

```console
$ go install github.com/moveeeax/k8s-finalizer-doctor/cmd/k8s-finalizer-doctor@latest
```

## Usage

```console
$ k8s-finalizer-doctor --all-namespaces
OBJECT                            FINALIZER                               VERDICT          ACTION             OWNER
Namespace/argocd                  resources-finalizer.argocd.argoproj.io  owner-missing    strip              none
app Bucket/assets                 finalizer.gcp.crossplane.io             owner-healthy    wait               Deployment crossplane-system/crossplane-provider-gcp
app Certificate/web-tls           finalizer.cert-manager.io               owner-unhealthy  fix-controller     Deployment cert-manager/cert-manager
app ExternalScaler/scaler         webhook.example.com/cleanup             owner-unhealthy  fix-controller     APIService v1.webhook.example.com
app PersistentVolumeClaim/data-0  kubernetes.io/pvc-protection            owner-builtin    wait-on-dependent  control plane
app Service/web                   service.k8s.aws/resources               owner-missing    strip              none

Namespace/argocd  resources-finalizer.argocd.argoproj.io
  no CRD, APIService or Deployment matches "resources-finalizer.argocd.argoproj.io"; whatever set this finalizer is gone
  kubectl patch namespaces/argocd --type=json -p '[{"op":"test","path":"/metadata/finalizers/0","value":"resources-finalizer.argocd.argoproj.io"},{"op":"remove","path":"/metadata/finalizers/0"}]'

app Service/web  service.k8s.aws/resources
  no CRD, APIService or Deployment matches "service.k8s.aws"; whatever set this finalizer is gone
  warning: stripping may leak an AWS load balancer / target group
  kubectl patch services/web -n app --type=json -p '[{"op":"test","path":"/metadata/finalizers/0","value":"service.k8s.aws/resources"},{"op":"remove","path":"/metadata/finalizers/0"}]'

6 object(s) stuck: 2 safe to strip, 2 blocked on a broken controller, 2 still waiting
```

```console
$ k8s-finalizer-doctor --namespace staging --output json
$ k8s-finalizer-doctor --context prod --dump incident-4412.json
$ k8s-finalizer-doctor --snapshot incident-4412.json
```

### Flags

```
-A, --all-namespaces   scan every namespace (default)
-n, --namespace ns     restrict to one namespace (cluster-scoped objects are still shown)
-o, --output form      table (default) or json
    --snapshot file    read a snapshot instead of a cluster, for CI and postmortems
    --dump file        write the collected cluster state to file as a snapshot
    --kubeconfig path  explicit kubeconfig
    --context name     kubeconfig context to use
    --version          print version and exit
```

Exit codes: `0` nothing stuck, `1` at least one object blocked on a finalizer,
`2` the tool itself failed.

## How it works

1. Discovery lists every resource that supports `list`. Each one is listed once
   and filtered to objects that have both a `deletionTimestamp` and a non-empty
   `metadata.finalizers`.
2. For each finalizer, the domain is walked from longest to shortest suffix
   against installed CRD groups and APIService groups, so
   `resources-finalizer.argocd.argoproj.io` also tries `argocd.argoproj.io` and
   `argoproj.io`. Controllers routinely prefix their API group.
3. The domain's identifying labels are matched against Deployment and namespace
   names to find the workload that should be doing the cleanup, and its ready
   replica count decides `healthy` vs `unhealthy`.
4. Only when nothing at all matches does an object get `owner-missing` and a
   command.

The patch it prints carries a JSON Patch `test` op before the `remove`:

```json
[{"op":"test","path":"/metadata/finalizers/0","value":"..."},
 {"op":"remove","path":"/metadata/finalizers/0"}]
```

Without the `test`, a JSON Patch index is a race. If anything removes a
different finalizer between your `get` and your `patch`, index 0 is now a
different string and you delete the wrong one. The `test` op makes the API
server reject the patch instead.

## Snapshots

`--dump` writes everything the tool collected to a JSON file, and `--snapshot`
reads it back. That gets the cluster state off the incident call and into a file
you can diff, attach to a ticket, or drop into `testdata/` as a regression
fixture. See [`examples/`](examples/).

## Caveats

Step 3 is a heuristic. Finalizer domains are free-form strings and nothing in
the API links one to the controller that writes it, so a controller whose
Deployment name shares no label with its finalizer domain lands on
`owner-missing` when the CRD is also absent. That is why this tool prints
commands instead of running them, and why the cloud-resource warnings exist:
stripping `service.k8s.aws/resources` really does unblock the delete, and really
does leave the load balancer behind.

## License

MIT
