---
name: kado-operator-sdlc
description: Build and iterate on the kado-operator (dev environment provisioning operator) using Gitflow, automated testing, and GitHub Actions CI/CD. Use this skill whenever working on the operator codebase—whether writing controllers in Go, setting up tests, configuring GitHub Actions, or deploying to any Kubernetes cluster. This skill covers the complete SDLC from feature branch to production readiness.
---

# KADO Operator Development Workflow

A structured approach to building a Kubernetes custom operator for automated dev
environment provisioning using Go, Gitflow branching, local cluster testing, and
GitHub Actions automation.

**Every rule in this document is stated exactly once.** If you are looking for a
rule and find only a link, follow the link — the target is authoritative and the
place you are standing is not a second copy that can drift out of agreement with
it. Source files (`Makefile`, `.github/workflows/*.yml`, `charts/`) are likewise
never reproduced here, only described and linked.

---

## Core Principle: Stay Cluster-Agnostic

**The operator must run on any conformant Kubernetes cluster.** K3D, Kind,
minikube, Docker Desktop, EKS, GKE, AKS and OpenShift are all just targets —
none of them is *the* target.

In practice this means:

- **Talk to clusters through kubectl contexts, never through a cluster provider's CLI.** Build and test steps use `kubectl`/`helm` against whatever context is current. `k3d`/`kind` commands belong only in optional convenience targets that create a cluster, never in the build, test, deploy or teardown path.
- **Never hardcode a cluster name.** No `picard`, no `kado-ci`. Where a name is unavoidable (a helper that creates a local cluster), make it an overridable variable.
- **Use no provider-specific Kubernetes features** — no K3s-only storage classes, no Traefik-specific ingress annotations, no `local-path` assumptions. If a test needs a StorageClass, use the cluster default.
- **Load images the portable way.** `k3d image load` and `kind load` exist for local speed only. CI and any real cluster pull from a registry.
- **Gate anything genuinely provider-specific** behind a documented variable with a portable default.

A good check: if a step would break by pointing `KUBECONFIG` at a remote EKS
cluster, it is too tightly coupled.

### Container Registry

Images are published to **Docker Hub** under the `partofaplan` namespace:

- Repository: <https://hub.docker.com/repositories/partofaplan>
- Image: `docker.io/partofaplan/kado-operator`

CI authenticates with a single `DOCKERHUB_TOKEN` repository secret (a Docker Hub
access token, not the account password). The Docker Hub *username* is not a
secret — it is the public namespace already embedded in every image reference —
so it stays a plain `DOCKERHUB_USER` env var in the workflow. Storing it as a
secret adds no protection and creates a silent failure mode: a misnamed or
missing username secret resolves to an empty string, and the login fails with a
confusing error.

---

## The Loop: Branch → Done → Review → Merge → Validate

Work proceeds in **loops**. One loop is one unit of change: a feature, a fix, a
refactor. Every loop follows the same five gates, in order, with no step skipped
and no step reordered. These are requirements, not suggestions.

```
     ┌─ 1. BRANCH ──────────────────────────────────────────┐
     │  cut feature/<name> from develop                      │
     └───────────────────────────┬───────────────────────────┘
                                 ▼
     ┌─ 2. DONE ────────────────────────────────────────────┐
     │  every cluster-free test layer passes; merge request  │
     │  opened; every CI check green                         │
     └───────────────────────────┬───────────────────────────┘
                                 ▼
     ┌─ 3. REVIEW ──────────────────────────────────────────┐
     │  evaluated by a DIFFERENT agent than the author       │
     └───────────────────────────┬───────────────────────────┘
                                 ▼
     ┌─ 4. MERGE ───────────────────────────────────────────┐
     │  only on approval → CI tags the version → publishes   │
     └───────────────────────────┬───────────────────────────┘
                                 ▼
     ┌─ 5. VALIDATE ────────────────────────────────────────┐
     │  AUTOMATIC, after the merge. Integration suite on an  │
     │  ephemeral cluster, then the published image is       │
     │  upgraded onto picard and proven to provision a test  │
     │  environment. Nothing to run by hand.                 │
     └───────────────────────────────────────────────────────┘
```

### Nothing before the merge touches a cluster

This is the rule that shapes everything below. **A feature branch must not
create a cluster, apply a CRD, or deploy the operator anywhere** — not to
`picard`, not to a colleague's cluster, and not to a throwaway cluster inside a
CI runner.

What a pull request runs is therefore exactly the cluster-free set: lint, code
generation, unit tests, envtest, and a Dockerfile build. `envtest` is not an
exception — it starts an API server and etcd as local binaries, with no
container runtime, no nodes and no kubeconfig pointing anywhere.

Everything that needs a real cluster happens **after** the merge, automatically,
in [Gate 5](#gate-5--automatic-post-merge-validation).

Know what this costs, because it is a deliberate trade and not a free one: a
change that breaks the reconciler now merges into `develop` before anything
catches it. What protects the line is that `version` is gated on the integration
job, so a failing post-merge run cuts **no version and publishes no image** —
`develop` is left with a bad commit and no release, and the fix goes forward
through a normal loop. Reverting is always available and is often the faster
answer.

`make test-integration` still exists and still works against whatever context
you have selected. It is a tool, not a gate — reach for it deliberately when you
are changing reconciler behaviour and want the feedback early, against a cluster
of your own.

### Gate 1 — One branch per loop

**Every change to the codebase starts on a feature branch cut from `develop`.**
No exceptions, including one-line fixes, documentation edits and changes made by
an AI assistant. `develop` and `main` are never committed to directly.

```bash
git checkout develop
git pull --ff-only
git checkout -b feature/<short-description>
```

Branch names describe the change, not the author or the tool:
`feature/env-provisioner-crd`, `feature/pvc-resize`,
`hotfix/nil-deref-on-delete`.

Why this is a hard rule rather than a preference: every push to `develop` cuts a
version, publishes an image, and upgrades `picard`. A commit made directly to
`develop` is therefore a release *and* a deployment, made without review, that
cannot be undone without burning a version number. The feature branch is where
work is reviewable and abandonable.

Feature branches are short-lived and single-purpose: one loop, one branch, one
merge request. Do not continue a finished loop's branch into the next loop, and
do not accumulate unrelated changes on one branch — a reviewer cannot
meaningfully approve a branch that does three things.

Work merges back through a pull request into `develop`. Only release preparation
moves `develop` into `main`. Both branches are protected and reject direct
pushes, so the rule is enforced by the forge rather than by discipline alone.

### Gate 2 — Definition of Done

A loop is **done** when the cluster-free tests pass and CI agrees. All of these
MUST be true before the change is offered for review:

| Check | Command | Proves |
| --- | --- | --- |
| Unit + envtest | `make test` | Reconcile logic and the generated CRD schema |
| Lint | `make lint` | No new correctness or style violations |
| Generated code current | `make manifests generate helm-crds` leaves no diff | Committed CRDs match the Go types |
| CI green | GitHub Actions, on the open merge request | It passes somewhere other than your machine |

"Tests pass" means you ran them and read the output. A loop is not done because
the change looks right, because it compiled, or because the tests were passing
before you started. Green locally but red in CI is **not done**.

Open the merge request once these pass:

```bash
gh pr create --base develop --fill
```

If any check cannot pass for a defensible reason, say so explicitly in the merge
request. Do not open one claiming done when it is not.

### Gate 3 — Independent agent review

Every merge into `develop` MUST go through a merge request. Every merge from
`develop` into `main` MUST go through a merge request. There are no direct merges
and no exceptions for small changes.

**The merge request MUST be evaluated by a different agent than the one that
wrote the change.** This is the central rule of this gate.

An agent reviewing its own work is not review. It re-derives the same assumptions
that produced the code, so the failure modes it missed while writing are exactly
the ones it will miss while reading. Worse, it already believes the change is
correct — it wrote it — so it reads to confirm rather than to falsify. A separate
agent starts from the diff with no attachment to the approach and no memory of
why a shortcut seemed reasonable at the time.

This gate carries more weight than it used to. It is now the **last** thing
standing between a change and `develop`, because nothing proves cluster
behaviour beforehand. A reviewer should read reconciler changes with that in
mind and say plainly when a change is one whose blast radius warrants running
`make test-integration` by hand before merging.

The reviewing agent MUST:

- Start from the **diff and the merge request description**, not from the
  authoring conversation. A fresh context is the point; inheriting the author's
  reasoning defeats it.
- **Independently verify the Definition of Done** rather than trusting the
  description. Run the tests. A merge request asserting "tests pass" is a claim
  to check, not evidence.
- Look for what the author could not see: unhandled error paths, missing test
  coverage for the change, assumptions that hold only on the author's cluster,
  breaking changes to the CRD schema, drift between RBAC markers and the chart.
- Return an explicit verdict — **APPROVE**, or **REQUEST CHANGES** with specific,
  actionable findings. "Looks good" is not a verdict.

The authoring agent MUST NOT approve its own merge request, and MUST NOT merge on
the strength of its own assessment.

In Claude Code, spawn the reviewer as a subagent with its own context, or run the
review in a separate session:

```bash
/code-review                       # reviews the current diff
/code-review <pr-number>           # reviews a specific merge request
```

Blocking findings are fixed **on the same feature branch**, and the merge request
is re-reviewed. A finding is not resolved by arguing it away in a comment; either
change the code or explain in the merge request why the finding does not apply,
and let the reviewer decide.

### Gate 4 — Merge, tag, publish

Only an approved merge request may be merged. The merge is what authorises the
version: merging into `develop` cuts the next MINOR, merging into `main` cuts the
next MAJOR, and CI does the tagging (see [Versioning](#versioning-majorminor)).
Nothing is tagged or released that did not pass an independent review.

### Gate 5 — Automatic post-merge validation

Everything that needs a real cluster lives here, and **none of it is run by
hand.** Merging into `develop` triggers, in order:

1. **The integration suite**, against an ephemeral cluster the CI runner creates
   and destroys. `version` depends on this job, so a failure means no tag and no
   image.
2. **`version` and `publish`** — the commit is tagged and the image pushed.
3. **`verify-picard`** — on the self-hosted runner, the *published* image is
   upgraded onto `picard` and proven to work.

Validating the published tag rather than a locally built image is deliberate:
what gets proven is the exact artifact a user would install. The cost is that a
bad version is published before it is validated, which is acceptable because the
tag is immutable and cheap, and the alternative proves something other than what
shipped.

**The operator is never uninstalled.** `picard` tracks `develop`: each merge
upgrades the release in place, which is also the path real users take and
catches migration failures a clean install hides. Only the *test environment* is
temporary — it is created, asserted against, and deleted.

What `verify-picard` asserts, with a timeout:

- the namespace is created
- a Deployment and Service exist for **every** service in the spec (two)
- the `<env>-config` ConfigMap exists and carries the spec's `config` values
- the PVC binds
- `status.phase` reaches `Ready` with `readyServices == totalServices`
- `Available` is true and `Degraded` is false
- deleting the resource reclaims the namespace and releases the finalizer

On failure it captures `helm history`, `helm status`, operator logs selected
**by label** (`logs deploy/...` picks a pod arbitrarily and usually prints the
old healthy one), pod descriptions, and events scoped to both namespaces — all
before the test environment is torn down, because the namespace, its pods and
its events do not survive teardown.

It deliberately does **not** pass `--rollback-on-failure` (or Helm 3's
`--atomic`). Rolling back is right for a deploy and wrong for a validation: it
reverts the failure before diagnostics can be taken, and afterwards `helm status`
reports `deployed`, actively hiding what it just recovered from. A failed upgrade
therefore leaves `picard` on the broken revision on purpose. `helm rollback` by
hand is the escape hatch.

**This runs on `develop` only.** A promotion of `develop` into `main` contains
nothing that did not already pass it, and wiring a disposable dev cluster into
the release path would make releases fail for reasons unrelated to the release.

**`picard` is a shared test cluster, and this job is serialised.** Two runs would
fight over the same release and delete each other's test environment. It also
means `picard` is not a cluster to keep anything you care about on: whatever is
on `develop` is what is running.

**A hotfix merged straight to `main` is never validated on a cluster.** That is
the one real hole in this arrangement, and it is not an oversight to work around
quietly — a hotfix reaches `main` without passing through `develop`, so no
automated job ever exercises it in a cluster. Run `make test-integration` by hand
against a cluster of your own before merging one, and say in the merge request
that you did.

### What never to do

This is the complete list.

- **Never commit directly to `develop` or `main`,** including fast-forward merges.
- **Never open a merge request before the Definition of Done is met.**
- **Never deploy the operator or apply a CRD from a feature branch,** to any
  cluster, including a throwaway one.
- **Never keep anything you care about on `picard`.** It tracks `develop`
  automatically and its test environments are deleted on every run.
- **Never review your own change.** If you wrote it, a different agent reviews it.
- **Never merge an unreviewed or change-requested merge request.**
- **Never create a `v*` tag by hand.** CI derives the next version from the
  highest existing tag; a hand-made tag silently reassigns everything after it.
- **Never delete or move a published tag.** Images are already pushed against it;
  the tag and the image would disagree.
- **Never tag or release a commit that did not come through an approved merge
  request.**


## Versioning: MAJOR.MINOR

Versions are `MAJOR.MINOR`. There is no patch component.

| Event | Effect | Example |
| --- | --- | --- |
| Push to `develop` | MINOR increments | `v1.4` → `v1.5` |
| `develop` merged to `main` | MAJOR increments, MINOR resets to 0 | `v1.5` → `v2.0` |

So a cycle reads: `v0.1`, `v0.2`, `v0.3` on `develop`, then `v1.0` when that work
reaches `main`, then `v1.1`, `v1.2` on `develop`, then `v2.0`, and so on.

**MAJOR here means "released", not "breaking".** This is a deliberate departure
from SemVer: a major bump marks the promotion of `develop` into `main`, whatever
the change contained. Communicate breaking changes in the release notes, because
the version number will not signal them.

### Who assigns versions

GitHub Actions does, never a human. The version is derived from the highest
existing `v*` git tag, so the tags themselves are the source of truth — there is
no VERSION file to drift, and no commit written back to a branch.

- Push to `develop` → tag `v<MAJOR>.<MINOR + 1>`
- Push to `main` → tag `v<MAJOR + 1>.0`

### Image tags: the version, and `latest`

**The registry carries exactly two kinds of tag.** A published image is tagged
with its `MAJOR.MINOR` version, and `latest` moves to whatever was published most
recently — which, because versions only ever increase, is always the newest
release on either branch.

| Tag | Points at | Moves |
| --- | --- | --- |
| `MAJOR.MINOR` (e.g. `1.5`, `2.0`) | one exact release, forever | never |
| `latest` | the most recently published version | every publish |

Nothing else is published. There are deliberately **no `sha-<short>` tags and no
floating branch tags** (`develop`, `main`): every commit that reaches a publish
already has a version of its own, so a per-commit tag is a second name for a
thing that already has one, and a branch tag is a worse-specified alias for
`latest`. Three naming schemes for the same image is three chances to deploy
something other than what you meant.

**Pin the `MAJOR.MINOR` tag, or a digest, for anything reproducible.** `latest`
is a convenience for "give me the newest thing", and it deliberately follows
development builds as well as promotions — it is not a stability channel.

Nothing builds a `sha-` tag any more, locally or otherwise: [Gate 5](#gate-5--automatic-post-merge-validation)
deploys the published `MAJOR.MINOR` tag, and no other stage puts an image on a
cluster.

### Two consequences worth knowing

**Helm charts need a patch component, and `appVersion` must name a real image
tag.** Helm requires full SemVer2, so `0.2` is rejected as a chart version.
Charts are published as `MAJOR.MINOR.0` (`v0.2` → chart `0.2.0`).

`appVersion` is set to the **unprefixed** `0.2`, not `v0.2`. The chart defaults
`image.tag` to `.Chart.AppVersion`, and the published image tag is `0.2` — a
`v`-prefixed `appVersion` renders an image reference that was never pushed, and
every `helm install` fails with `ImagePullBackOff`. Whenever you change how
versions are named, render the packaged chart and check the image reference
against the tags the publish job actually pushes.

**A tag pushed by CI does not trigger another workflow.** GitHub suppresses
workflow triggers for pushes made with the default `GITHUB_TOKEN`, to prevent
loops. A separate release workflow listening on `push: tags: v*` would therefore
never fire, and would sit there looking correct. Everything that reacts to a new
version — image tags, chart publish, GitHub release — must live in the same run
that creates the tag.

---

## Phase 1: Local Development Setup

### Prerequisites

- Go 1.21+ installed locally
- **Any Kubernetes cluster** reachable via a kubectl context — K3D, Kind, minikube, Docker Desktop, or a remote cluster
- kubebuilder v3.11+ (for scaffolding CRD boilerplate)
- Docker Desktop or equivalent (for building operator images)
- Helm 3.8+ (for chart packaging and OCI push)

### Pick a cluster

Any provider works; nothing downstream depends on the choice. `make cluster-up`
creates one for you — see [Phase 4](#phase-4-makefile-targets) — or select an
existing context by hand:

```bash
kubectl config use-context my-remote-cluster
```

Confirm which cluster you are about to act on, every time:

```bash
kubectl config current-context
```

### Generate operator scaffold (if needed)

```bash
kubebuilder init --domain aviture.dev --repo github.com/partofaplan/kado-operator
kubebuilder create api --group devenv --version v1alpha1 --kind DevEnvironment
```

---

## Phase 2: Writing Controllers in Go

### Controller patterns for the DevEnvironment CRD

The operator watches `DevEnvironment` custom resources and manages:

- Namespace provisioning
- PVC creation (storage)
- Deployment of supporting services (databases, caches, queues)
- Secret/ConfigMap injection
- Cleanup on CR deletion

### Key files to iterate on

- `api/v1alpha1/devenvironment_types.go` — CRD schema
- `internal/controller/devenvironment_controller.go` — reconciliation logic
- `config/samples/devenv_v1alpha1_devenvironment.yaml` — example CRs for testing

### Portability rules for controller code

- **Do not assume a StorageClass.** Leave `storageClassName` unset to use the cluster default, and let the CRD expose it for callers who need a specific one.
- **Do not assume a default ingress controller or LoadBalancer.** ClusterIP Services are portable; anything else is opt-in.
- **Do not assume a node topology.** Single-node K3D and a 200-node EKS cluster must both work.
- **Watch by label, not by assumption.** Cross-namespace children cannot carry an `ownerReference`, so map them back with labels.

### Iterating against a live cluster

`make run` runs the operator on your host against the current context, which is
the fastest edit-test cycle. Be aware of what it hides: it uses *your* kubeconfig
credentials, so missing RBAC in the chart's ClusterRole does not show up until
[Gate 5](#gate-5--automatic-post-merge-validation) runs the real pod after the merge.

```bash
kubectl config current-context   # always confirm the target first
make install                     # CRDs into that cluster
make run
```

---

## Phase 3: Testing Strategy

Three layers, each with a different cost and a different thing it can prove.

### Unit tests (fast, no cluster)

Written in `internal/controller/*_test.go`:

- Use a fake client for reconciliation logic
- Test every path (success, error, edge cases) and idempotency
- Runs anywhere, including a laptop with no Docker

```go
// internal/controller/devenvironment_controller_test.go
func TestDevEnvironmentReconciliation(t *testing.T) {
    ctx := context.Background()
    client := fake.NewClientBuilder().Build()
    reconciler := &DevEnvironmentReconciler{Client: client}

    req := reconcile.Request{
        NamespacedName: types.NamespacedName{Name: "test-env", Namespace: "default"},
    }

    result, err := reconciler.Reconcile(ctx, req)

    assert.NoError(t, err)
    assert.Equal(t, ctrl.Result{}, result)
}
```

### envtest (local control plane, no cluster)

`envtest` starts a real API server and etcd without a full cluster. Use it to
prove the **generated CRD schema** behaves: defaults, validation, immutability
(`XValidation`) rules.

Note its limits: envtest runs no kube-controller-manager and no scheduler.
Nothing ever becomes Ready, and a deleted namespace never finishes terminating.
Assert those in the integration layer instead.

### Integration tests (any real cluster)

Written in `test/integration/` — run against **whatever cluster the current
kubectl context points at**. That is deliberately not tied to a provider: the
same suite validates a local K3D cluster, a CI cluster, or a staging EKS cluster.

Use these for the things only a real cluster does: pods actually becoming Ready,
PVCs binding, namespace teardown completing.

**In CI this layer runs only after a merge**, never on a pull request — see
[Nothing before the merge touches a cluster](#nothing-before-the-merge-touches-a-cluster).
Locally it is yours to run whenever it would help.

Guard them with a build tag so `go test ./...` never reaches for a cluster:

```go
//go:build integration
```

### End-to-end (GitHub Actions)

After a merge into `develop`, CI runs the integration suite against an ephemeral
cluster it creates itself, and then upgrades the published image onto `picard`
and proves it provisions a real environment. See
[Gate 5](#gate-5--automatic-post-merge-validation) and
[Phase 5](#phase-5-github-actions-cicd-pipeline).

---

## Phase 4: Makefile targets

**The `Makefile` is authoritative; run `make help` for the current list.** The
table below describes what each group is *for* — it is not a copy of the file and
will not be kept in sync line by line.

### Cluster-agnostic — acts on the current kubectl context

| Target | Purpose |
| --- | --- |
| `test` | Unit + envtest. Regenerates manifests first, then runs with `KUBEBUILDER_ASSETS` set |
| `test-integration` | Integration suite against the current context. Not a gate — see [Nothing before the merge touches a cluster](#nothing-before-the-merge-touches-a-cluster). Depends on `install`, so it leaves a kustomize-applied CRD behind; run `make uninstall` before pointing Helm at the same cluster |
| `lint`, `lint-fix`, `lint-config` | golangci-lint |
| `manifests`, `generate`, `helm-crds` | Code generation. Together these must leave no diff (Gate 2) |
| `helm-lint` | Lint and render the chart |
| `build`, `run` | Build or run the manager on your host |
| `docker-build`, `docker-push`, `docker-buildx` | Image build; `docker-buildx` is the multi-arch one |
| `install`, `uninstall`, `deploy`, `undeploy`, `build-installer` | kustomize paths into the current context |

### Local cluster conveniences

Optional, and nothing in the build, test or deploy path depends on them. Select a
provider with `LOCAL_PROVIDER=k3d|kind|minikube` and a name with `LOCAL_CLUSTER`.

| Target | Purpose |
| --- | --- |
| `cluster-up`, `cluster-down` | Create/delete a throwaway local cluster |
| `cluster-load` | Build and side-load the image, skipping a registry round trip |
| `test-integration-local` | `cluster-up` then `test-integration` |

### Image coordinates

`REGISTRY`, `IMAGE_NAME` and `IMAGE_TAG` compose `IMG`. Override `IMAGE_TAG` to
build and side-load something other than the default into a local cluster of
your own:

```bash
make cluster-load LOCAL_CLUSTER=my-dev IMAGE_TAG="$(git rev-parse --short HEAD)"
```

---

## Phase 5: GitHub Actions CI/CD Pipeline

**`.github/workflows/ci.yml` is authoritative. Read it before changing CI.** This
section records the *decisions* behind it — the things a reader would otherwise
undo, not knowing why they were done.

**Required repository secret:**

| Secret | Value |
| --- | --- |
| `DOCKERHUB_TOKEN` | A Docker Hub access token with Read/Write scope |

Create the token at <https://hub.docker.com/settings/security> — never use the
account password. On why the username is not alongside it, see
[Container Registry](#container-registry).

### What the workflow does

One workflow owns the whole line. It validates every merge request, and on
`develop` and `main` it additionally assigns the next version, tags the commit,
and publishes. Jobs: `lint`, `generated`, `test`, `image`, `integration`, then
`version` → `publish` → `release`.

### Decisions that look wrong until you know why

**Integration is a job in `ci.yml`, not a workflow of its own.** A workflow
cannot declare a `needs` dependency on a *different* workflow. With integration
tests in their own file, `version` could not wait for them, so a merge to `main`
could tag, publish and release while integration tests were still running or
already red. The Definition of Done requires them to pass, so they sit upstream
of `version` in the same run.

**There is no `release.yml` triggered by `push: tags: v*`,** for the reason given
under [Versioning](#two-consequences-worth-knowing).

**The `integration` job is `push`-only, and `verify-picard` exists at all.**
Together these implement
[Nothing before the merge touches a cluster](#nothing-before-the-merge-touches-a-cluster):
a pull request runs `lint`, `generated`, `test` and `image`, none of which
create a cluster or apply a CRD, and every cluster-touching stage moved behind
the merge. `version` still depends on `integration`, so a post-merge failure
cuts no version and publishes no image.

**`verify-picard` runs after `publish`, not before `version`.** It deploys the
published tag, so what is validated is the exact artifact a user would install
rather than a locally built lookalike. It is also the only job on a self-hosted
runner in this workflow, and it is reachable only from a `push` to `develop` —
which can only happen through a reviewed, merged pull request. No fork code and
no unreviewed code ever executes on that machine. Do not add a `pull_request`
trigger to it.

**Only `develop` and `main` are in the `push` trigger.** Feature and hotfix
branches are covered by the `pull_request` trigger instead. Listing both meant
every commit on an open merge request ran the whole pipeline twice — once for the
push and once for the pull request — in two different concurrency groups, so
neither cancelled the other. The `pull_request` trigger is also the better of the
two, because it tests the merge result rather than the branch tip.

**`develop`/`main` runs are never cancelled by newer ones.** They assign a
version and publish; a half-finished publish is worse than a slow queue.
Everything else is cancellable.

**The `version` job is serialised in its own concurrency group.** Two runs
computing "the next version" concurrently would race to push the same tag. Be
aware of the cost: GitHub keeps only ONE run pending per group, so with three
merges in quick succession the middle run is cancelled and that commit gets no
version and no image. Space merges out, or re-run the cancelled one.

**The tag is pushed before the image is built.** If `publish` then fails, the tag
exists with no image behind it. Recover with "Re-run failed jobs" — a full re-run
aborts on "tag already exists".

**Codegen runs in the `generated` job, and CI test jobs skip it.** `make test`
and `make test-integration` depend on `manifests generate fmt vet` so that a
developer running them locally cannot test stale code. In CI that is three jobs
doing the same regeneration in parallel with the one job whose entire purpose is
to check it, so CI calls the `*-only` variants instead. `go vet` is not lost:
golangci-lint runs the `govet` linter.

**`bin/` is cached, with a per-job key.** `controller-gen`, `kustomize` and the
envtest control-plane binaries are `go install`ed, and the Go module cache does
not cover installed binaries — without this they were re-downloaded on every job
of every run. The key includes `github.job` because each job populates `bin/`
with a different subset and `actions/cache` does not save on a hit: under one
shared key the first job to miss froze its partial `bin/` for every other job,
which then kept downloading what it was missing.

---

## Phase 6: Gitflow — release and hotfix procedures

The day-to-day loop is [The Loop](#the-loop-branch--done--review--merge--validate).
This section covers only the two procedures that loop does not describe.

### Promoting develop to main

When a line of work is ready, promote `develop` to `main` — through a merge
request, reviewed by a separate agent, exactly like a feature. A promotion is a
release; it gets *more* scrutiny than a feature, not less. There is no version to
bump by hand and no release branch to prepare — CI computes the MAJOR.

```bash
gh pr create --base main --head develop \
  --title "Release: promote develop to main" --fill
/code-review <pr-number>        # a different agent evaluates it
# merge only on APPROVE
```

That push tags `v2.0`, publishes `partofaplan/kado-operator:2.0`, moves `latest`,
pushes the chart as `2.0.0`, and creates the GitHub release.

Then bring `main` back into `develop` so the branches do not diverge — through a
merge request, like everything else:

```bash
git checkout -b chore/back-merge-main develop
git merge --no-ff main
git push -u origin chore/back-merge-main
gh pr create --base develop --title "Back-merge main into develop" --fill
```

`develop` is protected and rejects direct pushes, so a plain
`git push origin develop` does not work. It also matters for a second reason: a
hotfix reaches `develop` only through this back-merge, and pushing directly would
carry hotfix code into `develop` with no review and no verification.

Merging that request cuts `v2.1`. This is expected: the first development build
after a release is `MAJOR.1`.

### Hotfixes

A production fix still goes through a branch, but branches from `main`:

```bash
git checkout -b hotfix/nil-deref-on-delete main
# fix, meet the Definition of Done, open an MR into main,
# have a separate agent review it, then merge
```

Urgency does not waive any gate. A hotfix is the change most likely to be written
quickly and least likely to have been thought through.

It is also the one path no automated cluster validation covers: [Gate 5](#gate-5--automatic-post-merge-validation) runs on
`develop` only, and a hotfix never goes near `develop`. Run
`make test-integration` by hand against a cluster of your own before merging one,
and record in the merge request that you did.

Merging it to `main` cuts the next MAJOR, because `main` only ever produces MAJOR
versions. Afterwards, back-merge `main` into `develop` as above. That is the only
route by which the fix reaches `develop`.

---

## Phase 7: Deployment to Any Cluster

The chart in `charts/kado-operator/` must install cleanly on any distribution.
Its `version` and `appVersion` are only a floor — CI overwrites both at publish
time, per [Versioning](#two-consequences-worth-knowing).

**Portability rules for the chart:**

- No `storageClassName` default — inherit the cluster default.
- No `nodeSelector`, `tolerations` or `affinity` defaults.
- Service type `ClusterIP` by default; anything else is opt-in.
- Guard optional CRD-dependent resources (e.g. `ServiceMonitor`) behind a flag.

### Install from Docker Hub (OCI)

```bash
helm install kado-operator oci://registry-1.docker.io/partofaplan/kado-operator \
  --version 0.1.0 \
  --namespace kado-operator-system \
  --create-namespace
```

### Install from a local checkout

```bash
helm install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system --create-namespace
```

To test a locally built image without pushing:

```bash
make cluster-load IMAGE_TAG=dev
helm upgrade --install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system --create-namespace \
  --set image.tag=dev \
  --set image.pullPolicy=Never
```

### Verify on a new distribution

Before claiming support for a distribution, run the integration suite against it:

```bash
kubectl config use-context <that-cluster>
make install
make test-integration
```

---

## Key Metrics to Track

Process rules are in [What never to do](#what-never-to-do); these are the numbers
worth watching.

- **Test coverage**: aim for >80% on controllers
- **CI wall-clock**: keep the critical path under 10 min — it gates every loop
- **Time to merge**: feature branch → develop should be <1 day, review included
- **Review turnaround**: a merge request should be evaluated the same day it
  opens — a slow review gate is the one people start routing around
- **Release cadence**: target weekly or bi-weekly promotions of `develop` to `main`
- **Portability**: integration suite green on at least two distributions

---

## Troubleshooting

### Wrong cluster

Most "it deployed but nothing happened" reports are a context mismatch:

```bash
kubectl config current-context
kubectl config get-contexts
```

### Local cluster won't start

Delete and recreate it: `make cluster-down && make cluster-up`
(add `LOCAL_PROVIDER=kind|minikube` if you are not on the default).

### Operator fails to start

The Helm release names the Deployment after the release, so it is `kado-operator`
— not `kado-operator-controller-manager`, which is the kustomize
(`make deploy`) name and does not exist in a chart install. Selecting by label
works for both and is what the validation job uses:

```bash
kubectl -n kado-operator-system logs -f --selector app.kubernetes.io/name=kado-operator
# Check for RBAC errors, missing CRDs, image pull issues
```

### Image pull failures

```bash
# Confirm the image exists and is public
docker pull docker.io/partofaplan/kado-operator:latest

# For a private repo, create a pull secret in the operator's namespace
kubectl create secret docker-registry dockerhub \
  --docker-server=https://index.docker.io/v1/ \
  --docker-username=partofaplan \
  --docker-password=<access-token> \
  -n kado-operator-system
# then set imagePullSecrets in values.yaml
```

A pull failure naming a tag like `sha-72a92f7`, `develop` or `main` is a stale
reference: those tags are not published. See
[Image tags](#image-tags-the-version-and-latest).

### Docker Hub push denied in CI

- Confirm `DOCKERHUB_TOKEN` is set as a repository secret and `DOCKERHUB_USER` is set in the workflow env
- An empty username resolves silently: check the login step's resolved username, not just that a secret exists
- Confirm the token has **Read/Write** scope and has not expired
- Pull requests from forks cannot read secrets — those runs build without pushing, which is expected

### Post-merge validation fails

Read the job's diagnostics step first — it captures logs, describes and events
before the test environment is torn down, precisely so they survive it.

The commit is already on `develop` when this runs. If `integration` failed, no
version was cut and no image published, so there is nothing to withdraw: fix
forward through a normal loop, or revert. If `verify-picard` failed, the image
*was* published and `picard` is left on the broken revision deliberately.

| Symptom | Likely cause |
| --- | --- |
| `ImagePullBackOff` on the operator pod | `publish` succeeded but pushed a tag the chart does not reference, or Docker Hub is unreachable from `picard`. Check the tag in the job log against <https://hub.docker.com/r/partofaplan/kado-operator/tags>. |
| Operator runs but the test environment never leaves `Provisioning` | RBAC. `make run` uses your kubeconfig and hides missing permissions; the chart's ClusterRole is what the pod actually gets. Check the logs for `forbidden`. |
| Helm refuses to upgrade, ownership error | A CRD applied by `make install` against `picard` — someone ran `make test-integration` at it. The preflight step names this explicitly. Clear it with `make uninstall` against the `picard` context. |
| Job cannot start | The self-hosted runner is offline, or `picard` is down. Neither is a skip: no validation happened. |
| `picard` still broken after a fix | Nothing rolls it back on its own. Either merge the fix, which upgrades it, or `helm rollback kado-operator -n kado-operator-system --kube-context picard`. |

### A merge request cannot clear review

| Symptom | What it means |
| --- | --- |
| Reviewer cannot reproduce "tests pass" | The Definition of Done was asserted, not met. Run every layer and paste the output into the merge request. |
| Reviewer keeps finding the same class of issue | The loop is too large. Split it: one loop, one concern. |
| You are tempted to approve your own change | That is the rule working. Spawn a separate reviewing agent. |
| Review is blocking an urgent fix | Urgency is not an exemption. A hotfix is the change most likely to be wrong. |

### Version was not assigned, or jumped

```bash
git fetch --tags
git tag --list 'v*' --sort=-v:refname | head -5
```

| Symptom | Cause |
| --- | --- |
| No tag after a merge to `develop` | The run failed before the `version` job, or the push was to a feature branch, which is never versioned. |
| MINOR jumped several numbers | Someone created a `v*` tag by hand; CI counts from the highest tag it finds. |
| Two runs raced for the same tag | The `version-assign` concurrency group is missing, so runs were not serialised. |
| Tag exists but no image | The `publish` job failed after tagging. Re-run failed jobs; the tag is already correct. |

### Integration tests hang

```bash
# Confirm you are pointed at a live cluster
kubectl config current-context
kubectl get nodes

# Inspect what the operator created
kubectl get devenvironments -A
kubectl get all -A -l app.kubernetes.io/managed-by=kado-operator
```

### Works on one distribution, fails on another

Usually an embedded assumption. Check for: a hardcoded StorageClass, a
LoadBalancer Service, an ingress annotation, or a cluster-name reference that
escaped into non-helper code.

---

## References

- [Kubebuilder Docs](https://book.kubebuilder.io/)
- [Gitflow Cheatsheet](https://danielkummer.github.io/git-flow-cheatsheet/)
- [Kubernetes Operator Best Practices](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/)
- [GitHub Actions for Go](https://github.com/actions/setup-go)
- [Docker Hub — partofaplan](https://hub.docker.com/repositories/partofaplan)
- [docker/build-push-action](https://github.com/docker/build-push-action)
- [Helm OCI Registries](https://helm.sh/docs/topics/registries/)
- Local cluster options: [K3D](https://k3d.io/) · [Kind](https://kind.sigs.k8s.io/) · [minikube](https://minikube.sigs.k8s.io/)
