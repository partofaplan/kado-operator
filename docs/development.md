# Development workflow

## Prerequisites

| Tool | Version | Used for |
| --- | --- | --- |
| Go | 1.24+ | building and testing |
| Docker | any recent | building the operator image |
| kubectl | 1.29+ | talking to the cluster |
| K3D | 5.x | local cluster |
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

```bash
make test                 # unit + envtest, no cluster needed (~15s)
make lint                 # golangci-lint
make k3d-up               # create/select the K3D cluster
make install              # install CRDs into it
make run                  # run the operator on your host against the cluster
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
| Integration | `make test-integration` | K3D | The real thing: pods becoming ready, PVCs binding, namespace teardown completing. |

Integration tests are behind a `//go:build integration` tag, so `go test ./...`
never reaches for a cluster.

envtest runs no kube-controller-manager or scheduler, which is why readiness
and namespace teardown are asserted in the K3D suite rather than there — in
envtest nothing ever becomes Ready and a deleted namespace never finishes
terminating.

## Deploying into the cluster

Running the image in-cluster, rather than `make run` on your host, is what
exercises RBAC:

```bash
make docker-build IMG=kado-operator:dev
k3d image load kado-operator:dev -c picard

helm upgrade --install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system --create-namespace \
  --set image.repository=kado-operator \
  --set image.tag=dev \
  --set image.pullPolicy=Never

kubectl -n kado-operator-system logs -f deploy/kado-operator
```

RBAC comes from two places that must agree: the `+kubebuilder:rbac` markers on
the reconciler (which generate `config/rbac/role.yaml`) and the ClusterRole in
`charts/kado-operator/templates/rbac.yaml`. Adding a permission means updating
both; `make run` will not catch the difference because it uses your kubeconfig.

## CI

| Workflow | Trigger | Does |
| --- | --- | --- |
| `ci.yml` | push to `develop`/`main`/`release/*`/`feature/*`, PRs | lint, verify generated files are current, unit + envtest with coverage, build image |
| `integration-test.yml` | push to `develop`/`main`/`release/*`, PRs | K3D cluster, install CRDs, build image, run the integration suite |
| `release.yml` | tag `v*` | test, build and push a multi-arch image to GHCR, push the chart to the OCI registry, publish a GitHub release |

## Adding a new API

```bash
kubebuilder create api --group devenv --version v1alpha1 --kind <Kind>
make manifests generate helm-crds
```

Then wire the new reconciler into [`cmd/main.go`](../cmd/main.go) and add its
RBAC to both the markers and the chart.
