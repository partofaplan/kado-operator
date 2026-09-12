# Build the manager binary.
#
# The builder runs on the BUILD machine's native architecture and cross-compiles
# to the target via GOARCH. Without --platform=$BUILDPLATFORM, a multi-arch
# build would emulate the whole toolchain under QEMU for every foreign
# architecture, which is dramatically slower for no benefit: the Go compiler
# cross-compiles a static CGO-free binary natively.
FROM --platform=$BUILDPLATFORM golang:1.24 AS builder
ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/main.go cmd/main.go
COPY api/ api/
COPY internal/ internal/

# Build. TARGETOS/TARGETARCH are supplied by buildx per target platform; they
# fall back to the host's values for a plain `docker build`.
#
# No -a. kubebuilder scaffolds it, but it is a pre-Go-1.10 relic from when the
# shipped standard library archives were cgo-enabled. With Go's content-
# addressed build cache it forces every package including the stdlib to be
# rebuilt for no change in output: the binaries built with and without it are
# byte-identical (same SHA-256, same BuildID), still statically linked, still
# CGO-free, and still run under distroless/static. It only costs time — 84s vs
# 74s for two platforms on a cold builder.
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH} go build -o manager cmd/main.go

# Use distroless as minimal base image to package the manager binary
# Refer to https://github.com/GoogleContainerTools/distroless for more details
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/manager .
USER 65532:65532

ENTRYPOINT ["/manager"]
