# Build the frr-cra binary
ARG GO_VERSION=1.25
FROM --platform=$BUILDPLATFORM docker.io/library/golang:${GO_VERSION}-alpine AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace
COPY go.mod go.mod
COPY go.sum go.sum
RUN go mod download

COPY cmd/frr-cra/main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

RUN CGO_ENABLED=0 GOOS=${TARGETOS:-linux} GOARCH=${TARGETARCH:-$(go env GOARCH)} \
    go build -trimpath -ldflags="-s -w" -o frr-cra main.go

FROM docker.io/library/ubuntu:25.10

ENV FRRVER="frr-stable"
ARG DEBIAN_FRONTEND=noninteractive

# Install: dependencies, clean: apt cache, remove dir: cache, man, doc, change mod time of cache dir.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       software-properties-common \
       ca-certificates \
       curl \
       systemd \
       netplan.io \
       prometheus-node-exporter \
       iputils-ping \
       mtr-tiny \
    && apt-get clean \
    && rm -Rf /usr/share/doc && rm -Rf /usr/share/man \
    && rm -rf /var/lib/apt/lists/* \
    && touch -d "2 hours ago" /var/lib/apt/lists

RUN curl -fsSL https://deb.frrouting.org/frr/keys.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/frr.gpg && \
    echo "deb https://deb.frrouting.org/frr noble $FRRVER" | tee -a /etc/apt/sources.list.d/frr.list

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       frr \
       frr-pythontools \
    && apt-get clean \
    && rm -Rf /usr/share/doc && rm -Rf /usr/share/man \
    && rm -rf /var/lib/apt/lists/* \
    && touch -d "2 hours ago" /var/lib/apt/lists

RUN rm /usr/bin/udevadm && ln -s /usr/bin/true /usr/bin/udevadm
COPY ./docker/frr-cra.service /lib/systemd/system/
COPY ./docker/fix-vrf-rules.service /lib/systemd/system/
COPY ./docker/daemons /etc/frr/daemons
COPY ./docker/networkd.conf /etc/systemd/networkd.conf.d/cra.conf
COPY ./docker/10-cra.conf /etc/sysctl.d/10-cra.conf
COPY --from=builder /workspace/frr-cra /usr/local/bin/frr-cra
COPY LICENSE /licenses/LICENSE
COPY ./docker/frr-cra.env /etc/default/frr-cra
COPY ./docker/prometheus-node-exporter.env /etc/default/prometheus-node-exporter
COPY ./docker/hosts /etc/hosts
RUN systemctl enable frr-cra.service
RUN systemctl enable fix-vrf-rules.service
RUN systemctl enable prometheus-node-exporter.service

LABEL org.opencontainers.image.title="das-schiff-cra-frr" \
      org.opencontainers.image.source="https://github.com/telekom/das-schiff-network-operator" \
      org.opencontainers.image.vendor="Deutsche Telekom AG" \
      org.opencontainers.image.licenses="Apache-2.0"

VOLUME ["/sys/fs/cgroup", "/tmp", "/run"]
CMD ["/sbin/init"]
