# Test runner image for das-schiff-network-operator e2e tests.
# Includes ginkgo CLI, kubectl, Go, and network diagnostic tools.
FROM golang:1.24 AS builder

# Install ginkgo CLI
RUN go install github.com/onsi/ginkgo/v2/ginkgo@latest

FROM ubuntu:24.04

# Runtime tools
RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
      curl wget iputils-ping mtr-tiny \
      dnsutils iproute2 jq \
      docker.io && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

# kubectl
RUN curl -fsSL "https://dl.k8s.io/release/v1.35.1/bin/linux/amd64/kubectl" \
      -o /usr/local/bin/kubectl && chmod +x /usr/local/bin/kubectl

# ginkgo + go (for running tests)
COPY --from=builder /usr/local/go /usr/local/go
COPY --from=builder /go/bin/ginkgo /usr/local/bin/ginkgo
ENV PATH="/usr/local/go/bin:${PATH}"
ENV GOPATH="/go"

WORKDIR /e2etests
CMD ["sleep", "infinity"]
