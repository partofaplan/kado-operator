# kado-operator

[![CI](https://github.com/partofaplan/kado-operator/actions/workflows/ci.yml/badge.svg)](https://github.com/partofaplan/kado-operator/actions/workflows/ci.yml)
[![Integration Tests](https://github.com/partofaplan/kado-operator/actions/workflows/integration-test.yml/badge.svg)](https://github.com/partofaplan/kado-operator/actions/workflows/integration-test.yml)
[![Docker Image](https://img.shields.io/docker/v/partofaplan/kado-operator?label=docker&sort=semver)](https://hub.docker.com/r/partofaplan/kado-operator)

A Kubernetes operator that provisions isolated development environments on
demand. Declare what a team needs — a namespace, some storage, a database, a
cache — and the operator builds it and tears it down again.

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
  services:
    - name: postgres
      image: postgres:16-alpine
      port: 5432
      mountPath: /var/lib/postgresql/data
    - name: redis
      image: redis:7-alpine
      port: 6379
```

```console
$ kubectl get devenvironments
NAME         PHASE   NAMESPACE    READY   SERVICES   AGE
team-alpha   Ready   team-alpha   2       2          30s
```

`kubectl delete devenvironment team-alpha` reclaims the whole namespace.

## What it does

Each `DevEnvironment` owns one namespace and everything in it:

- a namespace, labelled for ownership and cost attribution
- a ConfigMap built from `spec.config`, injected into every service
- copies of any Secrets the services reference
- an optional shared PersistentVolumeClaim
- a Deployment and ClusterIP Service per requested service
- teardown of all of it, via a finalizer, when the resource is deleted

Services reach each other by name: `postgres:5432`, `redis:6379`.

## Container image

Images are published to Docker Hub as
[`partofaplan/kado-operator`](https://hub.docker.com/r/partofaplan/kado-operator),
built for `linux/amd64` and `linux/arm64`.

Versions are `MAJOR.MINOR`. Merging into `develop` increments MINOR; promoting
`develop` to `main` increments MAJOR and resets MINOR to zero. CI assigns
every version — never tag by hand.

| Tag | Points at | Published by |
| --- | --- | --- |
| `latest` | the newest release | merge to `main` |
| `2.0` | that release | merge to `main` |
| `1.5` | one development build | merge to `develop` |
| `develop` / `main` | the tip of that branch | every merge to it |
| `sha-<short>` | one exact commit, never moves | every merge |

`MAJOR` here means *released*, not *breaking* — it marks the promotion of
`develop` into `main`. Breaking changes are called out in the release notes,
because the version number will not signal them.

```bash
docker pull partofaplan/kado-operator:latest    # newest release
docker pull partofaplan/kado-operator:develop   # newest development build
docker pull partofaplan/kado-operator:1.5       # one specific build
```

For anything reproducible — a pinned deployment, a bug report, a rollback —
use `sha-<short>` or a digest rather than a moving tag. Each CI run prints the
digest it published in its job summary.

## Install

```bash
helm install kado-operator oci://registry-1.docker.io/partofaplan/kado-operator \
  --namespace kado-operator-system --create-namespace
```

Or from a checkout:

```bash
helm install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system --create-namespace
```

To run a specific build — say the current tip of `develop`:

```bash
helm upgrade --install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system --create-namespace \
  --set image.tag=develop
```

Chart values are documented in
[`charts/kado-operator/values.yaml`](charts/kado-operator/values.yaml). The
image tag defaults to the chart's `appVersion`. The DevEnvironment CRD is
installed with the chart by default; set `installCRDs=false` where a platform
team owns CRDs separately.

Prefer plain manifests? Each release attaches a rendered `install.yaml`:

```bash
kubectl apply -f https://github.com/partofaplan/kado-operator/releases/latest/download/install.yaml
```

## Try it locally

The operator runs on any conformant cluster. Point kubectl at one — K3D, Kind,
minikube, Docker Desktop or a remote cluster — and everything below works the
same:

```bash
kubectl config current-context   # confirm the target
make install                     # install the CRD
make run                         # run the operator against it

# in another terminal
kubectl apply -f config/samples/devenv_v1alpha1_devenvironment.yaml
kubectl get devenvironments -w
```

If you need a throwaway local cluster, `make cluster-up` creates one
(`LOCAL_PROVIDER=k3d|kind|minikube`, default `k3d`).

## Build & release

```bash
make docker-build                  # build locally (tag with IMAGE_TAG=…)
make docker-push IMAGE_TAG=dev     # build and push to Docker Hub
make docker-buildx                 # multi-arch build and push
make build-installer               # render dist/install.yaml
```

The Dockerfile cross-compiles: its builder stage is pinned to
`$BUILDPLATFORM`, so a multi-arch build runs the Go toolchain natively once
per target rather than emulating it under QEMU.

Releases are not cut by hand. Merging an approved merge request from
`develop` into `main` is the release: CI assigns the next MAJOR, tags the
commit, publishes the multi-arch image and moves `latest`, pushes the chart to
the OCI registry, and creates a GitHub release with `install.yaml` and the
chart archive attached.

Every change reaches `develop` the same way — a feature branch, all test
layers green, then a merge request reviewed by someone other than its author.
See [the development workflow](docs/development.md) for the full set of gates.

CI needs one repository secret, `DOCKERHUB_TOKEN`: a Docker Hub access token
with Read/Write scope, never the account password. The username is not a
secret — it is the public `partofaplan` namespace already present in the image
name — so it lives in the workflow as a plain `DOCKERHUB_USER` env var. Pull
requests from forks cannot read secrets, so those runs build without pushing.

## Documentation

- [DevEnvironment reference](docs/devenvironment.md) — every field, what gets
  created, status and conditions, troubleshooting
- [Development workflow](docs/development.md) — branching, the local loop,
  testing layers, CI

## Testing

```bash
make test                    # unit + envtest, no cluster required
make test-integration        # full lifecycle against the current kubectl context
make test-integration-local  # ...or spin up a local cluster first
make lint
```

## License

Apache 2.0 — see [LICENSE](LICENSE).
