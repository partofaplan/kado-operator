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
feature/*  ──MR──▶ develop ──MR──▶ main
hotfix/*   ────────────────MR──▶ main
```

Every arrow is a merge request, and every merge request is reviewed by someone
other than its author. Four gates, in order, no exceptions:

1. **Branch** — one loop, one feature branch cut from current `develop`.
2. **Done** — `make test`, `make test-integration` and `make lint` all pass,
   regeneration leaves no diff, and CI is green on the pushed branch.
3. **Review** — a merge request, evaluated by a different agent than the one
   that wrote the change. The reviewer works from the diff, verifies the
   Definition of Done independently rather than trusting it, and returns
   APPROVE or REQUEST CHANGES.
4. **Merge** — only on approval. The merge is what authorises the version.

Nothing is tagged or released that did not come through an approved merge
request. The full rules live in `.claude/SKILL.MD`.

The `git flow` CLI is optional — plain git works the same way:

```bash
# start a feature
git checkout develop && git pull
git checkout -b feature/my-change

# finish it
git checkout develop
git merge --no-ff feature/my-change
git branch -d feature/my-change

# finish it: Definition of Done first, then a reviewed merge request
gh pr create --base develop --fill
/code-review <pr-number>          # a DIFFERENT agent evaluates it
# merge only on APPROVE — the merge cuts the next MINOR

# cut a release: promote develop to main, reviewed the same way
gh pr create --base main --head develop --title "Release: promote develop to main" --fill
# on approval and merge, CI cuts the next MAJOR and publishes everything

# then bring main back into develop (this itself cuts MAJOR.1)
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

### CRD ownership: `make install` vs the chart

Two things can install the DevEnvironment CRD, and they do not share
ownership. `make install` applies it with kustomize; the chart templates it
(when `installCRDs=true`, the default) and stamps it with Helm's ownership
metadata. If kustomize installed it first, `helm install` refuses with:

```
invalid ownership metadata; label validation error:
missing key "app.kubernetes.io/managed-by": must be set to "Helm"
```

Pick one owner per cluster. To switch from the kustomize path to the chart:

```bash
make uninstall ignore-not-found=true   # drop the kustomize-owned CRD first
helm install kado-operator ./charts/kado-operator ...
```

Deleting the CRD deletes every DevEnvironment with it, so check
`kubectl get devenvironments -A` before you do this anywhere real. On a
cluster where a platform team owns CRDs, install the chart with
`installCRDs=false` and let them manage it.

### Image builds

The Dockerfile's builder stage is pinned to `--platform=$BUILDPLATFORM` and
cross-compiles via `GOARCH`. A multi-arch build therefore runs the Go
toolchain natively once per target instead of emulating a foreign toolchain
under QEMU — the difference between roughly a minute and many. Keep that
pragma if you edit the Dockerfile; `make docker-buildx` and both publishing
workflows depend on it.

RBAC comes from two places that must agree: the `+kubebuilder:rbac` markers on
the reconciler (which generate `config/rbac/role.yaml`) and the ClusterRole in
`charts/kado-operator/templates/rbac.yaml`. Adding a permission means updating
both; `make run` will not catch the difference because it uses your kubeconfig.

## CI

| Workflow | Trigger | Does |
| --- | --- | --- |
| `ci.yml` | pushes and PRs | lint, unit + envtest, generated-code check, integration on an ephemeral cluster; on `develop`/`main` also assign the version, tag, publish the image, and on `main` publish the release |

Everything lives in one workflow on purpose. A workflow cannot declare a
`needs` dependency on a different workflow, so with integration tests in their
own file `version` could not wait for them — a merge to `main` could tag and
publish while they were red. They now sit upstream of `version` in the same
run.

There is deliberately no tag-triggered release workflow. Tags are pushed by
`ci.yml` using `GITHUB_TOKEN`, and GitHub suppresses workflow triggers for
such pushes to prevent loops — a workflow listening on `push: tags: v*` would
look correct and never fire. Everything reacting to a new version therefore
lives in the run that creates the tag.

### Container images

Images publish to [Docker Hub](https://hub.docker.com/repositories/partofaplan)
as `docker.io/partofaplan/kado-operator`:

| Trigger | Version | Image tags |
| --- | --- | --- |
| push to a feature branch | none assigned | none published |
| merge into `develop` | MINOR increments (`v1.4` → `v1.5`) | `1.5`, `develop`, `sha-<short>` |
| merge into `main` | MAJOR increments (`v1.5` → `v2.0`) | `2.0`, `main`, `sha-<short>`, `latest` |

Versions come from the highest existing `v*` tag, so the tags are the source
of truth — there is no VERSION file to drift. Never create a `v*` tag by
hand: CI counts from the highest one it finds, so a hand-made tag silently
reassigns every version after it.

Every image is built for `linux/amd64` and `linux/arm64`.

`latest` deliberately tracks the newest *release*, not the tip of a branch, so
`docker pull ...:latest` cannot hand someone unreleased work. Use `develop`
for the newest merged work, and `sha-<short>` or a digest whenever the build
must be reproducible — a pinned deployment, a bug report, a rollback. Each CI
run prints the digest it published in its job summary.

CI needs one repository secret, `DOCKERHUB_TOKEN`: a Docker Hub access token
with Read/Write scope, never the account password. Create it at
<https://hub.docker.com/settings/security>. The username is not a secret — it
is the public `partofaplan` namespace already present in the image name — so
it lives in the workflows as a plain `DOCKERHUB_USER` env var. Pull requests
from forks cannot read secrets, so those runs build without pushing.

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
