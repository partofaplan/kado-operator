# DevEnvironment

`DevEnvironment` is the custom resource kado-operator reconciles. One
`DevEnvironment` owns one Kubernetes namespace and everything inside it.

- **API group / version:** `devenv.aviture.dev/v1alpha1`
- **Kind:** `DevEnvironment`
- **Scope:** Namespaced

The resource lives in a control namespace (for example `default`) and
provisions a *separate* namespace for the environment itself. Keeping the two
apart means deleting the environment namespace never takes the record with it,
and a single control namespace can hold every team's environment.

## Quick example

```yaml
apiVersion: devenv.aviture.dev/v1alpha1
kind: DevEnvironment
metadata:
  name: team-alpha
  namespace: default
spec:
  owner: team-alpha
  storage:
    size: 1Gi
  config:
    LOG_LEVEL: debug
  services:
    - name: postgres
      image: postgres:16-alpine
      port: 5432
      env:
        POSTGRES_USER: dev
        POSTGRES_PASSWORD: dev
        PGDATA: /var/lib/postgresql/data/pgdata
      mountPath: /var/lib/postgresql/data
```

A fuller example lives in
[`config/samples/devenv_v1alpha1_devenvironment.yaml`](../config/samples/devenv_v1alpha1_devenvironment.yaml).

## What gets created

For a `DevEnvironment` named `team-alpha`:

| Resource | Name | Created when |
| --- | --- | --- |
| Namespace | `team-alpha` (or `spec.namespaceName`) | always |
| ConfigMap | `team-alpha-config` | `spec.config` is non-empty |
| Secret | one per name in `spec.services[].secretRefs` | a service references it |
| PersistentVolumeClaim | `team-alpha-workspace` | `spec.storage` is set |
| Deployment | one per `spec.services[].name` | always |
| Service | one per `spec.services[].name` (ClusterIP) | always |

Every object carries these labels:

| Label | Meaning |
| --- | --- |
| `app.kubernetes.io/managed-by: kado-operator` | created by this operator |
| `devenv.aviture.dev/environment` | the owning `DevEnvironment` name |
| `devenv.aviture.dev/owner-namespace` | where that record lives |
| `devenv.aviture.dev/service` | the service within the environment |
| `devenv.aviture.dev/owner` | `spec.owner`, for cost attribution |

These labels are load-bearing, not decoration. A namespace cannot hold an
`ownerReference` back to a namespaced resource, so the operator uses the labels
both to find the `DevEnvironment` when a child object changes and to decide
what it is allowed to delete.

## Spec reference

### Top level

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `namespaceName` | string | `metadata.name` | Namespace to provision. **Immutable** — changing it would orphan the environment's data. |
| `owner` | string | — | Person or team the environment belongs to. Stamped on the namespace as a label. |
| `storage` | [StorageSpec](#storagespec) | — | Requests a shared volume for the environment. |
| `services` | [[]ServiceSpec](#servicespec) | — | Supporting services to deploy. Keyed by `name`; duplicates are rejected. |
| `config` | map[string]string | — | Injected as `<name>-config` and exposed to every service via `envFrom`. |

### StorageSpec

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `size` | Quantity | *required* | Requested capacity, e.g. `1Gi`. |
| `storageClassName` | string | cluster default | StorageClass backing the claim. |
| `accessModes` | []string | `[ReadWriteOnce]` | Access modes for the claim. |

The claim is created once and then left alone: PVC specs are largely immutable,
so resizing is a deliberate manual operation rather than something a spec edit
silently attempts.

### ServiceSpec

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `name` | string | *required* | DNS label, unique within the environment. Names the Deployment and Service. |
| `image` | string | *required* | Container image to run. |
| `port` | int32 | *required* | Container port, exposed through a ClusterIP Service. 1–65535. |
| `replicas` | int32 | `1` | Desired pod count. |
| `env` | map[string]string | — | Plain environment variables. Sorted before rendering, so a reconcile never churns the pod template. |
| `secretRefs` | []string | — | Secrets in the `DevEnvironment`'s **own** namespace, copied into the environment namespace and mounted via `envFrom`. |
| `mountPath` | string | — | Mounts the shared volume here. Requires `spec.storage`. |
| `resources` | ResourceRequirements | — | Compute resources for the container. |

Put credentials in `secretRefs`, not `env` — `env` values are stored in the
`DevEnvironment` spec in plain text and visible to anyone who can read it.

Services reach each other by name inside the namespace:
`postgres:5432`, `redis:6379`.

## Status

| Field | Description |
| --- | --- |
| `phase` | `Pending`, `Provisioning`, `Ready` or `Failed`. A summary for humans; conditions carry the detail. `Provisioning` does not imply healthy — check `Degraded`, which `kubectl get devenvironments` now shows as a column. |
| `namespace` | The namespace actually provisioned. |
| `readyServices` / `totalServices` | How many services have at least one ready replica. |
| `observedGeneration` | The spec generation this status was computed from. |
| `conditions` | `Available`, `Progressing`, `Degraded`. |

```console
$ kubectl get devenvironments
NAME         PHASE   NAMESPACE    READY   SERVICES   AGE
team-alpha   Ready   team-alpha   2       2          3m
```

## Lifecycle

**Creation.** The operator adds the `devenv.aviture.dev/finalizer` finalizer,
then creates the namespace and its contents. If a namespace of that name
already exists and is *not* labelled as managed by this same
`DevEnvironment`, reconciliation fails with `Degraded` rather than adopting
it — this is what stops a typo'd `namespaceName` from taking over `kube-system`.

**Updates.** Editing `spec.services` converges the namespace: entries added are
created, entries removed have their Deployment and Service deleted. Clearing
`spec.config` removes the ConfigMap.

**Deletion.** The finalizer deletes the environment namespace and is only
released once the namespace is gone, so the record remains visible while
teardown is in flight. A namespace the operator did not create is left
untouched and the finalizer is released immediately.

## Troubleshooting

Check conditions first — `phase` alone will not say why:

```bash
kubectl describe devenvironment team-alpha
```

> **A service that runs for a while before dying may briefly read as `Ready`.**
> The operator sets no readiness probes, and Kubernetes calls a running
> probe-less container ready the moment it starts. A container that crashes
> after a few seconds therefore alternates between ready and degraded until it
> settles into a backoff. A `readinessProbe` on the service spec would fix
> this; there is no field for one yet.

| Symptom | Likely cause |
| --- | --- |
| `Degraded=True`, "not managed by this DevEnvironment" | The target namespace already exists and belongs to something else. Pick a different `namespaceName`. |
| `Degraded=True`, "reading source secret" | A name in `secretRefs` does not exist in the `DevEnvironment`'s own namespace. |
| `Degraded=True`, reason `WorkloadUnhealthy` | A container cannot start. The message names the service, the blocking reason (`CrashLoopBackOff`, `ImagePullBackOff`, `Unschedulable`, …), the last exit code and the `kubectl logs` command that shows why. A missing required env var — `POSTGRES_PASSWORD`, say — lands here. |
| Stuck at `Provisioning` with `Degraded=False` | Pods are still coming up and nothing has gone wrong yet. If it persists, check image pull times, and whether a PVC is waiting for a consumer — a volume no service mounts stays `Pending` forever. |
| Stuck deleting | The namespace is still terminating, usually a finalizer on something inside it. `kubectl get ns <env> -o yaml`. |
| `helm install` rejects the CRD as not Helm-owned | The CRD was installed by `make install` (kustomize). See [CRD ownership](development.md#crd-ownership-make-install-vs-the-chart). |
