# Build the agent binary
ARG GO_VERSION=1.25
ARG BUILDPLATFORM
FROM --platform=$BUILDPLATFORM docker.io/library/golang:${GO_VERSION}-alpine AS builder

ARG TARGETOS
ARG TARGETARCH
ARG ldflags="-s -w"

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/agent-cra-vsr/main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags="${ldflags}" -o agent main.go

FROM gcr.io/distroless/static-debian12

LABEL org.opencontainers.image.title="das-schiff-nwop-agent-cra-vsr" \
      org.opencontainers.image.source="https://github.com/telekom/das-schiff-network-operator" \
      org.opencontainers.image.vendor="Deutsche Telekom AG" \
      org.opencontainers.image.licenses="Apache-2.0"

WORKDIR /
COPY --from=builder /workspace/agent .
COPY LICENSE /licenses/LICENSE
COPY NOTICE /licenses/NOTICE
USER 65532:65532

ENTRYPOINT ["/agent"]
