# Development workflow

## Prerequisites

| Tool | Version | Used for |
| --- | --- | --- |
| Go | 1.24+ | building and testing |
| Docker | any recent | building the operator image |
| kubectl | 1.29+ | talking to the cluster |
| A local cluster tool | — | optional: K3D 5.x, Kind, or minikube |
| Helm | 3.8+ | chart install, OCI push |
| kubebuilder | 4.x | scaffolding new APIs |

`golangci-lint`, `controller-gen`, `kustomize` and the envtest binaries are
downloaded into `bin/` on demand by the Makefile — no manual install needed.

## Branching

The repo follows Gitflow. `main` is always releasable; `develop` is the
integration branch.

```
feature/*  ──▶ develop ──▶ release/* ──▶ main (tagged)
hotfix/*   ──────────────────────────▶ main
```

The `git flow` CLI is optional — plain git works the same way:

```bash
# start a feature
git checkout develop && git pull
git checkout -b feature/my-change

# finish it
git checkout develop
git merge --no-ff feature/my-change
git branch -d feature/my-change

# cut a release
git checkout -b release/v0.2.0 develop
# bump Chart.yaml version/appVersion, update CHANGELOG
git checkout main && git merge --no-ff release/v0.2.0
git tag -a v0.2.0 -m "v0.2.0"      # pushing the tag triggers the release workflow
git checkout develop && git merge --no-ff release/v0.2.0
```

## The local loop

Everything here acts on the **current kubectl context**. The operator is
cluster-agnostic: K3D, Kind, minikube, Docker Desktop and remote clusters are
interchangeable, and nothing in the build, test or deploy path invokes a
provider CLI.

```bash
kubectl config current-context   # always confirm the target first

make test                 # unit + envtest, no cluster needed (~15s)
make lint                 # golangci-lint
make install              # install CRDs into the current context
make run                  # run the operator on your host against that cluster
```

Need a throwaway cluster? The optional helpers create one and select its
context:

```bash
make cluster-up                          # default LOCAL_PROVIDER=k3d
make cluster-up LOCAL_PROVIDER=kind      # or kind
make cluster-up LOCAL_PROVIDER=minikube  # or minikube
make cluster-down                        # tear it down
```

With `make run` going in one terminal, drive it from another:

```bash
kubectl apply -f config/samples/devenv_v1alpha1_devenvironment.yaml
kubectl get devenvironments -w
kubectl get all -n team-alpha
kubectl delete -f config/samples/devenv_v1alpha1_devenvironment.yaml
```

After changing `api/v1alpha1/*_types.go`, regenerate — CI fails if the
committed output is stale:

```bash
make manifests generate helm-crds
```

## Testing layers

| Layer | Command | Cluster | What it covers |
| --- | --- | --- | --- |
| Unit | `go test ./internal/...` | none | Reconcile logic against a fake client: every branch, error path and idempotency. |
| envtest | `make test` | none (local control plane) | The generated CRD schema — defaults, validation, immutability. |
| Integration | `make test-integration` | any (current context) | The real thing: pods becoming ready, PVCs binding, namespace teardown completing. |

Integration tests are behind a `//go:build integration` tag, so `go test ./...`
never reaches for a cluster.

envtest runs no kube-controller-manager or scheduler, which is why readiness
and namespace teardown are asserted in the integration suite rather than
there — in envtest nothing ever becomes Ready and a deleted namespace never
finishes terminating.

Because the integration suite targets whatever context is current, the same
tests validate a local cluster, the CI cluster, or a staging cluster. Running
them against a second distribution is how you prove portability:

```bash
kubectl config use-context <other-cluster>
make test-integration
```

## Deploying into the cluster

Running the image in-cluster, rather than `make run` on your host, is what
exercises RBAC:

```bash
# Build and side-load, skipping a registry round trip (local clusters only).
make cluster-load IMAGE_TAG=dev

helm upgrade --install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system --create-namespace \
  --set image.repository=docker.io/partofaplan/kado-operator \
  --set image.tag=dev \
  --set image.pullPolicy=Never

kubectl -n kado-operator-system logs -f deploy/kado-operator
```

On a remote cluster there is nothing to side-load — push the image and let the
cluster pull it:

```bash
make docker-push IMAGE_TAG=dev
helm upgrade --install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system --create-namespace \
  --set image.tag=dev
```

RBAC comes from two places that must agree: the `+kubebuilder:rbac` markers on
the reconciler (which generate `config/rbac/role.yaml`) and the ClusterRole in
`charts/kado-operator/templates/rbac.yaml`. Adding a permission means updating
both; `make run` will not catch the difference because it uses your kubeconfig.

## CI

| Workflow | Trigger | Does |
| --- | --- | --- |
| `ci.yml` | push to `develop`/`main`/`release/*`/`feature/*`, PRs | lint, verify generated files are current, unit + envtest with coverage, build and push an iteration image |
| `integration-test.yml` | push to `develop`/`main`/`release/*`, PRs | ephemeral cluster, install CRDs, build image, run the integration suite |
| `release.yml` | tag `v*` | test, build and push a multi-arch image to Docker Hub, push the chart to the OCI registry, publish a GitHub release |

### Container images

Images publish to [Docker Hub](https://hub.docker.com/repositories/partofaplan)
as `docker.io/partofaplan/kado-operator`:

| Trigger | Tags |
| --- | --- |
| push to a branch | `<branch>` (moving) and `sha-<short>` (immutable) |
| tag `v*` | `vX.Y.Z` and `latest`, multi-arch (amd64 + arm64) |

Pin `sha-<short>` when you need the exact build you tested. CI needs two
repository secrets, `DOCKERHUB_USERNAME` and `DOCKERHUB_TOKEN` — the latter a
Docker Hub access token with Read/Write scope, never the account password.
Pull requests from forks cannot read secrets, so those runs build without
pushing.

Only the "Create ephemeral cluster" step of `integration-test.yml` is
provider-specific. Swapping K3D for Kind, or for a kubeconfig secret pointing
at a remote cluster, means editing that step and nothing else.

## Adding a new API

```bash
kubebuilder create api --group devenv --version v1alpha1 --kind <Kind>
make manifests generate helm-crds
```

Then wire the new reconciler into [`cmd/main.go`](../cmd/main.go) and add its
RBAC to both the markers and the chart.
