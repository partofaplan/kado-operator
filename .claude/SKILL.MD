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

## Quick Start: Workflow Overview

```
1. Feature Branch (gitflow feature/*)
   ↓
2. Local Development + Local Cluster Testing
   ↓
3. Unit & Integration Tests
   ↓
4. Push to GitHub (auto-triggers Actions)
   ↓
5. GitHub Actions Validates (lint, test, build)
   ↓
6. Image Pushed to Docker Hub (develop + sha tags)
   ↓
7. Code Review & Merge to develop
   ↓
8. Release Branch (gitflow release/*)
   ↓
9. Merge to main + Tag Release → versioned image on Docker Hub
   ↓
10. Deploy to Any Cluster via Helm/OCI
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

### `.github/workflows/ci.yml` — On Every Push

Validates the change, and on `develop` pushes an iteration image to Docker Hub so the latest build is always pullable.

```yaml
name: CI

on:
  push:
    branches: [ develop, main, 'release/**', 'feature/**' ]
  pull_request:
    branches: [ develop, main ]

concurrency:
  group: ci-${{ github.ref }}
  cancel-in-progress: true

env:
  IMAGE: docker.io/partofaplan/kado-operator
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
      - name: Run linters
        uses: golangci/golangci-lint-action@v8
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
      - name: Run unit tests
        run: make test
      - name: Report coverage
        run: go tool cover -func=cover.out | tail -1

  build:
    needs: [ lint, test ]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      # Only authenticate when we actually intend to push. Pull requests from
      # forks have no access to secrets, so they build without pushing.
      - name: Log in to Docker Hub
        if: github.event_name == 'push'
        uses: docker/login-action@v3
        with:
          username: ${{ env.DOCKERHUB_USER }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}

      # Derive tags from the ref: `develop` gets a moving tag plus an immutable
      # sha tag, so you can always pin the exact build you tested.
      - name: Derive image tags
        id: meta
        uses: docker/metadata-action@v5
        with:
          images: ${{ env.IMAGE }}
          tags: |
            type=ref,event=branch
            type=ref,event=pr
            type=sha,format=short

      - name: Build and push operator image
        uses: docker/build-push-action@v6
        with:
          context: .
          push: ${{ github.event_name == 'push' }}
          tags: ${{ steps.meta.outputs.tags }}
          labels: ${{ steps.meta.outputs.labels }}
          cache-from: type=gha
          cache-to: type=gha,mode=max
```

### `.github/workflows/integration-test.yml` — Integration Tests

The cluster provider is confined to a single step. Swapping K3D for Kind, or pointing at a remote cluster, changes only that step — the test command below it stays identical because it acts on the current kubectl context.

```yaml
name: Integration Tests

on:
  push:
    branches: [ develop, main, 'release/**' ]
  pull_request:
    branches: [ develop, main ]

concurrency:
  group: integration-${{ github.ref }}
  cancel-in-progress: true

jobs:
  integration:
    runs-on: ubuntu-latest
    timeout-minutes: 25
    steps:
      - uses: actions/checkout@v4

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      # --- Cluster provisioning: the ONLY provider-specific step. ---
      # Swap this for Kind, minikube, or a kubeconfig secret pointing at a
      # remote cluster. Everything after it is provider-agnostic.
      - name: Create ephemeral cluster
        run: |
          curl -sfL https://raw.githubusercontent.com/k3d-io/k3d/main/install.sh | bash
          k3d cluster create ci --kubeconfig-update-default --wait
      # --- End provider-specific section. ---

      - name: Show target cluster
        run: kubectl config current-context && kubectl get nodes

      - name: Install CRDs
        run: make install

      - name: Build operator image
        run: make docker-build IMG=partofaplan/kado-operator:ci

      - name: Run integration tests
        run: make test-integration

      - name: Dump cluster state on failure
        if: failure()
        run: |
          kubectl get devenvironments -A -o wide || true
          kubectl describe devenvironments -A || true
          kubectl get all -A -l app.kubernetes.io/managed-by=kado-operator || true
```

### `.github/workflows/release.yml` — Release & Deploy

```yaml
name: Release

on:
  push:
    tags:
      - 'v*'

env:
  IMAGE: docker.io/partofaplan/kado-operator
  DOCKERHUB_USER: partofaplan

jobs:
  release:
    runs-on: ubuntu-latest
    permissions:
      contents: write

    steps:
      - uses: actions/checkout@v4
        with:
          fetch-depth: 0   # full history for changelog generation

      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
          cache: true

      # A tag that cannot pass its own tests must not become a release.
      - name: Run tests
        run: make test

      - name: Set up QEMU
        uses: docker/setup-qemu-action@v3

      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v3

      - name: Log in to Docker Hub
        uses: docker/login-action@v3
        with:
          username: ${{ env.DOCKERHUB_USER }}
          password: ${{ secrets.DOCKERHUB_TOKEN }}

      - name: Build and push image
        uses: docker/build-push-action@v6
        with:
          context: .
          push: true
          platforms: linux/amd64,linux/arm64
          tags: |
            ${{ env.IMAGE }}:${{ github.ref_name }}
            ${{ env.IMAGE }}:latest
          cache-from: type=gha
          cache-to: type=gha,mode=max

      - name: Package and push Helm chart
        run: |
          VERSION="${{ github.ref_name }}"
          helm package charts/kado-operator \
            --version "${VERSION#v}" \
            --app-version "${VERSION}" \
            --destination dist/
          echo "${{ secrets.DOCKERHUB_TOKEN }}" | helm registry login registry-1.docker.io \
            --username "${{ env.DOCKERHUB_USER }}" --password-stdin
          helm push "dist/kado-operator-${VERSION#v}.tgz" oci://registry-1.docker.io/partofaplan

      - name: Create Release Notes
        run: |
          VERSION="${{ github.ref_name }}"
          PREV=$(git describe --tags --abbrev=0 "${VERSION}^" 2>/dev/null || true)
          {
            echo "# kado-operator ${VERSION}"
            echo
            echo "## Install"
            echo '```bash'
            echo "helm install kado-operator oci://registry-1.docker.io/partofaplan/kado-operator \\"
            echo "  --version ${VERSION#v} --namespace kado-operator-system --create-namespace"
            echo '```'
            echo
            echo "## Container Image"
            echo '```'
            echo "docker.io/partofaplan/kado-operator:${VERSION}"
            echo '```'
            echo
            echo "## Changes"
            if [ -n "$PREV" ]; then
              git log --pretty='- %s (%h)' "${PREV}..${VERSION}"
            else
              git log --pretty='- %s (%h)' "${VERSION}"
            fi
          } > RELEASE.md

      - name: Create GitHub Release
        uses: softprops/action-gh-release@v2
        with:
          body_path: RELEASE.md
          files: dist/kado-operator-*.tgz
          draft: false
          prerelease: ${{ contains(github.ref_name, '-rc') }}
```

---

## Phase 6: Gitflow Workflow in Practice

### Starting a Feature
```bash
git flow feature start env-provisioner-crd
# Work on code, commit regularly
git add .
git commit -m "feat: add DevEnvironment CRD schema"
```

### Finishing a Feature (into develop)
```bash
git flow feature finish env-provisioner-crd
# Or merge manually:
git checkout develop && git merge --no-ff feature/env-provisioner-crd
```

Merging to `develop` publishes a `develop` and `sha-<short>` image to Docker Hub, so the newest build is always pullable for testing on a shared cluster.

### Creating a Release
```bash
git flow release start v0.1.0
# Update Chart.yaml version/appVersion, CHANGELOG
git commit -am "chore: bump to v0.1.0"
git flow release finish v0.1.0
# Tags main branch, merges back to develop
git push origin main develop --tags   # the tag triggers the release workflow
```

---

## Phase 7: Deployment to Any Cluster

The chart must install cleanly on any distribution. Keep the defaults portable.

### Build Helm Chart (for reusability)

Create `charts/kado-operator/Chart.yaml`:
```yaml
apiVersion: v2
name: kado-operator
version: 0.1.0
appVersion: "0.1.0"
description: Operator for provisioning dev environments in Kubernetes
```

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

1. **Open the repo in Claude Code**
   - Load `internal/controller/devenvironment_controller.go`
   - Load test files for context

2. **Write a feature**
   - Claude suggests controller changes
   - Generates test cases alongside code
   - Provides `make` commands to validate

3. **Test locally**
   ```bash
   make test                  # Unit tests — no cluster
   make lint                  # Catch issues early
   make install && make run   # Against your current context
   ```

4. **Iterate based on feedback**
   - Push to feature branch
   - GitHub Actions run automatically
   - Review action output (coverage, lint results)
   - Make adjustments and push again

5. **Merge to develop**
   - Feature tests pass ✅
   - Actions green ✅
   - Merge via `git flow feature finish`
   - Iteration image lands on Docker Hub

6. **Release when ready**
   - `git flow release start vX.Y.Z`
   - GitHub Actions builds & pushes the multi-arch image
   - Tag created, image available at `docker.io/partofaplan/kado-operator:vX.Y.Z`

---

## Key Metrics to Track

- **Test Coverage**: Aim for >80% on controllers
- **Build Time**: Keep CI <10 min (critical for iteration speed)
- **Time to Merge**: Feature branch → develop should be <1 day
- **Release Cadence**: Target weekly or bi-weekly releases
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
5. **Write first feature branch** and test locally
6. **Verify Actions run** on PR to develop, and the image appears on Docker Hub
7. **Document custom resources** in `docs/` folder
8. **Set up Helm chart** for multi-cluster deployment
9. **Run the integration suite on a second distribution** to prove portability

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
