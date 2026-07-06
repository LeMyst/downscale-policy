# downscale-policy Helm chart

Deploys the DownscalePolicy CRD and the downscale-policy operator, which lets
namespace users manage the [kube-downscaler](https://github.com/caas-team/py-kube-downscaler)
schedule of their namespace through `DownscalePolicy` resources instead of
annotating the Namespace object directly.

## Install

```sh
helm install downscale-policy ./charts/downscale-policy \
  --namespace downscale-policy-system --create-namespace \
  --set image.repository=ghcr.io/LeMyst/downscale-policy \
  --set image.tag=0.1.0
```

The CRD in `crds/` is installed automatically on first install.

## Upgrade note (CRD)

Helm never upgrades or deletes anything under `crds/`. After upgrading the
chart to a version with CRD changes, apply the CRD manually:

```sh
kubectl apply -f charts/downscale-policy/crds/downscaler.io_downscalepolicies.yaml
```

## Values

| Key | Default | Description |
|---|---|---|
| `replicaCount` | `1` | Manager replicas (leader election makes >1 safe) |
| `image.repository` | `ghcr.io/LeMyst/downscale-policy` | Manager image |
| `image.tag` | `""` (chart appVersion) | Image tag |
| `image.pullPolicy` | `IfNotPresent` | Pull policy |
| `imagePullSecrets` | `[]` | Pull secrets |
| `serviceAccount.create` | `true` | Create the manager ServiceAccount |
| `serviceAccount.name` | `""` | Override the ServiceAccount name |
| `serviceAccount.annotations` | `{}` | ServiceAccount annotations |
| `rbac.create` | `true` | Create manager + user RBAC |
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
- With `rbac.aggregateToDefaultRoles=true` (default), any user who already has
  a namespace-scoped `edit`/`admin` RoleBinding can create and manage the
  `DownscalePolicy` of their namespace, and `view` users can read it — no
  extra bindings needed. Set it to `false` and bind
  `<release>-downscale-policy-editor` / `-viewer` yourself for tighter control.
