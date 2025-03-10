# Build the manager binary
FROM docker.io/library/golang:1.23-alpine AS builder


WORKDIR /workspace
# Copy the Go Modules manifests
COPY go.mod go.mod
COPY go.sum go.sum
# cache deps before building and copying source so that we don't need to re-download as much
# and so that source changes don't invalidate our downloaded layer
RUN go mod download

# Copy the go source
COPY cmd/frr-cra/main.go main.go
COPY api/ api/
COPY controllers/ controllers/
COPY pkg/ pkg/

# Build
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -o frr-cra main.go

FROM docker.io/library/ubuntu:24.10

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
COPY ./docker/node-exporter-override.conf /etc/systemd/system/prometheus-node-exporter.service.d/override.conf
RUN systemctl enable frr-cra.service
RUN systemctl enable fix-vrf-rules.service
RUN systemctl enable prometheus-node-exporter.service

VOLUME ["/sys/fs/cgroup", "/tmp", "/run"]
CMD ["/sbin/init"]
