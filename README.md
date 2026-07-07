# downscale-policy

A Kubernetes operator that lets namespace users manage the
[kube-downscaler](https://github.com/caas-team/py-kube-downscaler) schedule of
their namespace **without needing edit permission on the Namespace object**.

## The problem

kube-downscaler reads its namespace-wide configuration from `downscaler/*`
annotations on the `Namespace` object. Namespaces are cluster-scoped
resources: a user with an `edit`/`admin` RoleBinding inside a namespace still
cannot annotate the namespace itself. In practice only cluster admins can
change downscaling schedules — this operator removes that bottleneck.

## How it works

Users create a namespaced `DownscalePolicy` resource. The operator translates
its spec into the corresponding `downscaler/*` annotations on the Namespace
object and keeps them in sync:

```
User ──creates──▶ DownscalePolicy ──reconciled──▶ Namespace annotations ──read──▶ kube-downscaler
```

- **Enforcement** — the operator watches Namespace objects. If anyone edits or
  deletes a managed `downscaler/*` annotation by hand, it is reverted to what
  the active policy declares. Annotations that don't belong to the operator
  (including other `downscaler.io/*` or unrelated keys) are left untouched.
- **One policy per namespace** — the *oldest* policy (creation time, name as
  tie-breaker) is `Active`; any additional policy is marked `Failed` with a
  `Ready=False` / `ConflictingPolicies` condition, a warning Event, and is not
  applied. Deleting the active policy automatically promotes the next-oldest.
- **Cleanup** — a finalizer (`downscaler.io/finalizer`) guarantees the managed
  annotations are removed from the namespace when the active policy is
  deleted. Ownership is tracked with a `downscaler.io/managed-by` annotation
  on the namespace, so hand-overs and operator restarts are safe.

## Spec reference

Every field maps 1:1 to a namespace-level annotation supported by
py-kube-downscaler. All fields are optional, but at least one must be set
(enforced by CEL validation in the CRD).

| Field | Annotation | Value |
|---|---|---|
| `uptime` | `downscaler/uptime` | timespan |
| `downtime` | `downscaler/downtime` | timespan |
| `upscalePeriod` | `downscaler/upscale-period` | timespan |
| `downscalePeriod` | `downscaler/downscale-period` | timespan |
| `forceUptime` | `downscaler/force-uptime` | `"true"`/`"false"` or timespan |
| `forceDowntime` | `downscaler/force-downtime` | `"true"`/`"false"` or timespan |
| `downtimeReplicas` | `downscaler/downtime-replicas` | integer or percentage (`"20%"`) |
| `exclude` | `downscaler/exclude` | boolean |
| `excludeUntil` | `downscaler/exclude-until` | `2026-08-31` or RFC 3339 timestamp |

Timespans use kube-downscaler syntax, e.g. `Mon-Fri 08:00-19:00 Europe/Paris`,
`Sat-Sun 00:00-24:00 UTC`, absolute ranges, `always`/`never`, comma-separated
lists.

Example:

```yaml
apiVersion: downscaler.io/v1
kind: DownscalePolicy
metadata:
  name: office-hours
  namespace: team-a
spec:
  uptime: "Mon-Fri 07:30-19:30 Europe/Paris"
  downtimeReplicas: 0
```

```console
$ kubectl get dsp -n team-a
NAME           PHASE    UPTIME                            DOWNTIME   READY   AGE
office-hours   Active   Mon-Fri 07:30-19:30 Europe/Paris             True    1m
```

## Installation

### Helm (recommended)

```sh
helm install downscale-policy ./charts/downscale-policy \
  --namespace downscale-policy-system --create-namespace \
  --set image.tag=0.1.0
```

See [charts/downscale-policy/README.md](charts/downscale-policy/README.md)
for all values (CRD handling, RBAC, metrics, leader election, …). Notably:

- `crds.install=false` skips the CRD if you prefer to manage it yourself
  (by default it is installed and upgraded with the release, and kept on
  uninstall thanks to `crds.keep=true`).
- `rbac.userRoles=false` skips the user-facing editor/viewer ClusterRoles.

### Kustomize

```sh
make install          # CRD only
make deploy           # CRD + RBAC + controller (edit the image in config/default/kustomization.yaml)
```

or without make:

```sh
kubectl apply -k config/default
```

### RBAC for namespace users

With the Helm chart, the editor/viewer ClusterRoles are created by default
and aggregated into the built-in `edit`/`admin`/`view` roles; set
`rbac.userRoles=false` to skip them entirely, or
`rbac.aggregateToDefaultRoles=false` to bind them explicitly yourself.

With Kustomize, `config/rbac/downscalepolicy_editor_role.yaml` carries the
`rbac.authorization.k8s.io/aggregate-to-edit` and `aggregate-to-admin` labels:
anyone who already has a namespace-scoped `edit` or `admin` RoleBinding can
manage the `DownscalePolicy` of their namespace with no extra bindings (and
`view` users can read them via the viewer role). Remove the labels if you want
to bind the roles explicitly instead, or drop the files from
`config/rbac/kustomization.yaml` to not install them at all.
