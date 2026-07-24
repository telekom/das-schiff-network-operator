# Build the cni-routed binary.
ARG GO_VERSION=1.25
FROM docker.io/library/golang:${GO_VERSION}-alpine AS builder

WORKDIR /workspace
# Copy the Go Modules manifests and cache dependencies before copying source so
# that source changes do not invalidate the downloaded module layer.
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

# Copy the go source needed to build the CNI plugin.
COPY cmd/cni-routed/main.go main.go
COPY api/ api/
COPY pkg/ pkg/

# Build a static binary (the plugin runs on the host, not in this image).
RUN CGO_ENABLED=0 GOOS=linux go build -a -o cni-routed main.go

# The installer image copies the plugin binary onto the node's CNI bin dir and
# writes the CNI conflist, then sleeps (run as a DaemonSet init/side container).
FROM alpine:latest

WORKDIR /
COPY --from=builder /workspace/cni-routed /usr/local/bin/cni-routed
COPY e2e/kubevirt/install/install-cni.sh /usr/local/bin/install-cni.sh
RUN chmod +x /usr/local/bin/install-cni.sh

ENTRYPOINT ["/usr/local/bin/install-cni.sh"]
