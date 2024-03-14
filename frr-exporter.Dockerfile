ARG FRR_VERSION="9.1.0"
ARG REGISTRY="quay.io"
# Build the manager binary
FROM docker.io/library/golang:1.21-alpine as builder

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
COPY cmd/frr-exporter/main.go main.go
COPY pkg/ pkg/

# Build router
COPY bpf/ bpf/
RUN cd pkg/bpf/ && go generate

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o frr-exporter main.go


FROM ${REGISTRY}/frrouting/frr:${FRR_VERSION}

RUN apk add --no-cache frr

WORKDIR /
COPY --from=builder /workspace/frr-exporter .
## Needs to run as root
##  vtysh is required to have extended rights 
## to be able to connect to vty sockets on the host
# USER 65532:65532

ENTRYPOINT ["/frr-exporter"]
