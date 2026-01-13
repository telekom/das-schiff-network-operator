# Prometheus Metrics

The network operator exposes Prometheus metrics for observability. All metrics use the `nwop_` namespace.

## Health check metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nwop_healthcheck_status` | Gauge | `check` | Health check status (1=passing, 0=failing) |
| `nwop_healthcheck_last_success_timestamp_seconds` | Gauge | `check` | Unix timestamp of last successful check |
| `nwop_healthcheck_duration_seconds` | Histogram | `check` | Duration of health check execution |
| `nwop_healthcheck_node_ready` | Gauge | `reason` | Node readiness condition (1=ready, 0=not ready) |
| `nwop_healthcheck_taints_removed` | Gauge | – | Whether init taints have been removed (1=yes, 0=no) |

The `check` label can be `interfaces`, `reachability`, or `apiserver`.

## Scrape metrics

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nwop_scrape_collector_duration_seconds` | Gauge | `collector` | Duration of a collector scrape |
| `nwop_scrape_collector_success` | Gauge | `collector` | Whether a collector succeeded (1=yes, 0=no) |

## Netlink collector metrics (enabled by default)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nwop_netlink_routes_fib` | Gauge | `table`, `vrf`, `protocol`, `address_family` | Number of routes in the Linux dataplane |
| `nwop_netlink_neighbors` | Gauge | `interface`, `address_family`, `flags`, `status` | Number of neighbors in the Linux dataplane |

## FRR collector metrics (disabled by default)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nwop_frr_routes_fib` | Gauge | `table`, `vrf`, `protocol`, `address_family` | Routes in FRR FIB |
| `nwop_frr_routes_rib` | Gauge | `table`, `vrf`, `protocol`, `address_family` | Routes in FRR RIB |
| `nwop_frr_vni_state` | Gauge | `table`, `vrf`, `svi`, `vtep` | VRF/EVPN VNI state |
| `nwop_frr_bgp_uptime_seconds_total` | Counter | `vrf`, `as`, `peer_name`, `peer_host`, `ip_family`, `message_type`, `subsequent_family`, `remote_as` | BGP session uptime |
| `nwop_frr_bgp_status` | Gauge | (same as above) | BGP session status (1=established, 0=down) |
| `nwop_frr_bgp_prefixes_received_total` | Counter | (same as above) | Prefixes received from peer |
| `nwop_frr_bgp_prefixes_transmitted_total` | Counter | (same as above) | Prefixes transmitted to peer |
| `nwop_frr_bgp_messages_received_total` | Counter | (same as above) | Messages received from peer |
| `nwop_frr_bgp_messages_transmitted_total` | Counter | (same as above) | Messages transmitted to peer |

## VSR collector metrics (disabled by default)

| Metric | Type | Labels | Description |
|--------|------|--------|-------------|
| `nwop_vsr_routes_fib` | Gauge | `table`, `vrf`, `protocol`, `address_family` | Routes in VSR FIB |
| `nwop_vsr_routes_rib` | Gauge | `table`, `vrf`, `protocol`, `address_family` | Routes in VSR RIB |
| `nwop_vsr_vni_state` | Gauge | `table`, `vrf`, `svi`, `vtep` | VRF VNI state |
| `nwop_vsr_bgp_uptime_seconds_total` | Counter | `vrf`, `as`, `peer_name`, `ip_family`, `message_type`, `subsequent_family`, `remote_as` | BGP session uptime |
| `nwop_vsr_bgp_status` | Gauge | (same as above) | BGP session status |
| `nwop_vsr_bgp_prefixes_received_total` | Counter | (same as above) | Prefixes received from peer |
| `nwop_vsr_bgp_prefixes_transmitted_total` | Counter | (same as above) | Prefixes transmitted to peer |
| `nwop_vsr_bgp_messages_received_total` | Counter | (same as above) | Messages received from peer |
| `nwop_vsr_bgp_messages_transmitted_total` | Counter | (same as above) | Messages transmitted to peer |
| `nwop_vsr_neighbors` | Gauge | `interface`, `address_family`, `flags`, `status` | Neighbor count |
