# NAT64 gateway image for das-schiff-network-operator e2e tests.
# Provides DNS64 (unbound) + NAT64 (tayga).
FROM ubuntu:24.04

RUN apt-get update && \
    DEBIAN_FRONTEND=noninteractive apt-get install -y \
      tayga unbound iproute2 iptables && \
    apt-get clean && rm -rf /var/lib/apt/lists/*

COPY start-nat64.sh /start-nat64.sh
RUN chmod +x /start-nat64.sh

CMD ["/start-nat64.sh"]
