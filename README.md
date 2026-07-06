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
for all values (RBAC aggregation, metrics, leader election, …). Helm installs
the CRD from the chart's `crds/` directory on first install but never
upgrades it — apply `charts/downscale-policy/crds/*.yaml` manually after CRD
changes.

### Kustomize

```sh
make install          # CRD only
make deploy           # CRD + RBAC + controller (edit the image in config/default/kustomization.yaml)
# or run locally against the current kubeconfig:
make run
```

On Windows, the same steps without make:

```powershell
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0 object paths=./api/v1
go run sigs.k8s.io/controller-tools/cmd/controller-gen@v0.19.0 rbac:roleName=manager-role crd paths=./api/v1 paths=./internal/controller output:crd:artifacts:config=config/crd/bases output:rbac:artifacts:config=config/rbac
kubectl apply -k config/default
```

### RBAC for namespace users

`config/rbac/downscalepolicy_editor_role.yaml` carries the
`rbac.authorization.k8s.io/aggregate-to-edit` and `aggregate-to-admin` labels:
anyone who already has a namespace-scoped `edit` or `admin` RoleBinding can
manage the `DownscalePolicy` of their namespace with no extra bindings (and
`view` users can read them via the viewer role). Remove the labels if you want
to bind the roles explicitly instead.

## Design decisions (vs. the initial CRD draft)

- **Status describes the policy, not the workloads.** `currentReplicas`,
  `lastScaled` and phases like `Downscaled`/`Excluded` were dropped: the
  actual scaling is done by kube-downscaler, and this operator would have to
  re-implement its whole schedule engine to report them truthfully. Instead
  the status reports what the operator actually knows: `phase`
  (`Active`/`Failed`), a standard `Ready` condition, and
  `observedGeneration`.
- **`gracePeriod` was removed** — `downscaler/grace-period` is only evaluated
  on workloads by py-kube-downscaler, not on namespaces, so the field would
  silently do nothing.
- **`exclude` is a boolean, not a timespan** (that is what kube-downscaler
  expects); `upscalePeriod`/`downscalePeriod` were added for parity with the
  full namespace annotation set; `downscaleReplicas` was renamed
  `downtimeReplicas` to match the annotation and accepts a percentage.
- **No default for `downtimeReplicas`.** Defaulting to `0` would force the
  annotation onto every namespace; unset means kube-downscaler's own default
  (0) applies.
- **Oldest policy wins, not "everything fails".** Failing *all* policies on
  conflict would let anyone break a namespace's schedule just by creating a
  second policy. With oldest-wins, a working policy can never be disturbed by
  a newer one.
- **CEL validation** requires at least one spec field, and `excludeUntil` is
  pattern-checked. Timespan strings are not regex-validated — the
  kube-downscaler grammar (weekday ranges, timezones, absolute ranges,
  comma-separated lists) is too rich for a useful pattern; invalid values are
  surfaced by kube-downscaler's own logs/events.

## Development

```sh
make test        # regenerate + fmt + vet + unit tests
go test ./...    # just the tests
```

The reconciler is covered by unit tests using the controller-runtime fake
client (`internal/controller/downscalepolicy_controller_test.go`), including
conflict election, drift reversion, spec changes, deletion cleanup and
promotion of the next policy.

## Project layout

Standard [kubebuilder](https://book.kubebuilder.io/) v4 layout:

```
api/v1/                  CRD Go types (+ generated deepcopy)
cmd/main.go              manager entrypoint
internal/controller/     reconciler + tests
config/                  kustomize manifests (crd, rbac, manager, default, samples)
```
