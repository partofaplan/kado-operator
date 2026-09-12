---
name: kado-operator-sdlc
description: Build and iterate on the kado-operator (dev environment provisioning operator) using Gitflow, automated testing, and GitHub Actions CI/CD. Use this skill whenever working on the operator codebase—whether writing controllers in Go, setting up tests, configuring GitHub Actions, or deploying to any Kubernetes cluster. This skill covers the complete SDLC from feature branch to production readiness.
---

# KADO Operator Development Workflow

A structured approach to building a Kubernetes custom operator for automated dev environment provisioning using Go, Gitflow branching, local cluster testing, and GitHub Actions automation.

---

## Core Principle: Stay Cluster-Agnostic

**The operator must run on any conformant Kubernetes cluster.** K3D, Kind, minikube, Docker Desktop, EKS, GKE, AKS and OpenShift are all just targets — none of them is *the* target.

In practice this means:

- **Talk to clusters through kubectl contexts, never through a cluster provider's CLI.** Build and test steps use `kubectl`/`helm` against whatever context is current. `k3d`/`kind` commands belong only in optional convenience targets that create a cluster, never in the build, test, deploy or teardown path.
- **Never hardcode a cluster name.** No `picard`, no `kado-ci`. Where a name is unavoidable (a helper that creates a local cluster), make it an overridable variable.
- **Use no provider-specific Kubernetes features** — no K3s-only storage classes, no Traefik-specific ingress annotations, no `local-path` assumptions. If a test needs a StorageClass, use the cluster default.
- **Load images the portable way.** `k3d image load` and `kind load` exist for local speed only. CI and any real cluster pull from a registry.
- **Gate anything genuinely provider-specific** behind a documented variable with a portable default.

A good check: if a step would break by pointing `KUBECONFIG` at a remote EKS cluster, it is too tightly coupled.

### Container Registry

Images are published to **Docker Hub** under the `partofaplan` namespace:

- Repository: <https://hub.docker.com/repositories/partofaplan>
- Image: `docker.io/partofaplan/kado-operator`

CI authenticates with a single `DOCKERHUB_TOKEN` repository secret (a Docker Hub access token, not the account password). The username is not secret — it is the public namespace above — so it stays a plain `DOCKERHUB_USER` env var in the workflow.

---

## Core Principle: Branch Before You Change

**Every change to the codebase starts on a feature branch cut from `develop`.**
No exceptions, including one-line fixes, documentation edits and changes made
by an AI assistant. `develop` and `main` are never committed to directly.

```bash
git checkout develop
git pull --ff-only
git checkout -b feature/<short-description>
```

Branch names describe the change, not the author or the tool:
`feature/env-provisioner-crd`, `feature/pvc-resize`, `hotfix/nil-deref-on-delete`.

Why this is a hard rule rather than a preference: every push to `develop`
cuts a version and publishes an image (see below). A commit made directly to
`develop` is therefore a release, made without review, that cannot be undone
without burning a version number. The feature branch is where work is
reviewable and abandonable.

Work merges back through a pull request into `develop`. Only release
preparation moves `develop` into `main`.

---

## The Loop: Branch → Done → Verify → Review → Merge

Work proceeds in **loops**. One loop is one unit of change: a feature, a fix, a
refactor. Every loop follows the same five gates, in order, with no step
skipped and no step reordered. These are requirements, not suggestions.

```
     ┌─ 1. BRANCH ──────────────────────────────────────────┐
     │  cut feature/<name> from develop                      │
     └───────────────────────────┬───────────────────────────┘
                                 ▼
     ┌─ 2. DONE ────────────────────────────────────────────┐
     │  every test layer passes; merge request opened;       │
     │  every CI check green                                 │
     └───────────────────────────┬───────────────────────────┘
                                 ▼
     ┌─ 3. VERIFY ON PICARD ────────────────────────────────┐
     │  deploy/upgrade the operator on the live cluster,     │
     │  prove it provisions a test environment, then wipe    │
     │  it. On failure: an agent posts logs + events to      │
     │  the merge request.                                   │
     │  SKIPPED for develop → main.                          │
     └───────────────────────────┬───────────────────────────┘
                                 ▼
     ┌─ 4. REVIEW ──────────────────────────────────────────┐
     │  evaluated by a DIFFERENT agent than the author       │
     └───────────────────────────┬───────────────────────────┘
                                 ▼
     ┌─ 5. MERGE ───────────────────────────────────────────┐
     │  only on approval → CI tags the version → release     │
     └───────────────────────────────────────────────────────┘
```

### Gate 1 — One branch per loop

A new loop MUST begin with a new feature branch cut from current `develop`.
Feature branches are short-lived and single-purpose: one loop, one branch, one
merge request. Do not continue a finished loop's branch into the next loop,
and do not accumulate unrelated changes on one branch — a reviewer cannot
meaningfully approve a branch that does three things.

### Gate 2 — Definition of Done

A loop is **done** when the tests pass and CI agrees. Concretely, all of these
MUST be true before the change moves on to verification:

| Check | Command | Proves |
| --- | --- | --- |
| Unit + envtest | `make test` | Reconcile logic and the generated CRD schema |
| Integration | `make test-integration` | Real cluster behaviour end to end |
| Lint | `make lint` | No new correctness or style violations |
| Generated code current | `make manifests generate helm-crds` leaves no diff | Committed CRDs match the Go types |
| CI green | GitHub Actions, on the open merge request | It passes somewhere other than your machine |

"Tests pass" means you ran them and read the output. A loop is not done
because the change looks right, because it compiled, or because the tests were
passing before you started. Green locally but red in CI is **not done**.

Open the merge request once these pass. Gate 3 begins only after the merge
request exists and every CI check on it is green — verifying a branch CI has
already rejected wastes a cluster deploy on a change that cannot merge.

If any check cannot pass for a defensible reason, say so explicitly in the
merge request. Do not open one claiming done when it is not.

### Gate 3 — Live verification on the picard cluster

Unit tests, envtest and the integration suite all run the reconciler as a
process. None of them proves the thing you actually ship — the built image,
running under the chart's RBAC, inside a real cluster. This gate does.

**Who runs it:** the agent driving the loop, by hand, using the commands
below. There is no workflow for it yet. `deploy.yml` is *not* this gate and
cannot be: it is `workflow_dispatch`-only by design (public repo, physical
runner), and its preflight requires a published image tag, which a feature
branch never has. Until a workflow exists, run this on a machine that can
reach `picard` and hold the one-at-a-time rule by not starting a second run.

**Who it applies to:** every merge request except `develop` → `main`. See
[below](#which-merges-are-exempt).

`picard` is a disposable test cluster. This gate uninstalls whatever operator
is on it and deletes the CRD, which deletes every `DevEnvironment` with it.
Never run it against a cluster anyone depends on.

Pin `--kube-context` on every command. This machine also has a
`rancher-desktop` context, and `make uninstall` deletes the CRD on whatever
context is current.

**1. Build and side-load the image from the merge request's head commit.**
Feature branches publish nothing to Docker Hub, so verifying a published tag
would verify the wrong code. Tag by commit, not by PR number, so the image
names exactly what was tested:

```bash
SHA=$(git rev-parse --short HEAD)
CTX=k3d-picard

# Gate 2 runs `make test-integration`, which depends on `make install` and
# leaves a kustomize-applied CRD. Helm refuses to adopt it, so without this the
# deploy below fails with an ownership error every time. This is the normal
# state of the cluster after Gate 2, not an edge case.
kubectl config use-context "$CTX"
make uninstall ignore-not-found=true

make cluster-load LOCAL_CLUSTER=picard IMAGE_TAG="sha-$SHA"
```

**2. Deploy, or upgrade in place if a release is already present.** An upgrade
exercises the path real users take and catches migration failures a clean
install hides.

```bash
helm upgrade --install kado-operator ./charts/kado-operator \
  --kube-context "$CTX" \
  --namespace kado-operator-system --create-namespace \
  --set image.tag="sha-$SHA" \
  --set image.pullPolicy=Never \
  --wait --timeout 5m
```

**Do not pass `--rollback-on-failure` (or Helm 3's `--atomic`) here.** Rolling
back is right for a deploy and wrong for a *verification*: it reverts the
failure inside this step, before any diagnostics can be taken. After a
rollback the failing pod is gone, `kubectl logs deploy/kado-operator` streams
the previous healthy pod, and `helm status` reports `STATUS: deployed` —
actively hiding the failure it just recovered from. On a first install it is
worse: at revision 1 there is nothing to roll back to, so Helm uninstalls,
taking the CRD and every `DevEnvironment` with it. This gate wants the
wreckage left exactly where it fell. `--wait` alone fails without cleaning up
after itself, which is what we need.

(`--wait` works on Helm 3 and 4. `--rollback-on-failure` is Helm 4 only —
another reason not to use it here.)

**3. Prove it actually works.** Deploying is not evidence; a running pod that
reconciles nothing still reports Ready. Apply a *dedicated* test resource —
not `config/samples/`, whose fixed name `team-alpha` collides with anything
already on the cluster:

```bash
ENV="verify-$SHA"
cat <<YAML | kubectl --context "$CTX" apply -f -
apiVersion: devenv.aviture.dev/v1alpha1
kind: DevEnvironment
metadata: { name: $ENV, namespace: default }
spec:
  owner: live-verification
  storage: { size: 128Mi }
  services:
    - { name: redis, image: redis:7-alpine, port: 6379 }
YAML
```

Then assert, with a timeout:

- the namespace `$ENV` is created
- a Deployment and Service exist for every service in the spec
- the PVC binds
- `status.phase` reaches `Ready` with `readyServices == totalServices`
- `Available` is true and `Degraded` is false

**4. Prove teardown works.** Delete the test resource and assert the namespace
is reclaimed and the finalizer released. Cleanup is part of the operator's
contract, so it is part of the verification.

**5. Wipe.** The gate ends with no operator, no CRD and no test residue on the
cluster:

```bash
kubectl --context "$CTX" delete devenvironment "$ENV" --ignore-not-found
helm uninstall kado-operator -n kado-operator-system --kube-context "$CTX"
# helm uninstall does NOT remove the namespace it created.
kubectl --context "$CTX" delete ns kado-operator-system --ignore-not-found
# Side-loaded images accumulate on every node across runs.
k3d image rm -c picard "docker.io/partofaplan/kado-operator:sha-$SHA" 2>/dev/null || true
docker image rm "docker.io/partofaplan/kado-operator:sha-$SHA" 2>/dev/null || true
```

Uninstalling the release removes the CRD too, because the chart templates it
rather than shipping it in `crds/`.

**On failure: collect evidence, then wipe.** If any step fails, an agent MUST
gather diagnostics and post them to the merge request with `gh pr comment`
*before* running step 5 — the namespace, the pods and their events all
disappear with teardown.

```bash
ENV_NS="$ENV"
helm --kube-context "$CTX" -n kado-operator-system history kado-operator
helm --kube-context "$CTX" -n kado-operator-system status kado-operator
kubectl --context "$CTX" -n kado-operator-system logs deploy/kado-operator --tail=200
kubectl --context "$CTX" -n kado-operator-system logs deploy/kado-operator --previous --tail=200 || true
kubectl --context "$CTX" -n kado-operator-system describe pods
kubectl --context "$CTX" describe devenvironment "$ENV"
kubectl --context "$CTX" get devenvironment "$ENV" -o yaml
kubectl --context "$CTX" -n "$ENV_NS" get deploy,svc,pvc,pods -o wide
kubectl --context "$CTX" -n "$ENV_NS" describe pods
kubectl --context "$CTX" -n "$ENV_NS" get events --sort-by=.lastTimestamp
kubectl --context "$CTX" -n kado-operator-system get events --sort-by=.lastTimestamp
```

`helm history` is in that list deliberately: it is the only one of these that
reveals a failed revision. Note also that a cluster-wide
`kubectl get events -A | tail` is nearly useless — on a real cluster the tail
is almost entirely `kube-system` noise. Scope events to the namespaces you
care about.

The comment must state which step failed, what was expected and what happened.
"Deployment failed" is not a diagnosis; handing the author the evidence is the
entire point of this gate.

Then run step 5 anyway. A failed verification must not leave the cluster dirty
for the next merge request.

**The gate fails if it cannot run.** If `picard` is unreachable, that is a
failure, not a skip. A gate that silently passes when it cannot execute is
worse than no gate.

Only one verification may run at a time: `picard` is a single shared cluster,
and two concurrent runs would delete each other's resources.

#### Which merges are exempt

Exactly one: **`develop` → `main`**.

Everything in that merge request already passed this gate on its way into
`develop`, because a merge request is the only way in. Re-running it would
re-verify verified code at exactly the wrong moment — a promotion to `main`
cuts a MAJOR and publishes a release, and wiring a disposable dev cluster into
the release path makes releases fail for reasons that have nothing to do with
the release.

**`hotfix/*` → `main` is NOT exempt.** A hotfix branches from `main` and merges
back to `main` without ever passing through `develop`, so it has never been
verified. It is also, by the nature of hotfixes, the change most likely to have
been written in a hurry. It runs the full gate.

**Documentation-only changes are exempt.** A change touching only `*.md` files
— no Go, no chart, no workflow, no Makefile, no `config/` — cannot affect what
the operator does in a cluster, and requiring a serialized cluster deploy to
merge a typo fix is how a rule gets routed around. Any other change runs the
gate.

Promotions to `main` and documentation changes still require CI green and
independent review. They just do not touch `picard`.

### Gate 4 — Independent agent review

Every merge into `develop` MUST go through a merge request. Every merge from
`develop` into `main` MUST go through a merge request. There are no direct
merges and no exceptions for small changes.

Review comes after live verification, so the reviewer can see whether the
change actually worked in a cluster rather than only whether the tests passed.

**The merge request MUST be evaluated by a different agent than the one that
wrote the change.** This is the central rule of this gate.

An agent reviewing its own work is not review. It re-derives the same
assumptions that produced the code, so the failure modes it missed while
writing are exactly the ones it will miss while reading. Worse, it already
believes the change is correct — it wrote it — so it reads to confirm rather
than to falsify. A separate agent starts from the diff with no attachment to
the approach and no memory of why a shortcut seemed reasonable at the time.

The reviewing agent MUST:

- Start from the **diff and the merge request description**, not from the
  authoring conversation. A fresh context is the point; inheriting the
  author's reasoning defeats it.
- **Independently verify the Definition of Done** rather than trusting the
  description. Run the tests. A merge request asserting "tests pass" is a
  claim to check, not evidence.
- Look for what the author could not see: unhandled error paths, missing test
  coverage for the change, assumptions that hold only on the author's cluster,
  breaking changes to the CRD schema, drift between RBAC markers and the chart.
- Return an explicit verdict — **APPROVE**, or **REQUEST CHANGES** with
  specific, actionable findings. "Looks good" is not a verdict.

The authoring agent MUST NOT approve its own merge request, and MUST NOT
merge on the strength of its own assessment.

In Claude Code, spawn the reviewer as a subagent with its own context, or run
the review in a separate session:

```bash
/code-review                       # reviews the current diff
/code-review <pr-number>           # reviews a specific merge request
gh pr create --base develop --fill # open the MR first
```

Blocking findings are fixed **on the same feature branch**, and the merge
request is re-reviewed. A finding is not resolved by arguing it away in a
comment; either change the code or explain in the merge request why the
finding does not apply, and let the reviewer decide.

### Gate 5 — Merge, tag, release

Only an approved merge request may be merged. The merge is what authorises
the version: merging into `develop` cuts the next MINOR, merging into `main`
cuts the next MAJOR, and CI does the tagging (see below). Nothing is tagged
or released that did not pass an independent review.

### What never to do

- **Never open a merge request before the Definition of Done is met.**
- **Never merge a change that has not been verified on a live cluster,** except
  a promotion of `develop` into `main`, which is exempt by design.
- **Never let a failed verification wipe the cluster before its logs and events
  have been posted to the merge request.**
- **Never leave `picard` dirty** — the gate cleans up on success and on failure.
- **Never point the verification gate at a cluster holding environments anyone
  cares about.** It deletes the CRD, and every `DevEnvironment` with it.
- **Never review your own change.** If you wrote it, a different agent reviews it.
- **Never merge an unreviewed or change-requested merge request.**
- **Never merge directly into `develop` or `main`,** including fast-forward merges.
- **Never tag or release a commit that did not come through an approved merge request.**

---

## Versioning: MAJOR.MINOR

Versions are `MAJOR.MINOR`. There is no patch component.

| Event | Effect | Example |
| --- | --- | --- |
| Push to `develop` | MINOR increments | `v1.4` → `v1.5` |
| `develop` merged to `main` | MAJOR increments, MINOR resets to 0 | `v1.5` → `v2.0` |

So a cycle reads: `v0.1`, `v0.2`, `v0.3` on `develop`, then `v1.0` when that
work reaches `main`, then `v1.1`, `v1.2` on `develop`, then `v2.0`, and so on.

**MAJOR here means "released", not "breaking".** This is a deliberate
departure from SemVer: a major bump marks the promotion of `develop` into
`main`, whatever the change contained. Communicate breaking changes in the
release notes, because the version number will not signal them.

### Who assigns versions

GitHub Actions does, never a human. The version is derived from the highest
existing `v*` git tag, so the tags themselves are the source of truth — there
is no VERSION file to drift, and no commit written back to a branch.

- Push to `develop` → tag `v<MAJOR>.<MINOR + 1>`
- Push to `main` → tag `v<MAJOR + 1>.0`

Never create a `v*` tag by hand. Doing so silently reassigns what CI computes
next.

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
loops. A separate release workflow listening on `push: tags: v*` would
therefore never fire. Everything that reacts to a new version — image tags,
chart publish, GitHub release — must live in the same run that creates the
tag.

---

## Quick Start: Workflow Overview

```
1. Feature branch cut from develop      (one loop, one branch)
   ↓
2. Local development + local cluster testing
   ↓
3. DEFINITION OF DONE: all test layers pass, CI green
   ↓
4. Merge request opened against develop; CI checks go green
   ↓
5. VERIFY GATE: deploy/upgrade on the live picard cluster, prove it
   provisions a test environment, wipe it. On failure an agent posts
   logs + events to the merge request.
   ↓
6. REVIEW GATE: evaluated by a DIFFERENT agent than the author
   ↓                              ↘ changes requested → back to 2
7. Approved → merged into develop
   ↓
8. Actions tag vMAJOR.(MINOR+1) and publish that image to Docker Hub
   ↓
9. When the line is ready: merge request from develop into main,
   reviewed the same way but NOT re-verified on picard
   ↓
10. Actions tag v(MAJOR+1).0, publish the image, chart and GitHub release
   ↓
11. Deploy to any cluster via Helm/OCI
```

---

## Phase 1: Local Development Setup

### Prerequisites
- Go 1.21+ installed locally
- **Any Kubernetes cluster** reachable via a kubectl context — K3D, Kind, minikube, Docker Desktop, or a remote cluster
- kubebuilder v3.11+ (for scaffolding CRD boilerplate)
- Docker Desktop or equivalent (for building operator images)
- Helm 3.8+ (for chart packaging and OCI push)

### Pick a Cluster

Any of these work. Pick one; nothing downstream depends on the choice.

```bash
# K3D
k3d cluster create dev --kubeconfig-update-default

# Kind
kind create cluster --name dev

# minikube
minikube start -p dev

# Docker Desktop — enable Kubernetes in settings, then:
kubectl config use-context docker-desktop

# Remote cluster — just select the context
kubectl config use-context my-remote-cluster
```

Confirm which cluster you are about to act on, every time:

```bash
kubectl config current-context
```

### Initialize Feature Branch
```bash
git flow feature start <feature-name>
# Example: git flow feature start env-provisioner-crd
```

The `git flow` CLI is optional — plain git branches follow the same model.

### Generate Operator Scaffold (if needed)
```bash
kubebuilder init --domain aviture.dev --repo github.com/partofaplan/kado-operator
kubebuilder create api --group devenv --version v1alpha1 --kind DevEnvironment
```

---

## Phase 2: Writing Controllers in Go

### Controller Patterns for DevEnvironment CRD

Your operator watches `DevEnvironment` custom resources and manages:
- Namespace provisioning
- PVC creation (storage)
- Deployment of supporting services (databases, caches, queues)
- Secret/ConfigMap injection
- Cleanup on CR deletion

### Key Files to Iterate On
- `api/v1alpha1/devenvironment_types.go` — CRD schema
- `internal/controller/devenvironment_controller.go` — Reconciliation logic
- `config/samples/devenv_v1alpha1_devenvironment.yaml` — Example CRs for testing

### Portability Rules for Controller Code

- **Do not assume a StorageClass.** Leave `storageClassName` unset to use the cluster default, and let the CRD expose it for callers who need a specific one.
- **Do not assume a default ingress controller or LoadBalancer.** ClusterIP Services are portable; anything else is opt-in.
- **Do not assume a node topology.** Single-node K3D and a 200-node EKS cluster must both work.
- **Watch by label, not by assumption.** Cross-namespace children cannot carry an `ownerReference`, so map them back with labels.

### Testing During Development

**Unit Tests**
```bash
# Run unit tests locally — no cluster required
go test ./internal/controller/... -v
```

**Integration Testing Against a Live Cluster**
```bash
# Verify which cluster you are pointed at
kubectl config current-context

# Deploy operator (during iteration)
make install  # Installs CRD to the current context's cluster
make run      # Runs operator locally against that cluster
```

**Manual Smoke Test**
```bash
# In a separate terminal, apply a test CR
kubectl apply -f config/samples/devenv_v1alpha1_devenvironment.yaml

# Watch reconciliation (when running in-cluster)
kubectl logs -f deployment/kado-operator-controller-manager -n kado-operator-system

# Verify the dev environment was provisioned
kubectl get ns <env-name>
kubectl get deployment,svc,pvc -n <env-name>
```

---

## Phase 3: Testing Strategy

Three layers, each with a different cost and a different thing it can prove.

### Unit Tests (Fast, No Cluster)
Write tests in `internal/controller/*_test.go`:
- Use a fake client for reconciliation logic
- Test every path (success, error, edge cases) and idempotency
- Runs anywhere, including a laptop with no Docker

**Example structure:**
```go
// internal/controller/devenvironment_controller_test.go
func TestDevEnvironmentReconciliation(t *testing.T) {
    // Setup
    ctx := context.Background()
    client := fake.NewClientBuilder().Build()
    reconciler := &DevEnvironmentReconciler{Client: client}

    // Test case
    req := reconcile.Request{
        NamespacedName: types.NamespacedName{Name: "test-env", Namespace: "default"},
    }

    // Execute
    result, err := reconciler.Reconcile(ctx, req)

    // Assert
    assert.NoError(t, err)
    assert.Equal(t, ctrl.Result{}, result)
}
```

### envtest (Local Control Plane, No Cluster)
`envtest` starts a real API server and etcd without a full cluster. Use it to prove the **generated CRD schema** behaves: defaults, validation, immutability (`XValidation`) rules.

Note its limits: envtest runs no kube-controller-manager and no scheduler. Nothing ever becomes Ready, and a deleted namespace never finishes terminating. Assert those in the integration layer instead.

### Integration Tests (Any Real Cluster)
Written in `test/integration/` — run against **whatever cluster the current kubectl context points at**. That is deliberately not tied to a provider: the same suite validates a local K3D cluster, a CI Kind cluster, or a staging EKS cluster.

Use these for the things only a real cluster does: pods actually becoming Ready, PVCs binding, namespace teardown completing.

Guard them with a build tag so `go test ./...` never reaches for a cluster:

```go
//go:build integration
```

**Run integration tests:**
```bash
make test-integration              # against the current context
make test-integration-local        # convenience: create a local cluster first
```

### End-to-End Tests (GitHub Actions)
Automatically triggered on push; GitHub Actions will:
- Spin up an ephemeral cluster (any provider — the workflow isolates this to one step)
- Build the operator image and push it to Docker Hub
- Deploy the operator via Helm
- Run the integration suite
- Report results

---

## Phase 4: Makefile for Local Workflow

Keep cluster-agnostic targets separate from local-cluster conveniences. The first group works against any context; the second exists only to get a cluster on your laptop.

```makefile
.PHONY: test test-integration lint docker-build docker-push run install uninstall clean help

# ---- Image coordinates (Docker Hub) ----
REGISTRY   ?= docker.io
IMAGE_NAME ?= partofaplan/kado-operator
IMG        ?= $(REGISTRY)/$(IMAGE_NAME):latest

# ---- Cluster-agnostic: acts on the CURRENT kubectl context ----

test:
	go test ./... -v -cover

test-integration:
	@echo "Running integration tests against context: $$(kubectl config current-context)"
	go test -tags=integration ./test/integration/... -v -timeout 15m

# Linting & formatting
lint:
	golangci-lint run ./...
	gofmt -s -w .

fmt:
	go fmt ./...

# Docker
docker-build:
	docker build -t $(IMG) .

docker-push: docker-build
	docker push $(IMG)

# Local operator deployment
install:
	kustomize build config/crd | kubectl apply -f -

uninstall:
	kustomize build config/crd | kubectl delete -f -

run:
	go run ./cmd/main.go

clean:
	kubectl delete -f config/samples/ --ignore-not-found=true
	$(MAKE) uninstall

# ---- Local cluster conveniences ----
# Optional. Only for creating a throwaway cluster on your own machine.
# Nothing in the build, test or deploy path depends on these.

LOCAL_CLUSTER   ?= dev
# One of: k3d | kind | minikube
LOCAL_PROVIDER  ?= k3d

cluster-up:
ifeq ($(LOCAL_PROVIDER),k3d)
	k3d cluster create $(LOCAL_CLUSTER) --kubeconfig-update-default || true
	kubectl config use-context k3d-$(LOCAL_CLUSTER)
else ifeq ($(LOCAL_PROVIDER),kind)
	kind create cluster --name $(LOCAL_CLUSTER) || true
	kubectl config use-context kind-$(LOCAL_CLUSTER)
else
	minikube start -p $(LOCAL_CLUSTER)
	kubectl config use-context $(LOCAL_CLUSTER)
endif

cluster-down:
ifeq ($(LOCAL_PROVIDER),k3d)
	k3d cluster delete $(LOCAL_CLUSTER)
else ifeq ($(LOCAL_PROVIDER),kind)
	kind delete cluster --name $(LOCAL_CLUSTER)
else
	minikube delete -p $(LOCAL_CLUSTER)
endif

# Side-load the image to skip a registry round trip. Local only — a real
# cluster pulls from Docker Hub instead.
cluster-load: docker-build
ifeq ($(LOCAL_PROVIDER),k3d)
	k3d image load $(IMG) -c $(LOCAL_CLUSTER)
else ifeq ($(LOCAL_PROVIDER),kind)
	kind load docker-image $(IMG) --name $(LOCAL_CLUSTER)
else
	minikube image load $(IMG) -p $(LOCAL_CLUSTER)
endif

test-integration-local: cluster-up install test-integration

help:
	@echo "Cluster-agnostic (uses current kubectl context):"
	@echo "  make test                   - Run unit tests"
	@echo "  make test-integration       - Run integration tests against current context"
	@echo "  make lint                   - Run linting checks"
	@echo "  make docker-build           - Build operator image ($(IMG))"
	@echo "  make docker-push            - Push operator image to Docker Hub"
	@echo "  make install                - Install CRD into current context"
	@echo "  make run                    - Run operator locally against current context"
	@echo "  make clean                  - Clean up deployments"
	@echo ""
	@echo "Local cluster helpers (LOCAL_PROVIDER=k3d|kind|minikube):"
	@echo "  make cluster-up             - Create local cluster '$(LOCAL_CLUSTER)'"
	@echo "  make cluster-down           - Delete local cluster"
	@echo "  make cluster-load           - Side-load image into local cluster"
	@echo "  make test-integration-local - Create cluster, install CRDs, run integration tests"
```

---

## Phase 5: GitHub Actions CI/CD Pipeline

Create `.github/workflows/` files to automate your SDLC.

**Required repository secret:**

| Secret | Value |
| --- | --- |
| `DOCKERHUB_TOKEN` | A Docker Hub access token with Read/Write scope |

Create the token at <https://hub.docker.com/settings/security> — never use the account password.

The Docker Hub *username* is not a secret: it is the public namespace already
embedded in every image reference. Keep it as a plain `DOCKERHUB_USER` env var
in the workflow. Storing it as a secret adds no protection and creates a
silent failure mode — a misnamed or missing username secret resolves to an
empty string, and the login fails with a confusing error.

### `.github/workflows/ci.yml` — Validate, Version, Publish

One workflow owns the whole line. It validates every push, and on `develop`
and `main` it additionally assigns the next version, tags the commit, and
publishes. The release steps live here rather than in a tag-triggered
workflow because a tag pushed with `GITHUB_TOKEN` does not trigger one.

```yaml
name: CI

on:
  push:
    branches: [ develop, main, 'feature/**', 'hotfix/**' ]
  pull_request:
    branches: [ develop, main ]

# Cancel superseded PR runs, but never cancel a develop/main run: those
# assign a version and publish, and a half-finished publish is worse than a
# slow queue.
concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: ${{ github.event_name == 'pull_request' }}

env:
  IMAGE: docker.io/partofaplan/kado-operator
  # Not a secret: this namespace is public and already appears in IMAGE.
  DOCKERHUB_USER: partofaplan

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      - uses: golangci/golangci-lint-action@v8
        with:
          args: --timeout=5m

  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      # Use the Makefile target: it provisions envtest binaries and sets
      # KUBEBUILDER_ASSETS, which a bare `go test ./...` would not.
      - run: make test

  generated:
    name: Generated code is up to date
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true
      # helm-crds matters: without it the chart's CRD copy drifts, CI stays
      # green, and the release job regenerates it at package time.
      - run: make manifests generate helm-crds
      - name: Fail if regeneration changed anything
        run: |
          if [[ -n "$(git status --porcelain)" ]]; then
            echo "::error::Generated files are out of date. Run 'make manifests generate helm-crds' and commit."
            git --no-pager diff
            exit 1
          fi

  # Lives here rather than in its own workflow so that `version` can depend on
  # it. Only the cluster-provisioning step is provider-specific.
  integration:
    runs-on: ubuntu-latest
    timeout-minutes: 25
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      # --- The ONLY provider-specific step. Swap for Kind, minikube, or a
      # kubeconfig secret pointing at a remote cluster; everything below acts
      # on the current kubectl context. ---
      - name: Create ephemeral cluster
        run: |
          curl -sfL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | TAG=v5.7.4 bash
          k3d cluster create ci --kubeconfig-update-default --wait

      - name: Show target cluster
        run: kubectl config current-context && kubectl get nodes

      - name: Run integration tests
        run: make test-integration

      - name: Dump cluster state on failure
        if: failure()
        run: |
          kubectl get devenvironments -A -o wide || true
          kubectl get all -A -l app.kubernetes.io/managed-by=kado-operator || true

  # Assigns MAJOR.MINOR from the highest existing tag. Only develop and main
  # produce versions; feature branches are validated but never versioned.
  version:
    name: Assign version
    # Integration is a dependency, not a separate workflow: a separate
    # workflow cannot gate this one, so a release could be cut with integration
    # tests red — contradicting the Definition of Done.
    needs: [ lint, test, generated, integration ]
    if: github.event_name == 'push' && contains(fromJSON('["develop","main"]'), github.ref_name)
    runs-on: ubuntu-latest
    permissions:
      contents: write
    # Serialised and never cancelled: two runs computing "the next version"
    # concurrently would race to push the same tag.
    concurrency:
      group: version-assign
      cancel-in-progress: false
    outputs:
      version: ${{ steps.next.outputs.version }}
      chart: ${{ steps.next.outputs.chart }}
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0   # tags are the source of truth; we need all of them

      - name: Compute next version
        id: next
        env:
          BRANCH: ${{ github.ref_name }}
        run: |
          set -euo pipefail
          # Match ONLY strict vMAJOR.MINOR. A glob like 'v[0-9]*.[0-9]*' would
          # also match v1.2-rc1 or v1.2.3, and the arithmetic below would then
          # expand a non-numeric minor and produce an empty version.
          latest=$(git tag --list 'v*' --sort=-v:refname \
                     | grep -E '^v[0-9]+\.[0-9]+$' | head -n1 || true)
          latest=${latest:-v0.0}
          ver=${latest#v}
          major=${ver%%.*}
          minor=${ver#*.}
          case "$BRANCH" in
            # 10# forces base 10, so a tag like v1.08 is not read as octal.
            develop) next="${major}.$((10#$minor + 1))" ;;   # MINOR on every dev build
            main)    next="$((10#$major + 1)).0" ;;          # MAJOR on promotion
            *)       echo "::error::refusing to version branch $BRANCH"; exit 1 ;;
          esac
          # Never publish a malformed version.
          [[ "$next" =~ ^[0-9]+\.[0-9]+$ ]] \
            || { echo "::error::computed bad version '$next' from '$latest'"; exit 1; }
          if git rev-parse -q --verify "refs/tags/v${next}" >/dev/null; then
            echo "::error::tag v${next} already exists"; exit 1
          fi
          echo "version=${next}" >> "$GITHUB_OUTPUT"
          # Helm demands full SemVer2, so the chart carries an explicit patch.
          echo "chart=${next}.0" >> "$GITHUB_OUTPUT"
          echo "$latest -> v${next}" >> "$GITHUB_STEP_SUMMARY"

      - name: Tag the commit
        run: |
          set -euo pipefail
          V="v${{ steps.next.outputs.version }}"
          git config user.name  "github-actions[bot]"
          git config user.email "41898282+github-actions[bot]@users.noreply.github.com"
          git tag -a "$V" -m "$V from ${{ github.ref_name }}"
          git push origin "$V"

  publish:
    name: Build & publish image
    needs: [ version ]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: docker/setup-qemu-action@v3
      - uses: docker/setup-buildx-action@v3

      - uses: docker/login-action@v3
        with:
          username: ${{ env.DOCKERHUB_USER }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}

      # The version tag is the durable one. The branch tag is a convenience
      # pointer, the sha tag an immutable escape hatch. `latest` follows main.
      - name: Derive image tags
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.IMAGE }}
          tags: |
            type=raw,value=${{ needs.version.outputs.version }}
            type=ref,event=branch
            type=sha,format=short
            type=raw,value=latest,enable=${{ github.ref_name == 'main' }}

      - name: Build and push
        id: build
        uses: docker/build-push-action@v6
        with:
          context: .
          push: true
          platforms: linux/amd64,linux/arm64
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max

  # Only a promotion to main produces a release artifact set.
  release:
    name: Publish release
    needs: [ version, publish ]
    if: github.ref_name == 'main'
    runs-on: ubuntu-latest
    permissions:
      contents: write
    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0

      # make helm-crds and make build-installer run go-installed tools.
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      - name: Package and push Helm chart
        env:
          V: ${{ needs.version.outputs.version }}
          CHART: ${{ needs.version.outputs.chart }}
        run: |
          set -euo pipefail
          make helm-crds
          # appVersion must name a tag that is actually published.
          helm package charts/kado-operator \
            --version "$CHART" --app-version "$V" --destination dist/
          echo "${{ secrets.DOCKERHUB_TOKEN }}" | helm registry login registry-1.docker.io \
            --username "${{ env.DOCKERHUB_USER }}" --password-stdin
          helm push "dist/kado-operator-${CHART}.tgz" oci://registry-1.docker.io/partofaplan

      - name: Build install manifest
        run: make build-installer IMG=${{ env.IMAGE }}:${{ needs.version.outputs.version }}

      - name: Create GitHub release
        uses: softprops/action-gh-release@v2
        with:
          tag_name: v${{ needs.version.outputs.version }}
          name: v${{ needs.version.outputs.version }}
          generate_release_notes: true
          files: |
            dist/install.yaml
            dist/kado-operator-*.tgz
```

Note what is *absent*: no `release.yml` triggered by `push: tags: v*`. Tags
are created by this workflow using `GITHUB_TOKEN`, and GitHub does not fire
workflows for such pushes. A tag-triggered release workflow would sit there
looking correct and never run.

The integration job is also *present* rather than living in its own workflow.
That is deliberate: a separate workflow cannot be a `needs` dependency, so
`version` could not gate on it, and a release could be tagged and published
while integration tests were failing.

**This listing is abridged.** The repository's `.github/workflows/ci.yml` is
authoritative and carries additional detail (pinned linter version, coverage
reporting, step summaries). Read that file before changing CI; do not treat
this excerpt as the current state of the pipeline.

### Why there is no separate integration workflow

The integration job is a job inside `ci.yml`, not a workflow of its own.

A workflow cannot declare a `needs` dependency on a *different* workflow. With
integration tests in their own file, `version` could not wait for them, so a
merge to `main` could tag, publish and release while integration tests were
still running or already red. The Definition of Done requires them to pass, so
they have to sit upstream of `version` in the same run.

The cost is that every push runs a cluster, making CI slower. That is the
right trade: a release that skipped its integration tests is worse than a
pipeline that takes four more minutes.

---

## Phase 6: Gitflow Workflow in Practice

### Every change starts on a feature branch

This is the rule from the top of this skill, restated where the work happens:
**never commit to `develop` or `main` directly.** A direct commit to `develop`
cuts a version and publishes an image with no review.

```bash
git checkout develop
git pull --ff-only
git checkout -b feature/env-provisioner-crd

# work, committing as you go
git add .
git commit -m "feat: add DevEnvironment CRD schema"
git push -u origin feature/env-provisioner-crd
```

Pushing a feature branch runs lint, tests and the generated-code check. It
does **not** assign a version and does **not** publish an image — only
`develop` and `main` do that.

### Finishing a feature

Meet the Definition of Done first — all test layers green, CI green on the
pushed branch. Only then open the merge request.

```bash
gh pr create --base develop --fill
```

Once CI is green on the merge request, verify it on the live cluster
(Gate 3): deploy or upgrade the operator on `picard`, prove it provisions a
test environment, then wipe everything. If it fails, an agent posts the
operator logs and cluster events to the merge request before teardown.

Then hand it to a **different agent** for review. The author does not approve
its own work:

```bash
/code-review <pr-number>
```

Merge only on APPROVE. If changes are requested, fix them on the same feature
branch, push, and have the reviewer re-evaluate.

The merge commit lands on `develop`, which:

1. assigns the next MINOR (`v1.4` → `v1.5`),
2. tags the merge commit `v1.5`,
3. publishes `partofaplan/kado-operator:1.5`, plus `develop` and `sha-<short>`.

### Promoting to main

When a line of work is ready, promote `develop` to `main` — through a merge
request, reviewed by a separate agent, exactly like a feature. A promotion is
a release; it gets *more* scrutiny than a feature, not less. There is no
version to bump by hand and no release branch to prepare — CI computes the
MAJOR.

```bash
gh pr create --base main --head develop \
  --title "Release: promote develop to main" --fill
/code-review <pr-number>        # a different agent evaluates it
# merge only on APPROVE
```

A promotion does **not** re-run the picard verification gate. Everything in it
already passed that gate on its way into `develop`, and a release must not
depend on a disposable dev cluster being up.

That push tags `v2.0`, publishes `partofaplan/kado-operator:2.0` and moves
`latest`, pushes the chart as `2.0.0`, and creates the GitHub release.

Then bring `main` back into `develop` so the branches do not diverge — through
a merge request, like everything else:

```bash
git checkout -b chore/back-merge-main develop
git merge --no-ff main
git push -u origin chore/back-merge-main
gh pr create --base develop --title "Back-merge main into develop" --fill
```

`develop` is protected and rejects direct pushes, so the old
`git push origin develop` form no longer works. It also mattered for a second
reason: a hotfix reaches `develop` only through this back-merge, and pushing
directly would carry hotfix code into `develop` with no review and no
verification.

Merging that request cuts `v2.1`. This is expected: the first development build
after a release is `MAJOR.1`.

### Hotfixes

A production fix still goes through a branch, but branches from `main`:

```bash
git checkout -b hotfix/nil-deref-on-delete main
# fix, meet the Definition of Done, open an MR into main,
# have a separate agent review it, then merge
```

Urgency does not waive the review gate. A hotfix is the change most likely to
be written quickly and least likely to have been thought through.

Merging it to `main` cuts the next MAJOR, because `main` only ever produces
MAJOR versions.

A hotfix **does** run the live verification gate. Only `develop` → `main` is
exempt, and a hotfix never passes through `develop`, so it has never been
verified anywhere.

Afterwards, back-merge `main` into `develop` through a merge request, as above.
That is the only route by which the fix reaches `develop`.

### What never to do

- **Never commit directly to `develop` or `main`.**
- **Never merge without an approved review from a different agent.**
- **Never create a `v*` tag by hand.** CI derives the next version from the
  highest existing tag; a hand-made tag silently reassigns everything after it.
- **Never delete or move a published tag.** Images are already pushed against
  it; the tag and the image would disagree.

## Phase 7: Deployment to Any Cluster

The chart must install cleanly on any distribution. Keep the defaults portable.

### Build Helm Chart (for reusability)

Create `charts/kado-operator/Chart.yaml`:
```yaml
apiVersion: v2
name: kado-operator
version: 0.1.0       # overwritten at publish time with MAJOR.MINOR.0
appVersion: "0.1"    # overwritten at publish time with MAJOR.MINOR
description: Operator for provisioning dev environments in Kubernetes
```

The committed values are only a floor. CI packages the chart with
`--version MAJOR.MINOR.0 --app-version MAJOR.MINOR`: Helm rejects a
two-component chart version, while `appVersion` must match a published image
tag exactly, because the chart defaults `image.tag` to it.

And in `values.yaml`, point at Docker Hub:
```yaml
image:
  repository: docker.io/partofaplan/kado-operator
  tag: ""            # defaults to .Chart.AppVersion
  pullPolicy: IfNotPresent
```

**Portability rules for the chart:**
- No `storageClassName` default — inherit the cluster default.
- No `nodeSelector`, `tolerations` or `affinity` defaults.
- Service type `ClusterIP` by default; anything else is opt-in.
- Guard optional CRD-dependent resources (e.g. `ServiceMonitor`) behind a flag.

### Deploy Locally (any local cluster)
```bash
helm install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system \
  --create-namespace \
  --values charts/kado-operator/values.yaml
```

To test a locally built image without pushing:
```bash
make cluster-load IMG=partofaplan/kado-operator:dev
helm upgrade --install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system --create-namespace \
  --set image.repository=partofaplan/kado-operator \
  --set image.tag=dev \
  --set image.pullPolicy=Never
```

### Deploy to Any Cluster (via Docker Hub OCI registry)
```bash
helm install kado-operator oci://registry-1.docker.io/partofaplan/kado-operator \
  --version 0.1.0 \
  --namespace kado-operator-system \
  --create-namespace
```

### Verify on a New Distribution

Before claiming support for a distribution, run the integration suite against it:
```bash
kubectl config use-context <that-cluster>
make install
make test-integration
```

---

## Phase 8: Iteration Workflow with Claude Code

When using Claude Code (or Claude in your IDE):

1. **Cut a feature branch first**
   ```bash
   git checkout develop && git pull --ff-only
   git checkout -b feature/<short-description>
   ```
   This applies to assistant-made changes exactly as it does to hand-written
   ones. If you find yourself on `develop` with uncommitted work, move it:
   `git stash && git checkout -b feature/… && git stash pop`.

2. **Open the repo in Claude Code**
   - Load `internal/controller/devenvironment_controller.go`
   - Load test files for context

3. **Write a feature**
   - Claude suggests controller changes
   - Generates test cases alongside code
   - Provides `make` commands to validate

4. **Reach the Definition of Done**
   ```bash
   make test                  # Unit + envtest — no cluster
   make test-integration      # Against your current context
   make lint                  # Catch issues early
   make manifests generate helm-crds && git status --porcelain   # must be empty
   ```
   Read the output. The loop is not done until every layer is green.

5. **Iterate based on feedback**
   - Push the feature branch
   - GitHub Actions run automatically
   - Review action output (coverage, lint results)
   - Make adjustments and push again until CI is green

6. **Open the merge request, verify live, then hand it off**
   ```bash
   gh pr create --base develop --fill
   # wait for CI green, then Gate 3: deploy/upgrade on picard, prove it
   # provisions a test environment, wipe it
   /code-review <pr-number>
   ```
   The review MUST come from a different agent than the one that wrote the
   change. If you authored it, you do not approve it. Fix any blocking
   findings on the same branch and have it re-reviewed.

7. **Merge on approval**
   - Merge the approved merge request into `develop`
   - CI cuts the next MINOR and publishes that image

8. **Release when ready**
   - Open a merge request from `develop` into `main`, reviewed the same way
   - On approval and merge, CI cuts the next MAJOR, publishes the image and
     chart, moves `latest`, and creates the GitHub release
   - Merge `main` back into `develop`

---

## Key Metrics to Track

- **Test Coverage**: Aim for >80% on controllers
- **Build Time**: Keep CI <10 min (critical for iteration speed)
- **Time to Merge**: Feature branch → develop should be <1 day, review included
- **Review Turnaround**: A merge request should be evaluated the same day it
  opens — a slow review gate is the one people start routing around
- **Unreviewed Merges**: Zero. Any merge without an approving review from a
  separate agent is a process failure, whatever the change contained
- **Unverified Merges**: Zero, excepting the two documented exemptions
  (`develop` → `main`, and documentation-only changes). Every change that can
  alter cluster behaviour is run on a real cluster before it merges
- **Cluster Left Dirty**: Zero. A verification run that does not clean up is a
  bug in the gate, not an inconvenience
- **Release Cadence**: Target weekly or bi-weekly promotions of `develop` to `main`
- **Portability**: Integration suite green on at least two distributions

---

## Troubleshooting

### Wrong Cluster
Most "it deployed but nothing happened" reports are a context mismatch:
```bash
kubectl config current-context
kubectl config get-contexts
```

### Local Cluster Won't Start
```bash
# K3D
k3d cluster delete dev && k3d cluster create dev --kubeconfig-update-default

# Kind
kind delete cluster --name dev && kind create cluster --name dev

# minikube
minikube delete -p dev && minikube start -p dev
```

### Operator Fails to Start
```bash
kubectl logs -f deployment/kado-operator-controller-manager -n kado-operator-system
# Check for RBAC errors, missing CRDs, image pull issues
```

### Image Pull Failures
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

### Docker Hub Push Denied in CI
- Confirm `DOCKERHUB_TOKEN` is set as a repository secret and `DOCKERHUB_USER` is set in the workflow env
- An empty username resolves silently: check the login step's resolved username, not just that a secret exists
- Confirm the token has **Read/Write** scope and has not expired
- Pull requests from forks cannot read secrets — those runs build without pushing, which is expected

### Live Verification Fails

Read the diagnostics the gate posted to the merge request first — they are
captured before teardown precisely so they survive it.

| Symptom | Likely cause |
| --- | --- |
| `ImagePullBackOff` on the operator pod | The image was not side-loaded, or `image.pullPolicy` is not `Never`. A feature branch publishes nothing, so the image must be built locally and loaded into the cluster. |
| Operator runs but the test environment never leaves `Provisioning` | RBAC. `make run` uses your kubeconfig and hides missing permissions; the chart's ClusterRole is what the pod actually gets. Check the logs for `forbidden`. |
| Helm refuses to install | A CRD left behind by `make install`, or by a previous verification that did not clean up. See CRD ownership below. |
| Gate cannot run at all | `picard` down or the self-hosted runner offline. This is a failure, not a skip — fix the cluster and re-run. |
| Two runs interfering | Verification is not serialised. Only one may touch `picard` at a time. |

### A Merge Request Cannot Clear Review

| Symptom | What it means |
| --- | --- |
| Reviewer cannot reproduce "tests pass" | The Definition of Done was asserted, not met. Run every layer and paste the output into the merge request. |
| Reviewer keeps finding the same class of issue | The loop is too large. Split it: one loop, one concern. |
| You are tempted to approve your own change | That is the rule working. Spawn a separate reviewing agent. |
| Review is blocking an urgent fix | Urgency is not an exemption. A hotfix is the change most likely to be wrong. |

### Version Was Not Assigned, or Jumped

```bash
git fetch --tags
git tag --list 'v*' --sort=-v:refname | head -5
```

| Symptom | Cause |
| --- | --- |
| No tag after a merge to `develop` | The run failed before the `version` job, or the push was to a feature branch, which is never versioned. |
| MINOR jumped several numbers | Someone created a `v*` tag by hand; CI counts from the highest tag it finds. |
| Two runs raced for the same tag | The `version-assign` concurrency group is missing, so runs were not serialised. |
| Tag exists but no image | The `publish` job failed after tagging. Re-running it republishes; the tag is already correct. |

### GitHub Actions Fails Locally
Run the same commands Actions runs:
```bash
make lint
make test
make docker-build
```

### Integration Tests Hang
```bash
# Confirm you are pointed at a live cluster
kubectl config current-context
kubectl get nodes

# Inspect what the operator created
kubectl get devenvironments -A
kubectl get all -A -l app.kubernetes.io/managed-by=kado-operator
```

### Works on One Distribution, Fails on Another
Usually an embedded assumption. Check for: a hardcoded StorageClass, a
LoadBalancer Service, an ingress annotation, or a cluster-name reference that
escaped into non-helper code.

---

## Next Steps

1. **Scaffold the operator** using kubebuilder (if not done)
2. **Copy GitHub Actions workflows** to `.github/workflows/`
3. **Add the `DOCKERHUB_TOKEN`** repository secret
4. **Add Makefile targets** from Phase 4
5. **Write first feature branch** (cut from `develop`) and test locally
6. **Verify Actions run** on the PR, then confirm the merge to `develop` cuts
   a MINOR tag and publishes that image to Docker Hub
7. **Protect `develop` and `main`** so the feature-branch and review rules are
   enforced by the forge, not by discipline alone — require a merge request,
   require an approving review, and require CI to pass before merge
8. **Document custom resources** in `docs/` folder
9. **Set up Helm chart** for multi-cluster deployment
10. **Run the integration suite on a second distribution** to prove portability

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
