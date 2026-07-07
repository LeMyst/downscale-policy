# downscale-policy Helm chart

Deploys the DownscalePolicy CRD and the downscale-policy operator, which lets
namespace users manage the [kube-downscaler](https://github.com/caas-team/py-kube-downscaler)
schedule of their namespace through `DownscalePolicy` resources instead of
annotating the Namespace object directly.

## Install

```sh
helm install downscale-policy ./charts/downscale-policy \
  --namespace downscale-policy-system --create-namespace \
  --set image.repository=ghcr.io/lemyst/downscale-policy \
  --set image.tag=0.1.0
```

## CRD handling

The DownscalePolicy CRD is rendered as a regular templated resource (not from
the special `crds/` directory), so Helm installs **and upgrades** it with the
release:

- `crds.install=false` skips it entirely — install
  `files/crd/downscaler.io_downscalepolicies.yaml` yourself before deploying
  the operator.
- `crds.keep=true` (default) annotates the CRD with
  `helm.sh/resource-policy: keep`, so `helm uninstall` leaves the CRD — and
  every `DownscalePolicy` in the cluster — in place. Set it to `false` if you
  want uninstall to delete them.

If you upgrade a release that originally installed the CRD from the old
`crds/` directory, let Helm adopt it first:

```sh
kubectl label crd downscalepolicies.downscaler.io app.kubernetes.io/managed-by=Helm
kubectl annotate crd downscalepolicies.downscaler.io \
  meta.helm.sh/release-name=<release> meta.helm.sh/release-namespace=<namespace>
```

(or set `crds.install=false` to keep managing it by hand).

## Values

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | Manager replicas (leader election makes >1 safe) |
| `image.repository` | `ghcr.io/lemyst/downscale-policy` | Manager image |
| `image.tag` | `""` (chart appVersion) | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `imagePullSecrets` | `[]` | Pull secrets |
| `serviceAccount.create` | `true` | Create the manager ServiceAccount |
| `serviceAccount.name` | `""` | Override the ServiceAccount name |
| `serviceAccount.annotations` | `{}` | ServiceAccount annotations |
| `crds.install` | `true` | Install and upgrade the DownscalePolicy CRD with the release |
| `crds.keep` | `true` | Keep the CRD (and all DownscalePolicies) on `helm uninstall` |
| `rbac.create` | `true` | Create the manager RBAC (ClusterRole/Binding, leader-election Role) |
| `rbac.userRoles` | `true` | Create the editor/viewer ClusterRoles for namespace users |
| `rbac.aggregateToDefaultRoles` | `true` | Fold DownscalePolicy permissions into the built-in `edit`/`admin`/`view` ClusterRoles |
| `leaderElection.enabled` | `true` | Enable leader election |
| `metrics.enabled` | `false` | Expose controller-runtime metrics |
| `metrics.bindAddress` | `":8443"` | Metrics bind address when enabled |
| `metrics.secure` | `true` | Serve metrics over HTTPS |
| `extraArgs` | `[]` | Extra manager arguments |
| `resources` | requests 10m/64Mi, limits 500m/128Mi | Manager resources |
| `podAnnotations`, `podLabels`, `priorityClassName`, `nodeSelector`, `tolerations`, `affinity` | — | Standard scheduling knobs |

## RBAC model

- The **manager** gets a ClusterRole for `namespaces` (get/list/watch/update/patch),
  `downscalepolicies` (+status, +finalizers) and Events.
- **Namespace users** get `<release>-downscale-policy-editor` / `-viewer`
  ClusterRoles (`rbac.userRoles=true`, default). Set `rbac.userRoles=false`
  to skip them and manage user access to DownscalePolicies yourself.
- With `rbac.aggregateToDefaultRoles=true` (default), the user roles are
  folded into the built-in `edit`/`admin`/`view` ClusterRoles: any user who
  already has a namespace-scoped `edit`/`admin` RoleBinding can create and
  manage the `DownscalePolicy` of their namespace, and `view` users can read
  it — no extra bindings needed. Set it to `false` and bind the editor/viewer
  roles yourself for tighter control.
