# Build the KubeVirt vhost-user network binding plugin hook sidecar.
ARG GO_VERSION=1.25
FROM docker.io/library/golang:${GO_VERSION}-alpine AS builder

WORKDIR /workspace
# Copy the Go Modules manifests and cache dependencies before copying source so
# that source changes do not invalidate the downloaded module layer.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/kubevirt-vhostuser-hook/main.go main.go
COPY api/ api/
COPY pkg/ pkg/

ARG ldflags
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags "${ldflags}" -o kubevirt-vhostuser-hook main.go

# Distroless: the sidecar serves one gRPC socket and reads one downward-API
# file, so it needs no shell and no package manager.
#
# No USER is set on purpose. KubeVirt renders a hook sidecar's security context
# itself: it forces the VMI's uid for a non-root VMI and uid 0 otherwise, and
# the pod security context overrides the image's user either way. The only
# requirement is being able to create the socket in the hook directory KubeVirt
# mounts, which holds for both.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/kubevirt-vhostuser-hook /usr/local/bin/kubevirt-vhostuser-hook

# KubeVirt injects a binding-plugin sidecar with no command and no arguments,
# so everything the hook needs has to come from the image's own entrypoint.
ENTRYPOINT ["/usr/local/bin/kubevirt-vhostuser-hook"]
