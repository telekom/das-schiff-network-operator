# Build the manager binary
FROM docker.io/library/golang:1.21-alpine AS builder


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
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o operator main.go

FROM alpine:latest

WORKDIR /
COPY --from=builder /workspace/operator .
USER 65532:65532

ENTRYPOINT ["/operator"]
