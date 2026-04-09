# Build the network-sync binary
ARG GO_VERSION=1.25
FROM docker.io/library/golang:${GO_VERSION}-alpine AS builder

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/network-sync/main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

RUN CGO_ENABLED=0 GOOS=linux go build -a -o network-sync main.go

FROM alpine:3.21

WORKDIR /
COPY --from=builder /workspace/network-sync .
USER 65532:65532

ENTRYPOINT ["/network-sync"]
