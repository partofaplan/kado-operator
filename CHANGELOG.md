# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and the project uses
[Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added

- `spec.imagePullSecrets` and `spec.services[].imagePullSecrets`: authenticate
  to a private registry. Names docker-registry Secrets in the `DevEnvironment`'s
  own namespace, which are copied into the environment namespace alongside
  `secretRefs`. A service's list replaces the environment's rather than adding
  to it, the same precedence `registry` uses. A Secret that is neither
  `kubernetes.io/dockerconfigjson` nor `kubernetes.io/dockercfg` is rejected
  during reconcile, because the kubelet accepts a wrong-typed pull secret and
  then silently ignores it.

- `spec.registry` and `spec.services[].registry`: choose the registry service
  images are pulled from, for the whole environment or one service at a time.
  The service-level field overrides the environment's. An image that already
  names a registry is never rewritten — using Docker's own rule for what counts
  as a host — so a service pinned to `quay.io/team/api:1` keeps it and no
  opt-out flag is needed. Unset leaves every image exactly as written.

- `.github/workflows/release.yml`: a `workflow_dispatch` release, from `main`
  only, that cuts the next RELEASE version and publishes the package — the
  multi-arch image, the Helm chart, `install.yaml`, the CRD on its own, and a
  sample environment. Release notes
  are generated from the pull requests merged since the **previous release
  tag**, not the previous tag, since every merge cuts one. `dry_run` defaults
  to true and builds everything without tagging or publishing. The version is
  tagged only after every artifact has been built, so a failed package cannot
  burn a release number.
- `hack/next-version.sh` and `hack/next-version_test.sh` (`make test-scripts`).

- `readinessProbe` on a service spec: a standard Kubernetes probe, with an
  empty handler (`readinessProbe: {}`) filled in as a TCP check against the
  service's own port. Without a probe Kubernetes calls a running container
  ready the instant it starts, so `readyServices` counted containers that had
  been *started* rather than services that work — a database still starting up,
  or one about to crash, counted as ready. Omitting the field leaves behaviour
  unchanged.
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
  immutable version tag and moves `latest` to it.
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


### Changed

- The Helm chart is distributed as a GitHub release asset rather than pushed to
  an OCI registry. `helm install` accepts the attached chart's URL directly, so
  a registry added authentication and a second place to keep in step with the
  image for no gain. Publishing it to Docker Hub was not an option either: the
  chart would have landed at the image's own repository and tag and replaced it.
- Versions are now `RELEASE.MAJOR.MINOR`. Merging into `develop` moves MINOR,
  merging into `main` moves MAJOR, and cutting a release moves RELEASE. The
  first two stay automatic; the third is a deliberate `workflow_dispatch`,
  because promoting work and shipping it are different acts. Numbering seeds
  from the highest two-place tag (`v0.9` → `0.9.0`) so it stays monotonic
  across the change and nothing sorts below an image already published.
- The version arithmetic moved out of `ci.yml` into `hack/next-version.sh`,
  shared with `release.yml` and covered by tests run on every pull request.
  Two inline copies would eventually have disagreed about what "next" means,
  and a mistake here mints an immutable tag and publishes an image under it.
- `Chart.yaml` carries `version: 0.0.0` and `appVersion: "latest"` as
  placeholders, both overwritten at package time. `appVersion` is `latest`
  rather than a number so a from-a-checkout install gets the newest published
  build instead of a committed number that drifts out of date and renders an
  image reference that was never pushed.
- Promoting `develop` to `main` no longer creates a GitHub release. It cuts a
  MAJOR version and publishes an image; the chart, `install.yaml` and the
  release itself now come from the release workflow.

- No stage before a merge touches a cluster. The `integration` job is now
  `push`-only, so a pull request runs only lint, code generation, unit tests,
  envtest and a Dockerfile build — it no longer creates a k3d cluster inside
  the runner or applies a CRD to it. `make test-integration` is correspondingly
  no longer part of the Definition of Done; it remains available as a tool.
- Added the `verify-picard` job: after a merge into `develop` is versioned and
  published, the published image is upgraded onto the live `picard` cluster on
  the self-hosted runner and proven to provision a test `DevEnvironment` to
  Ready, with teardown asserted. The operator is **not** uninstalled afterwards
  — `picard` tracks `develop` and each merge upgrades the release in place.
  Only the test environment is temporary. Runs on `develop` only.
- The gate model is now Branch → Done → Review → Merge → Validate, with
  validation after the merge rather than a manual cluster deploy before it.
  Both Gate 3 exemptions (`develop`→`main` and documentation-only) are gone
  along with the manual gate that needed them.
- Corrected the `picard` kube context name in `deploy.yml`'s `kube_context`
  default and in `docs/development.md`. The context and the k3d cluster are
  both `picard`; `k3d-picard` is only the kubeconfig cluster-entry name, so the
  old default failed `deploy.yml`'s own preflight.
- Branch protection on `develop` and `main` now requires `Image builds` instead
  of `Integration`. `Integration` no longer runs on pull requests, and GitHub
  counts a skipped check as satisfied — leaving it required gave false
  assurance while the only new pre-merge signal went unguarded.

- Image tags reduced to two kinds: the immutable version, and
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
- `deploy.yml` handles moving tags correctly. Deploying `latest` twice used to
  render a byte-identical pod spec, so Helm wrote a new revision but the
  Deployment's `.spec.template` was unchanged: no new ReplicaSet, no pod
  restart, `rollout status` returned instantly and the job reported success
  while the old image kept running. It now sets `image.pullPolicy=Always` and
  varies a pod annotation, which are each necessary and neither sufficient —
  the annotation forces a real rollout, and `Always` makes the replacement pod
  fetch the new digest rather than reuse the node's cached layer. Its default
  input also moves from `develop` (no longer published) to `latest`.
- CI caches `bin/` under a per-job key, so `controller-gen`, `kustomize` and
  the envtest control-plane binaries are no longer re-downloaded in every job
  of every run.
- `.claude/SKILL.MD` rewritten so each rule and rationale is stated exactly
  once, and so it links to `Makefile` and `.github/workflows/ci.yml` rather
  than embedding copies that had already drifted from them.

[Unreleased]: https://github.com/partofaplan/kado-operator/compare/main...develop
