module github.com/telekom/das-schiff-network-operator

go 1.16

require (
	github.com/cilium/ebpf v0.9.1
	github.com/coreos/go-iptables v0.6.0
	github.com/coreos/go-systemd/v22 v22.3.2
	github.com/go-logr/logr v0.4.0
	github.com/google/go-cmp v0.5.6
	github.com/onsi/ginkgo v1.16.4
	github.com/onsi/gomega v1.15.0
	github.com/prometheus/client_golang v1.11.1
	github.com/vishvananda/netlink v1.1.1-0.20211129163951-9ada19101fc5
	golang.org/x/sys v0.0.0-20210906170528-6f6e22806c34
	gopkg.in/yaml.v2 v2.4.0
	k8s.io/api v0.22.1
	k8s.io/apimachinery v0.22.1
	k8s.io/client-go v0.22.1
	k8s.io/utils v0.0.0-20210802155522-efc7438f0176
	sigs.k8s.io/controller-runtime v0.10.0
)
