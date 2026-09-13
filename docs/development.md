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
other than its author. Five gates, in order, no exceptions:

1. **Branch** — one loop, one feature branch cut from current `develop`.
2. **Done** — `make test` and `make lint` pass, regeneration leaves no diff,
   and CI is green on the open merge request.
3. **Review** — evaluated by a different agent than the one that wrote the
   change. The reviewer works from the diff, verifies the Definition of Done
   independently rather than trusting it, and returns APPROVE or REQUEST
   CHANGES.
4. **Merge** — only on approval. The merge is what authorises the version.
5. **Validate** — automatic, after the merge. CI runs the integration suite
   against an ephemeral cluster, tags and publishes, then upgrades the
   published image onto the live `picard` cluster and proves it provisions a
   test `DevEnvironment` to Ready and tears it down again.

**Nothing before the merge touches a cluster.** A feature branch must not
create a cluster, apply a CRD, or deploy the operator anywhere — not to
`picard`, not to your own cluster as a gate, and not to a throwaway cluster
inside a CI runner. A pull request runs only the cluster-free layers: lint,
codegen, unit tests, envtest and a Dockerfile build. (`envtest` is not an
exception: it starts an API server and etcd as local binaries, with no
container runtime and no nodes.)

That is a deliberate trade. A change that breaks the reconciler now merges
before anything catches it, and the two post-merge failures differ:

- **`integration` fails** — `version` depends on it, so no version is cut and
  no image is published. `develop` carries a bad commit and nothing shipped.
- **`verify-picard` fails** — it runs *after* `publish`, so the version was
  already cut, the image pushed and `latest` moved. A bad build is on Docker
  Hub, and `picard` is left on the broken revision. No *release* is involved
  either way: releases are cut deliberately, and a release would simply not be
  cut from a bad commit.

Either way the fix goes forward through a normal loop, or the commit is
reverted.

`make test-integration` still works against whatever context you have selected.
It is a tool, not a gate: reach for it when you are changing reconciler
behaviour and want the feedback early, against a cluster of your own.

Nothing is tagged or released that did not come through an approved merge
request. The full rules live in `.claude/SKILL.MD`, which is authoritative.

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
# merge only on APPROVE — the merge cuts the next MINOR (v1.2.3 -> v1.2.4)

# promote develop to main, reviewed the same way. This READIES a release; it
# does not cut one. On merge, CI cuts the next MAJOR (v1.2.4 -> v1.3.0) and
# publishes an image — no chart, no install.yaml, no GitHub release.
gh pr create --base main --head develop --title "Release: promote develop to main" --fill

# then bring main back into develop (that merge cuts the next MINOR)
```

## Cutting a release

Promoting readies work; cutting a release ships it. Releases move the RELEASE
place and are the one step a human starts — a `workflow_dispatch` on
`release.yml`, from `main` only:

```bash
gh workflow run release.yml -f dry_run=true    # build everything, print the notes, change nothing
gh workflow run release.yml -f dry_run=false   # publish for real
```

`dry_run` defaults to **true**. Run it that way first: it builds every artifact
and prints the release notes it would publish, without tagging, pushing or
releasing anything.

The release publishes the multi-arch image and creates a GitHub release with
`install.yaml`, the chart tarball, the CRD on its own and a sample environment
attached — everything a user needs to install into a cluster of their own.
There is no chart registry: `helm install` takes the attached chart's URL
directly. Notes are generated from the pull
requests merged since the previous **release** tag, not the previous tag, since
every merge cuts one.

Two ordering properties worth knowing, because both cost a release number if
they are ever changed back:

- The version is tagged **last**, by the release-creation step itself. Tagging
  earlier meant a failed chart push left the number burned — a re-dispatch
  would compute the next release and the tag would stand with nothing behind
  it.
- The image and chart are pushed before that tag exists. A failure after them
  is recoverable: a re-dispatch recomputes the same version and republishes
  both from the same source — an equivalent rebuild, though at a new digest,
  since neither a multi-arch image nor a packaged chart is byte-reproducible.
  So in that one recovery path a version tag can move. Repeating a push beats
  burning a release number, and it only happens after a release has failed.

## The backlog

Tracked in [GitHub Issues](https://github.com/partofaplan/kado-operator/issues),
not in a file here — a checked-in backlog would put every idea through the full
loop described above, cutting a version and deploying to `picard` for a note to
self.

```bash
gh issue list
gh issue create --label enhancement --title "..." --body "..."
```

When a change defers something, open an issue before the reasoning is lost, and
reference it from the merge request.

Closing is not automatic: GitHub fires a closing keyword only on the **default
branch**, which is `main`, and feature work merges into `develop`. `Closes #14`
in a PR body records the link but leaves the issue open. Put it in a **commit
message** and it fires when that commit reaches `main` on the next promotion;
otherwise close it by hand.

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

## picard tracks develop

`picard` is upgraded automatically after a merge into `develop`, by the
`verify-picard` job in `ci.yml`, and the operator is **never uninstalled** —
each merge upgrades the release in place, which is also the path real users
take and catches migration failures a clean install hides. Only the test
`DevEnvironment` is temporary: it is created, asserted against, and deleted.

Not *every* merge, despite the name: `verify-picard` depends on `version` and
`publish`, so it is skipped whenever either is skipped, fails or is cancelled —
including
when three merges land in quick succession and the `version-assign` concurrency
group cancels the middle run. That commit gets no version, no image and no
validation.

So `picard` is not a cluster to keep anything on. Whatever is on `develop` is
what is running, and every validation run deletes its own test environment.

A failed validation leaves `picard` on the broken revision on purpose — a Helm
rollback would revert the failure before diagnostics could be taken, and then
report `deployed`. Merge the fix, or roll back by hand:

```bash
helm rollback kado-operator -n kado-operator-system --kube-context picard
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

### Deploying from CI

`deploy.yml` installs the chart onto a cluster using the **self-hosted runner**
(`mac-runner`, macOS/ARM64). Run it from the Actions tab or:

```bash
gh workflow run deploy.yml \
  -f image_tag=latest \
  -f namespace=kado-operator-system \
  -f kube_context=picard \
  -f dry_run=false
```

It is **`workflow_dispatch` only, deliberately**. This repository is public and
the runner is a physical Mac: a `pull_request` trigger would let anyone opening
a PR from a fork execute code on that machine. Do not add one.

Two things about that runner are load-bearing:

- **PATH.** A self-hosted runner does not get a login shell's environment. On
  this machine `kubectl`, `helm` and `docker` live in `~/.rd/bin` (Rancher
  Desktop), which a non-login zsh does not include, so the workflow adds it to
  `$GITHUB_PATH` before anything else. Without that, every step fails with
  "command not found".
- **Context.** The machine has both `picard` and `rancher-desktop` contexts.
  Every `kubectl` and `helm` call passes `--kube-context` explicitly so a
  deploy cannot land in the wrong cluster. Note the name: the kubectl context
  and the k3d cluster are both `picard`, while `k3d-picard` is only the
  kubeconfig cluster-entry name and the node-container prefix —
  `kubectl --context k3d-picard` fails with "no context exists".

The workflow preflights before touching the cluster: the context exists, the
image tag is actually published, and the CRD is not already owned by something
other than this Helm release (see below).

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
| `ci.yml` | pull requests into `develop`/`main`, and pushes to those two branches | **On a pull request:** lint, unit + envtest, generated-code check and a Dockerfile build (`linux/arm64` only — the cross-compiled target, since `Unit & envtest` already compiles natively on the same runner) — nothing that touches a cluster. **On a push to `develop`/`main`:** additionally the integration suite on an ephemeral cluster, then assign the version, tag and publish the image; on `develop` also upgrade `picard` and validate it. No release is published on any push — that is `release.yml`, dispatched deliberately |

Feature and hotfix branches are covered by the pull request trigger, which
tests the merge result rather than the branch tip. They are deliberately not in
the `push` trigger: listing both ran the whole pipeline twice for every commit
on an open pull request, in two concurrency groups that could not cancel each
other.

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
| push to a feature branch | none assigned | none published — no workflow runs at all until a pull request is open |
| merge into `develop` | MINOR increments (`v1.2.3` → `v1.2.4`) | `1.2.4`, `latest` |
| merge into `main` | MAJOR increments (`v1.2.4` → `v1.3.0`) | `1.3.0`, `latest` |
| release dispatch | RELEASE increments (`v1.3.0` → `v2.0.0`) | `2.0.0`, `latest`, plus the chart and the GitHub release |

Versions come from the highest existing `v*` tag, so the tags are the source
of truth — there is no VERSION file to drift. Never create a `v*` tag by
hand: CI counts from the highest one it finds, so a hand-made tag silently
reassigns every version after it.

Every image is built for `linux/amd64` and `linux/arm64`.

`latest` tracks whatever was published most recently, which — because versions
only ever increase — is the newest release on either branch. That includes
development builds, so `latest` is a convenience, not a stability channel: pin
the version tag or a digest whenever the build must be reproducible, such
as a pinned deployment, a bug report or a rollback. Each CI run prints the
digest it published in its job summary.

Nothing else is published. `sha-<short>` and floating branch tags were dropped:
every commit that reaches a publish already has a version of its own, so those
were a second and third name for one image, and three naming schemes for the
same artifact is three chances to deploy something other than what you meant.
Tags pushed under the old scheme remain on Docker Hub but are frozen.

CI needs one repository secret, `DOCKERHUB_TOKEN`: a Docker Hub access token
with Read/Write scope, never the account password. Create it at
<https://hub.docker.com/settings/security>. The username is not a secret — it
is the public `partofaplan` namespace already present in the image name — so
it lives in the workflows as a plain `DOCKERHUB_USER` env var. Pull request
runs never reach the publish job at all: `version` is gated on a push to
`develop` or `main`, so a pull request is validated and nothing is tagged or
published.

Only the "Create ephemeral cluster" step of the `integration` job in `ci.yml`
is provider-specific. Swapping K3D for Kind, or for a kubeconfig secret
pointing at a remote cluster, means editing that step and nothing else.

## Adding a new API

```bash
kubebuilder create api --group devenv --version v1alpha1 --kind <Kind>
make manifests generate helm-crds
```

Then wire the new reconciler into [`cmd/main.go`](../cmd/main.go) and add its
RBAC to both the markers and the chart.
