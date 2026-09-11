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

| Tag | Points at | Published by |
| --- | --- | --- |
| `latest` | the newest stable release | release of a `v*` tag |
| `1.2.3`, `1.2`, `1` | that release, its patch line, its major line | release of a `v*` tag |
| `develop` | the tip of `develop` — newest merged work | every push to `develop` |
| `main` | the tip of `main` | every push to `main` |
| `sha-<short>` | one exact commit, never moves | every branch push |

Prereleases (`v1.2.3-rc1`) publish their version tags but never move `latest`.

```bash
docker pull partofaplan/kado-operator:latest    # newest release
docker pull partofaplan/kado-operator:develop   # newest merged work
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

Releases are cut by tagging `main`:

```bash
git tag -a v0.1.0 -m "v0.1.0" && git push origin v0.1.0
```

That triggers the release workflow: run tests, push the multi-arch image and
its semver tags, publish the chart to the OCI registry, and create a GitHub
release with `install.yaml` and the chart archive attached.

CI needs two repository secrets, `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` —
the latter a Docker Hub access token with Read/Write scope, never the account
password. Pull requests from forks cannot read secrets, so those runs build
without pushing.

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
