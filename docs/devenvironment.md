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
[`config/samples/devenv_v1alpha1_devenvironment.yaml`](../config/samples/devenv_v1alpha1_devenvironment.yaml),
and [`examples/`](../examples/) has ready-to-apply suites for common setups —
Postgres or MySQL with a database UI, MongoDB, RabbitMQ, Prometheus and
Grafana, and a mail sandbox.

## What gets created

For a `DevEnvironment` named `team-alpha`:

| Resource | Name | Created when |
| --- | --- | --- |
| Namespace | `team-alpha` (or `spec.namespaceName`) | always |
| ConfigMap | `team-alpha-config` | `spec.config` is non-empty |
| Secret | one per name in `spec.services[].secretRefs` | a service references it |
| Secret | one per name in `imagePullSecrets` | the environment or a service references it |
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
| `registry` | string | — | Registry (and optional namespace path) for every service's image, e.g. `ghcr.io/myorg`. See [Registries](#registries). |
| `imagePullSecrets` | []string | — | docker-registry Secrets in the `DevEnvironment`'s **own** namespace, copied into the environment namespace and used to pull every service's image. See [Private registries](#private-registries). |
| `config` | map[string]string | — | Injected as `<name>-config` and exposed to every service via `envFrom`. |

### StorageSpec

| Field | Type | Default | Description |
| --- | --- | --- | --- |
| `size` | Quantity | *required* | Requested capacity, e.g. `1Gi`. |
| `storageClassName` | string | cluster default | StorageClass backing the claim. |
| `accessModes` | []string | `[ReadWriteOnce]` | Access modes for the claim. |
| `fsGroup` | int64 | — | Makes the volume group-owned by this GID so a non-root image can write to it. See [Non-root images and storage](#non-root-images-and-storage). |

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
| `registry` | string | — | Overrides `spec.registry` for this service. See [Registries](#registries). |
| `imagePullSecrets` | []string | — | Replaces `spec.imagePullSecrets` for this service. See [Private registries](#private-registries). |
| `readinessProbe` | Probe | — | Gates when the service counts as ready. A standard Kubernetes probe; leave the handler empty (`readinessProbe: {}`) for a TCP check against this service's own `port`. See [Readiness](#readiness). |

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

## Registries

By default an image is used exactly as written, so `redis:7-alpine` comes from
Docker Hub. Set `registry` to pull from somewhere else — a mirror, an internal
registry, or your own organisation:

```yaml
spec:
  registry: ghcr.io/myorg      # every service
  services:
    - name: redis
      image: redis:7-alpine    # -> ghcr.io/myorg/redis:7-alpine
    - name: postgres
      image: postgres:16-alpine
      registry: registry.internal:5000   # -> registry.internal:5000/postgres:16-alpine
```

A service's own `registry` wins over the environment's. Both accept a host with
an optional port and an optional namespace path — and also a bare name, which
is treated as a Docker Hub organisation. No scheme and no trailing slash; the
CRD rejects those at apply time rather than letting `https://ghcr.io/redis:7`
fail later at pull time. An explicit empty string means "unset", so a templated
`registry: {{ .Values.registry }}` with no value behaves as if the field were
absent.

A port, if given, must be a real TCP port — between 1 and 65535. The CRD
rejects `:0` and `:99999` at apply time for the same reason it rejects a
scheme: neither could ever pull, and failing on `kubectl apply` beats failing
later in `ImagePullBackOff`. Only the canonical spelling is accepted, so a
zero-padded `:0080` is rejected too, even though Docker's grammar allows it —
write `:80`.

An IPv6 literal such as `[::1]:5000` cannot be used in `registry`. It works
inside `image`, where it is recognised as a host, so pin the full reference on
the service instead.

**An image that already names a registry is left alone.** So a service pinned to
`quay.io/team/api:1` keeps it even when the environment sets a registry, and
there is no opt-out flag to remember:

```yaml
spec:
  registry: ghcr.io/myorg
  services:
    - name: api
      image: quay.io/team/api:1   # unchanged — it already names a host
```

That is also how one service opts out of an environment-wide registry: name the
host in the image. For Docker Hub, that means writing it out in full.

```yaml
spec:
  registry: ghcr.io/myorg
  services:
    - name: redis
      image: docker.io/library/redis:7-alpine   # stays on Docker Hub
```

The test for "already names a registry" is Docker's own, so it behaves the way
every other tool does: the first path segment is a host if it contains a dot or
a colon, is exactly `localhost`, or contains an uppercase letter — that last
one because a repository path may not contain uppercase, so `MYHOST/app:1` can
only be a host. Two consequences worth knowing:

- `bitnami/redis:7` is a Docker Hub **organisation**, not a host, so it does get
  prefixed — `ghcr.io/myorg/bitnami/redis:7`.
- Rewriting is prefix-only. There is no way to redirect `quay.io/team/api` to a
  mirror through this field, because only you know how your mirror lays those
  images out. Configure a registry mirror on the nodes for that.

Changing a registry rolls the affected services, as any image change does.

## Private registries

`registry` chooses **where** an image comes from; `imagePullSecrets` is how the
kubelet authenticates to it. Without one, a private registry fails with
`ImagePullBackOff`.

Name a docker-registry Secret that lives in the `DevEnvironment`'s **own**
namespace. It is copied into the environment namespace, because a pod cannot
reference a Secret in another namespace — the same thing `secretRefs` does:

```yaml
spec:
  registry: ghcr.io/myorg
  imagePullSecrets:
    - ghcr-creds            # every service
  services:
    - name: api
      image: api:1
    - name: vendor
      registry: registry.vendor.io
      imagePullSecrets:
        - vendor-creds      # this service only
      image: tool:2
```

Create the Secret the usual way, in the namespace the `DevEnvironment` lives in:

```bash
kubectl create secret docker-registry ghcr-creds \
  --docker-server=ghcr.io \
  --docker-username="$USER" \
  --docker-password="$TOKEN"
```

**A service's list replaces the environment's, it does not add to it** — the
same rule `registry` follows. A service pinned to a different private registry
names its own credentials and inherits nothing, which is almost always what you
want, since the environment's credentials would not work against that registry
anyway. To use both, name both on the service.

Every referenced Secret is copied, including ones only some services use. An
unused copy is harmless, and the alternative — working out which secrets survive
resolution — would mean the set changed whenever a service was added.

**The Secret must be of type `kubernetes.io/dockerconfigjson`** (or the legacy
`kubernetes.io/dockercfg` — both are what the kubelet itself accepts). Anything
else is rejected during reconcile, with the reason in the environment's
conditions.
Kubernetes itself accepts a wrong-typed Secret here and then silently ignores
it, so the only symptom would otherwise be an `ImagePullBackOff` that looks
exactly like a bad password.

## Non-root images and storage

A container that does not run as root cannot write the shared volume unless you
say which group owns it:

```yaml
spec:
  storage:
    size: 2Gi
    fsGroup: 65534          # the GID the image runs as
  services:
    - name: prometheus
      image: prom/prometheus:v3.1.0
      port: 9090
      mountPath: /prometheus
```

Kubernetes then chowns the volume to `root:<fsGroup>`, sets the group-write and
setgid bits, and adds the GID to the container's supplementary groups. Measured
inside the container on a k3s cluster, with and without the field:

| | pod `securityContext` | volume |
| --- | --- | --- |
| `fsGroup: 65534` | `{"fsGroup":65534}` | `owner=0 group=65534 mode=2777` |
| unset | none | `owner=0 group=0 mode=777` |

The `group` column is the part that matters. The `777` on the unset row is k3s
`local-path` being permissive — that is why a non-root image appears to work
there. A provisioner that presents `root:root 0755` gives the same `group=0`
with no group write, and the container cannot write at all.

Without it the volume is presented as the provisioner leaves it. That is
`root:root 0755` on most CSI drivers, EBS and GCE PD, and a non-root process
gets `permission denied` — Prometheus, for instance, panics at startup with
`Unable to create mmap-ed active query log`, and the environment sits in
`Provisioning` while the pod crash-loops.

**Images that start as root and chown their own data directory do not need
this** — `postgres`, `mysql` and `mongo` all do. Images that run as a fixed
non-root user do: Prometheus is `nobody` (65534), Grafana is uid 472.

The field applies only to services that actually mount the volume, so adding it
does not restart anything else. `fsGroup` is a pod-level setting and the shared
volume is meant to be mounted by one service, so it lives on `storage` rather
than per service.

**Why this is easy to miss:** a cluster whose StorageClass mounts the volume
0777 — k3s `local-path`, which both k3d and Rancher Desktop use by default —
works without it. The failure appears on the second cluster, not the first.

## Readiness

Without a probe, `readyServices` counts containers Kubernetes *started*, not
services that work. A probe-less container is ready the instant it starts, so a
database still replaying its WAL — or one about to crash — counts as ready.

The shortest useful form is an empty handler, which becomes a TCP check against
the port the service already declares:

```yaml
services:
  - name: postgres
    image: postgres:16-alpine
    port: 5432
    readinessProbe: {}          # TCP connect to 5432
```

An empty handler is filled in rather than passed through because Kubernetes
accepts a probe with no action and silently never runs it — `readinessProbe: {}`
would otherwise look like it was doing something while changing nothing.

Anything a Kubernetes probe supports works, and an explicit handler is used
verbatim:

```yaml
    readinessProbe:
      httpGet:
        path: /healthz
        port: 8080
      initialDelaySeconds: 5
      periodSeconds: 10
```

Adding or changing a probe rolls the service's Deployment, as any pod-template
change does. Omitting the field leaves behaviour exactly as it was.

A probe that never passes leaves the environment at `Provisioning` with
`Degraded=False` indefinitely — the container is running, so nothing is
*blocked*, it is simply not answering. That is reported honestly rather than as
a failure, but it does mean a probe aimed at the wrong port looks like a slow
start forever. `kubectl describe pod -n <env>` names the failing probe.

## Troubleshooting

Check conditions first — `phase` alone will not say why:

```bash
kubectl describe devenvironment team-alpha
```

> **A service with no `readinessProbe` may briefly read as `Ready` before
> dying.** Kubernetes calls a running probe-less container ready the moment it
> starts, so a container that crashes a few seconds in alternates between ready
> and degraded until it settles into a backoff. Set a
> [readinessProbe](#readiness) to make `Ready` mean the service is actually
> answering.

| Symptom | Likely cause |
| --- | --- |
| `Degraded=True`, "not managed by this DevEnvironment" | The target namespace already exists and belongs to something else. Pick a different `namespaceName`. |
| `Degraded=True`, "reading source secret" | A name in `secretRefs` or `imagePullSecrets` does not exist in the `DevEnvironment`'s own namespace. |
| `Degraded=True`, "named in imagePullSecrets but has type" | The Secret exists but is not a docker-registry Secret. Recreate it with `kubectl create secret docker-registry`. |
| `Degraded=True`, reason `WorkloadUnhealthy` | A container cannot start. The message names the service, the blocking reason (`CrashLoopBackOff`, `ImagePullBackOff`, `Unschedulable`, …), the last exit code and the `kubectl logs` command that shows why. A missing required env var — `POSTGRES_PASSWORD`, say — lands here. |
| Stuck at `Provisioning` with `Degraded=False` | Pods are running but not ready, and nothing is *blocked* — so this is not degraded yet. Check image pull times and whether a PVC is waiting for a consumer (a volume no service mounts stays `Pending` forever). A service that never becomes ready does not stay silent: once its Deployment passes `progressDeadlineSeconds` (10 minutes by default) it is reported as `Degraded=True`, reason `WorkloadUnhealthy`, message `Stalled: ...` (#15). |
| `Degraded=True`, message begins `Stalled:` | The container is running but has never passed its `readinessProbe` within the Deployment's progress deadline. Usually the probe is pointed at a port nothing serves — `readinessProbe: {}` targets the service's own `port`, so an explicit handler naming a different one is the common cause. `kubectl describe pod -n <env>` shows the failing probe. |
| Stuck deleting | The namespace is still terminating, usually a finalizer on something inside it. `kubectl get ns <env> -o yaml`. |
| `helm install` rejects the CRD as not Helm-owned | The CRD was installed by `make install` (kustomize). See [CRD ownership](development.md#crd-ownership-make-install-vs-the-chart). |
