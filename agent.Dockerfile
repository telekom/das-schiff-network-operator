# Build the manager binary
FROM docker.io/library/golang:1.21-alpine AS builder


WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Build router
RUN apk add llvm clang linux-headers libbpf-dev musl-dev

# Copy the go source
COPY cmd/agent/main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

# Build router
COPY bpf/ bpf/
RUN cd pkg/bpf/ && go generate

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o agent main.go

FROM alpine:latest

RUN apk add --no-cache iptables ip6tables

WORKDIR /
COPY --from=builder /workspace/agent .
USER 65532:65532

ENTRYPOINT ["/agent"]
