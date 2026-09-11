---
name: kado-operator-sdlc
description: Build and iterate on the kado-operator (dev environment provisioning operator) using Gitflow, automated testing, and GitHub Actions CI/CD. Use this skill whenever working on the operator codebase—whether writing controllers in Go, setting up tests, configuring GitHub Actions, or deploying to K3D clusters. This skill covers the complete SDLC from feature branch to production readiness.
---

# KADO Operator Development Workflow

A structured approach to building a Kubernetes custom operator for automated dev environment provisioning using Go, Gitflow branching, local K3D testing, and GitHub Actions automation.

---

## Quick Start: Workflow Overview

```
1. Feature Branch (gitflow feature/*)
   ↓
2. Local Development + K3D Testing
   ↓
3. Unit & Integration Tests
   ↓
4. Push to GitHub (auto-triggers Actions)
   ↓
5. GitHub Actions Validates (lint, test, build)
   ↓
6. Code Review & Merge to develop
   ↓
7. Release Branch (gitflow release/*)
   ↓
8. Merge to main + Tag Release
   ↓
9. Deploy to Any Cluster via Helm/OCI
```

---

## Phase 1: Local Development Setup

### Prerequisites
- Go 1.21+ installed locally
- K3D cluster named `picard` with kubectl context configured
- kubebuilder v3.11+ (for scaffolding CRD boilerplate)
- Docker Desktop or equivalent (for building operator images)

### Initialize Feature Branch
```bash
git flow feature start <feature-name>
# Example: git flow feature start env-provisioner-crd
```

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

### Testing During Development

**Unit Tests**
```bash
# Run unit tests locally
go test ./internal/controller/... -v
```

**Integration Tests with K3D**
```bash
# Start your K3D cluster if not running
k3d cluster create picard --kubeconfig-update-default

# Set context to picard
kubectl config use-context k3d-picard

# Deploy operator (during iteration, use make install)
make install  # Installs CRD to cluster
make run      # Runs operator locally against K3D cluster
```

**Manual Smoke Test**
```bash
# In a separate terminal, apply a test CR
kubectl apply -f config/samples/devenv_v1alpha1_devenvironment.yaml

# Watch reconciliation
kubectl logs -f deployment/kado-operator-controller-manager -n kado-operator-system

# Verify the dev environment was provisioned
kubectl get ns <env-name>
kubectl get deployment,svc,pvc -n <env-name>
```

---

## Phase 3: Testing Strategy

### Unit Tests (Fast, No Cluster)
Write tests in `internal/controller/*_test.go`:
- Mock Kubernetes client interactions
- Test reconciliation logic paths (success, error, edge cases)
- Use `envtest` for testing without a live cluster

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

### Integration Tests (K3D Cluster)
Written in `test/integration/` — run against real K3D cluster:
- Verify CRD installation
- Test full reconciliation flow end-to-end
- Confirm resources are created/cleaned up correctly

**Run integration tests:**
```bash
make test-integration  # Custom make target (see Makefile section below)
```

### End-to-End Tests (GitHub Actions)
Automatically triggered on push; GitHub Actions will:
- Spin up ephemeral K3D cluster
- Build & push operator image to local registry
- Deploy operator via Helm
- Run E2E test suite
- Report results

---

## Phase 4: Makefile for Local Workflow

Add this to your `Makefile` to streamline common tasks:

```makefile
.PHONY: test test-integration lint docker-build run install uninstall help

# Local testing
test:
	go test ./... -v -cover

test-integration:
	@echo "Starting K3D cluster for integration tests..."
	k3d cluster create picard --kubeconfig-update-default || true
	kubectl config use-context k3d-picard
	go test ./test/integration/... -v
	@echo "Integration tests complete. Cluster left running for inspection."

# Linting & formatting
lint:
	golangci-lint run ./...
	gofmt -s -w .

fmt:
	go fmt ./...

# Docker & deployment
docker-build:
	docker build -t kado-operator:latest .

docker-build-push:
	docker build -t kado-operator:latest .
	docker tag kado-operator:latest ghcr.io/partofaplan/kado-operator:latest
	docker push ghcr.io/partofaplan/kado-operator:latest

# Local operator deployment
install:
	kustomize build config/crd | kubectl apply -f -

run:
	go run ./cmd/main.go

# Development cleanup
clean:
	kubectl delete -f config/samples/ --ignore-not-found=true
	make uninstall

uninstall:
	kustomize build config/crd | kubectl delete -f -

help:
	@echo "Available targets:"
	@echo "  make test              - Run unit tests"
	@echo "  make test-integration  - Run integration tests on K3D cluster"
	@echo "  make lint              - Run linting checks"
	@echo "  make docker-build      - Build operator Docker image"
	@echo "  make install           - Install CRD to K3D cluster"
	@echo "  make run               - Run operator locally against K3D"
	@echo "  make clean             - Clean up deployments"
```

---

## Phase 5: GitHub Actions CI/CD Pipeline

Create `.github/workflows/` files to automate your SDLC:

### `.github/workflows/ci.yml` — On Every Push

```yaml
name: CI

on:
  push:
    branches: [ develop, main, 'release/*' ]
  pull_request:
    branches: [ develop, main ]

jobs:
  lint:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      - name: Run linters
        uses: golangci/golangci-lint-action@v3
        with:
          version: latest
          args: --timeout=5m

  test:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      - name: Run unit tests
        run: go test ./... -v -cover -coverprofile=coverage.out
      - name: Upload coverage
        uses: codecov/codecov-action@v3
        with:
          files: ./coverage.out

  build:
    needs: [ lint, test ]
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v2
      - name: Build operator image
        uses: docker/build-push-action@v4
        with:
          context: .
          push: false
          tags: kado-operator:${{ github.sha }}
```

### `.github/workflows/integration-test.yml` — K3D Integration Tests

```yaml
name: Integration Tests

on:
  push:
    branches: [ develop, main ]
  pull_request:
    branches: [ develop, main ]

jobs:
  integration:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      
      - uses: actions/setup-go@v4
        with:
          go-version: '1.21'
      
      - name: Create K3D cluster
        uses: nolar/setup-k3d-k3s@v1
        with:
          version: latest
          name: github-actions
      
      - name: Build operator image
        run: docker build -t kado-operator:latest .
      
      - name: Load image into K3D
        run: k3d image load kado-operator:latest -c github-actions
      
      - name: Install CRDs
        run: make install
      
      - name: Run integration tests
        run: make test-integration
        env:
          KUBECONFIG: ${{ env.KUBECONFIG }}
```

### `.github/workflows/release.yml` — Release & Deploy

```yaml
name: Release

on:
  push:
    tags:
      - 'v*'

jobs:
  release:
    runs-on: ubuntu-latest
    permissions:
      contents: write
      packages: write
    
    steps:
      - uses: actions/checkout@v4
      
      - name: Set up Docker Buildx
        uses: docker/setup-buildx-action@v2
      
      - name: Log in to GitHub Container Registry
        uses: docker/login-action@v2
        with:
          registry: ghcr.io
          username: ${{ github.actor }}
          password: ${{ secrets.GITHUB_TOKEN }}
      
      - name: Build and push image
        uses: docker/build-push-action@v4
        with:
          context: .
          push: true
          tags: |
            ghcr.io/partofaplan/kado-operator:${{ github.ref_name }}
            ghcr.io/partofaplan/kado-operator:latest
      
      - name: Create Release Notes
        run: |
          echo "# kado-operator ${{ github.ref_name }}" > RELEASE.md
          echo "" >> RELEASE.md
          echo "## Docker Image" >> RELEASE.md
          echo "\`\`\`" >> RELEASE.md
          echo "ghcr.io/partofaplan/kado-operator:${{ github.ref_name }}" >> RELEASE.md
          echo "\`\`\`" >> RELEASE.md
      
      - name: Create GitHub Release
        uses: softprops/action-gh-release@v1
        with:
          body_path: RELEASE.md
          draft: false
          prerelease: false
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
# Creates PR into develop branch automatically (if using GitHub Flow variant)
# Or merge manually: git checkout develop && git merge feature/env-provisioner-crd
```

### Creating a Release
```bash
git flow release start v0.1.0
# Update VERSION file, CHANGELOG
git commit -am "chore: bump to v0.1.0"
git flow release finish v0.1.0
# Tags main branch, merges back to develop
```

---

## Phase 7: Deployment to Any Cluster

### Build Helm Chart (for reusability)

Create `charts/kado-operator/Chart.yaml`:
```yaml
apiVersion: v2
name: kado-operator
version: 0.1.0
appVersion: "0.1.0"
description: Operator for provisioning dev environments in Kubernetes
```

### Deploy to K3D (Local Testing)
```bash
helm install kado-operator ./charts/kado-operator \
  --namespace kado-operator-system \
  --create-namespace \
  --values charts/kado-operator/values.yaml
```

### Deploy to Any Cluster (via OCI or GitHub Container Registry)
```bash
helm repo add kado oci://ghcr.io/partofaplan/charts
helm repo update
helm install kado-operator kado/kado-operator \
  --namespace kado-operator-system \
  --create-namespace
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
   make test              # Unit tests
   make lint              # Catch issues early
   make install && make run   # Deploy to K3D
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

6. **Release when ready**
   - `git flow release start vX.Y.Z`
   - GitHub Actions automatically builds & pushes image
   - Tag created, available in GHCR

---

## Key Metrics to Track

- **Test Coverage**: Aim for >80% on controllers
- **Build Time**: Keep CI <10 min (critical for iteration speed)
- **Time to Merge**: Feature branch → develop should be <1 day
- **Release Cadence**: Target weekly or bi-weekly releases

---

## Troubleshooting

### K3D Cluster Won't Start
```bash
k3d cluster delete picard
k3d cluster create picard --kubeconfig-update-default
```

### Operator Fails to Start
```bash
kubectl logs -f deployment/kado-operator-controller-manager -n kado-operator-system
# Check for RBAC errors, missing CRDs, image pull issues
```

### GitHub Actions Fails Locally
Run the same commands in Actions locally:
```bash
docker build -t test .
docker run --rm test go test ./...
```

### Integration Tests Hang
```bash
# Check if K3D is running
k3d cluster list

# Manually clean up
k3d cluster delete github-actions
```

---

## Next Steps

1. **Scaffold the operator** using kubebuilder (if not done)
2. **Copy GitHub Actions workflows** to `.github/workflows/`
3. **Add Makefile targets** from Phase 4
4. **Write first feature branch** and test locally
5. **Verify Actions run** on PR to develop
6. **Document custom resources** in `docs/` folder
7. **Set up Helm chart** for multi-cluster deployment

---

## References

- [Kubebuilder Docs](https://book.kubebuilder.io/)
- [Gitflow Cheatsheet](https://danielkummer.github.io/git-flow-cheatsheet/)
- [Kubernetes Operator Best Practices](https://kubernetes.io/docs/concepts/extend-kubernetes/operator/)
- [GitHub Actions for Go](https://github.com/actions/setup-go)
- [K3D Documentation](https://k3d.io/)