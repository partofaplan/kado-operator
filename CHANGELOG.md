# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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
- Multi-arch (`linux/amd64`, `linux/arm64`) images on every push. Branch
  pushes publish a moving branch tag plus an immutable `sha-<short>` tag;
  releases publish semver tags (`1.2.3`, `1.2`, `1`) and move `latest`, which
  prereleases never claim.
- Dockerfile builder stage pinned to `$BUILDPLATFORM`, so multi-arch builds
  cross-compile natively instead of emulating the toolchain under QEMU. This
  removes the need for kubebuilder's generated `Dockerfile.cross` workaround.
- Makefile targets that act on the current kubectl context
  (`test-integration`, `install`, `run`, `helm-crds`, `helm-lint`, `clean`),
  plus optional local-cluster helpers (`cluster-up`, `cluster-down`,
  `cluster-load`, `test-integration-local`) selectable with
  `LOCAL_PROVIDER=k3d|kind|minikube`.

[Unreleased]: https://github.com/partofaplan/kado-operator/compare/main...develop
