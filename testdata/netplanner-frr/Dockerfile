FROM ubuntu:22.04
LABEL maintainer="Marcel Fest"

ENV FRRVER="frr-stable"
ENV NETPLANNER_VERSION="0.13.1"
ARG DEBIAN_FRONTEND=noninteractive

COPY ./netplanner.service /lib/systemd/system/

# Install: dependencies, clean: apt cache, remove dir: cache, man, doc, change mod time of cache dir.
RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       software-properties-common ca-certificates \
       curl \
       systemd \
    && apt-get clean \
    && rm -Rf /usr/share/doc && rm -Rf /usr/share/man \
    && rm -rf /var/lib/apt/lists/* \
    && touch -d "2 hours ago" /var/lib/apt/lists

RUN curl -fsSL https://deb.frrouting.org/frr/keys.asc | gpg --dearmor > /etc/apt/trusted.gpg.d/frr.gpg && \
    echo "deb https://deb.frrouting.org/frr $(lsb_release -s -c) $FRRVER" | tee -a /etc/apt/sources.list.d/frr.list

RUN curl -fsSL "https://github.com/telekom/netplanner/releases/download/${NETPLANNER_VERSION}/netplanner-linux-amd64-${NETPLANNER_VERSION}" --output "/usr/local/sbin/netplanner" && \
    chmod +x "/usr/local/sbin/netplanner"

RUN apt-get update \
    && apt-get install -y --no-install-recommends \
       frr \
    && apt-get clean \
    && rm -Rf /usr/share/doc && rm -Rf /usr/share/man \
    && rm -rf /var/lib/apt/lists/* \
    && touch -d "2 hours ago" /var/lib/apt/lists

RUN ln -s /lib/systemd/system/netplanner.service /etc/systemd/system/multi-user.target.wants/netplanner.service \
    && ln -s /lib/systemd/system/systemd-networkd.service /etc/systemd/system/dbus-org.freedesktop.network1.service \
    && ln -s /lib/systemd/system/systemd-networkd.service /etc/systemd/system/multi-user.target.wants/systemd-networkd.service \
    && mkdir -p /etc/systemd/system/sockets.target.wants \
    && ln -s /lib/systemd/system/systemd-networkd.socket /etc/systemd/system/sockets.target.wants/systemd-networkd.socket \
    && ln -s /lib/systemd/system/systemd-network-generator.service /etc/systemd/system/sysinit.target.wants/systemd-network-generator.service \
    && mkdir -p /etc/systemd/system/network-online.target.wants \
    && ln -s /lib/systemd/system/systemd-networkd-wait-online.service /etc/systemd/system/network-online.target.wants/systemd-networkd-wait-online.service \
    && mkdir -p /etc/netplanner

VOLUME ["/sys/fs/cgroup", "/tmp", "/run"]
CMD ["/sbin/init"]