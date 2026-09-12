# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- Image tags reduced to two kinds: the immutable `MAJOR.MINOR` version, and
  `latest` pointing at whatever was published most recently. Per-commit
  (`sha-<short>`) and floating branch tags are no longer published — every
  commit that reaches a publish already has a version of its own. Tags pushed
  under the old scheme remain on Docker Hub but are frozen. `latest` now
  follows development builds as well as promotions, so it is a convenience
  rather than a stability channel.
- CI no longer runs on pushes to `feature/**` and `hotfix/**`. The
  `pull_request` trigger already covers them and tests the merge result rather
  than the branch tip; running both meant every commit on an open pull request
  executed the whole pipeline twice, in two concurrency groups that could not
  cancel each other.
- The Dockerfile build moved out of the `integration` job into its own
  parallel `image` job with a layer cache. It consumed ~99s of the critical
  path where nothing used its output.
- CI caches `bin/` under a per-job key, so `controller-gen`, `kustomize` and
  the envtest control-plane binaries are no longer re-downloaded in every job
  of every run.
- `.claude/SKILL.MD` rewritten so each rule and rationale is stated exactly
  once, and so it links to `Makefile` and `.github/workflows/ci.yml` rather
  than embedding copies that had already drifted from them.

### Added

- `DevEnvironment` CRD (`devenv.aviture.dev/v1alpha1`) for declaring isolated
  dev environments: namespace, shared storage, supporting services, injected
  config and secrets.
- Reconciler that provisions the namespace, a ConfigMap, copies of referenced
  Secrets, an optional PVC, and a Deployment plus ClusterIP Service per
  service; prunes resources dropped from the spec; and tears the namespace down
  via a finalizer on delete.
- Safety check that refuses to adopt or delete a namespace the operator did not
  create.
- Helm chart (`charts/kado-operator`) with RBAC, leader election, metrics
  service and optional ServiceMonitor.
- Test suite across three layers: fake-client unit tests, envtest specs
  covering the generated CRD schema, and a K3D integration suite behind the
  `integration` build tag.
- GitHub Actions for CI (lint, generated-file drift, tests, image build and
  push), integration tests on an ephemeral cluster, and tagged releases that
  publish a multi-arch image and the chart to Docker Hub under `partofaplan`.
- Multi-arch (`linux/amd64`, `linux/arm64`) images. Each publish pushes the
  immutable `MAJOR.MINOR` version tag and moves `latest` to it.
- Dockerfile builder stage pinned to `$BUILDPLATFORM`, so multi-arch builds
  cross-compile natively instead of emulating the toolchain under QEMU. This
  removes the need for kubebuilder's generated `Dockerfile.cross` workaround.
- `test-only` and `test-integration-only` Makefile targets: the same suites
  without the code-generation prerequisites, for CI, where a separate job
  already regenerates and diffs those files.
- Makefile targets that act on the current kubectl context
  (`test-integration`, `install`, `run`, `helm-crds`, `helm-lint`, `clean`),
  plus optional local-cluster helpers (`cluster-up`, `cluster-down`,
  `cluster-load`, `test-integration-local`) selectable with
  `LOCAL_PROVIDER=k3d|kind|minikube`.

[Unreleased]: https://github.com/partofaplan/kado-operator/compare/main...develop
