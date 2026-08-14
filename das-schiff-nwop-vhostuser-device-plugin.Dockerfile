# Build the vhostuser-device-plugin binary.
ARG GO_VERSION=1.25
FROM docker.io/library/golang:${GO_VERSION}-alpine AS builder

WORKDIR /workspace
# Copy the Go Modules manifests and cache dependencies before copying source so
# that source changes do not invalidate the downloaded module layer.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/vhostuser-device-plugin/main.go main.go
COPY api/ api/
COPY pkg/ pkg/

ARG ldflags
RUN CGO_ENABLED=0 GOOS=linux go build -a -ldflags "${ldflags}" -o vhostuser-device-plugin main.go

# Distroless: the plugin only opens unix sockets and creates directories, so it
# needs no shell and no package manager. It does need to run as root, because the
# per-device socket directories are chowned to the qemu uid.
FROM gcr.io/distroless/static:nonroot
WORKDIR /
COPY --from=builder /workspace/vhostuser-device-plugin /usr/local/bin/vhostuser-device-plugin
USER 0:0

ENTRYPOINT ["/usr/local/bin/vhostuser-device-plugin"]
