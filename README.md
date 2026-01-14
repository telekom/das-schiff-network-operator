# network-operator
<p align='center'>
    <img alt="Kubernetes" src="https://img.shields.io/badge/kubernetes-326ce5.svg?&style=for-the-badge&logo=kubernetes&logoColor=white">
</p>

With our BGP-EVPN to the host architecture, `network-operator` is responsible for the configuration and monitoring of the host router according to the specified declarative state.

The project provides five components:
- One central `operator`, taking in desired state and deriving config revisions out of it. Further it controls the gradual rollout of the revisions.
  This is achieved by generating the `NodeNetworkConfig` and `NodeNetplanConfig` resources for each node, waiting for them to be provisioned successfully and then choosing the next node.
- A set of "agents" running as DaemonSets:
  - `agent-cra-frr`: Reading the `NodeNetworkConfig` for the respective node it is running on.
    It is responsible for configuring the so called _Containerized Routing Agent_ (CRA) of FRR.
  - `agent-netplan`: Taking the `NodeNetplanConfig` and applying it to the host network namespace.
  - `agent-hbn-l2`: This is an alternative to `agent-netplan`, providing a very simple binary to apply the needed configuration, a subset of the `netplan` functionality and spec.
- The _containerized routing agent_ (CRA), providing FRR and a small `cra-frr` configuration binary. This exposes an API to dynamically configure L2 and L3VNIs and reconfigure FRR.

## Node readiness signalling

During initialisation nodes may be tainted to prevent premature workload scheduling while the network stack is being prepared. The taints to be removed are configurable via the healthcheck configuration file (`/opt/network-operator/net-healthcheck-config.yaml`) using the `taints` field:

```yaml
taints:
  - node.cloudprovider.kubernetes.io/uninitialized
  - node.t-caas.telekom.com/uninitialized
```

Once all health checks (interface state, reachability targets, API server access) pass, these configured taints are removed and a custom Node condition is created or updated:

```text
Type:    NetworkOperatorReady
Status:  True | False
Reason:  <see below>
Message: Human readable description of the last evaluation.
```

Common reasons:
- `HealthChecksPassed` – all checks succeeded
- `InterfaceCheckFailed` – one or more configured interfaces are not UP (supports glob patterns like `eth*`, `bond?`)
- `ReachabilityCheckFailed` – a configured reachability target is unreachable
- `APIServerCheckFailed` – cannot reach the Kubernetes API server

Agent-specific reasons:
- `NetplanInitializationFailed` / `NetplanApplyFailed` – netplan agent errors
- `VLANReconcileFailed` / `LoopbackReconcileFailed` – hbn-l2 agent errors
- `ConfigFetchFailed` – failed to fetch node configuration

This allows cluster operators and higher level automation to rely on a standard Node condition instead of only watching for taint removal. When any health check fails the condition is set to `False` with the corresponding reason; taints are not re-applied (to avoid disruptive rescheduling) but the condition provides ongoing status.

Configuration can also be split across multiple files using external sources (`interfacesFile`, `reachabilityFile`, `taintsFile`). This is useful for reading parts of the configuration from hostPath mounts or ConfigMaps. Missing files are silently ignored, enabling graceful fallback with `FileOrCreate` volume strategies.

A sample healthcheck configuration file is provided at [config/samples/net-healthcheck-config.yaml](config/samples/net-healthcheck-config.yaml).

## Prometheus metrics

The network operator exposes Prometheus metrics for observability. See [docs/metrics.md](docs/metrics.md) for the complete list of metrics.

![schematic diagram of cra-frr and the host ns](./docs/cra-frr.png)

## Deploying the operator
There are two possibilities to deploy the operator:
1. Run everything through Kubernetes, providing BGP-EVPN services to an existing cluster
2. Start `cra-frr` as a standalone container during provisioning and deploy the other components later with Kubernetes.

```
WIP
```

## License

This project is licensed under Apache License Version 2.0.

Copyright (c) 2022-2025 Deutsche Telekom AG.

Licensed under the Apache License, Version 2.0 (the "License"); you may not use this file except in compliance with the License.

You may obtain a copy of the License at https://www.apache.org/licenses/LICENSE-2.0.

Unless required by applicable law or agreed to in writing, software distributed under the License is distributed on an "AS IS" BASIS, WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied. See the LICENSE for the specific language governing permissions and limitations under the License.
