# Build the manager binary
ARG GO_VERSION=1.25
FROM --platform=$BUILDPLATFORM docker.io/library/golang:${GO_VERSION}-alpine AS builder

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
COPY cmd/operator/main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

# Build
RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags="-s -w" -o manager main.go

# Runtime stage (distroless)
FROM gcr.io/distroless/static-debian12

LABEL org.opencontainers.image.title="das-schiff-network-operator" \
      org.opencontainers.image.description="Kubernetes network operator for das Schiff platform" \
      org.opencontainers.image.url="https://github.com/telekom/das-schiff-network-operator" \
      org.opencontainers.image.source="https://github.com/telekom/das-schiff-network-operator" \
      org.opencontainers.image.vendor="Deutsche Telekom AG" \
      org.opencontainers.image.licenses="Apache-2.0" \
      org.opencontainers.image.base.name="gcr.io/distroless/static-debian12"

WORKDIR /
COPY --from=builder /workspace/manager .
COPY LICENSE /licenses/LICENSE
COPY NOTICE /licenses/NOTICE
USER 65532:65532

ENTRYPOINT ["/manager"]
