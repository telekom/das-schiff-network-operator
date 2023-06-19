module github.com/telekom/das-schiff-network-operator

go 1.16

require (
	github.com/cilium/ebpf v0.9.1
	github.com/coreos/go-iptables v0.6.0
	github.com/coreos/go-systemd/v22 v22.4.0
	github.com/go-logr/logr v1.2.4
	github.com/google/go-cmp v0.5.9
	github.com/imdario/mergo v0.3.12 // indirect
	github.com/onsi/ginkgo v1.16.4
	github.com/onsi/gomega v1.27.7
	github.com/prometheus/client_golang v1.15.1
	github.com/vishvananda/netlink v1.1.1-0.20211129163951-9ada19101fc5
	golang.org/x/sys v0.8.0
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.27.2
	k8s.io/apimachinery v0.27.2
	k8s.io/client-go v0.27.2
	k8s.io/utils v0.0.0-20230209194617-a36077c30491
	sigs.k8s.io/controller-runtime v0.15.0
)
