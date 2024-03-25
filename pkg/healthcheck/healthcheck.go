// Package healthcheck is used for basic networking healthcheck.
package healthcheck

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/go-logr/logr"
	"github.com/vishvananda/netlink"
	"gopkg.in/yaml.v2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// NetHealthcheckFile is default path for healtcheck config file.
	NetHealthcheckFile = "/opt/network-operator/net-healthcheck-config.yaml"
	// NodenameEnv is an env variable that holds Kubernetes node's name.
	NodenameEnv = "NODE_NAME"

	configEnv         = "OPERATOR_NETHEALTHCHECK_CONFIG"
	defaultTCPTimeout = 3
	defaultRetries    = 3
)

var (
	// InitTaints is a list of taints that are applied during initialisation of a node
	// to prevent workloads being scheduled before the network stack is initialised.
	InitTaints = []string{
		"node.schiff.telekom.de/uninitialized",
		"node.cloudprovider.kubernetes.io/uninitialized",
	}
)

// HealthChecker is a struct that holds data required for networking healthcheck.
type HealthChecker struct {
	client        client.Client
	taintsRemoved bool
	logr.Logger
	netConfig *NetHealthcheckConfig
	toolkit   *Toolkit
	retries   int
}

// NewHealthChecker creates new HealthChecker.
func NewHealthChecker(clusterClient client.Client, toolkit *Toolkit, netconf *NetHealthcheckConfig) (*HealthChecker, error) {
	var retries int
	if netconf.Retries <= 0 {
		retries = defaultRetries
	} else {
		retries = netconf.Retries
	}

	return &HealthChecker{
		client:        clusterClient,
		taintsRemoved: false,
		Logger:        log.Log.WithName("HealthCheck"),
		netConfig:     netconf,
		toolkit:       toolkit,
		retries:       retries,
	}, nil
}

// TaintsRemoved returns value of isNetworkingHealthly bool.
func (hc *HealthChecker) TaintsRemoved() bool {
	return hc.taintsRemoved
}

// RemoveTaints removes taint from the node.
func (hc *HealthChecker) RemoveTaints(ctx context.Context) error {
	node := &corev1.Node{}
	err := hc.client.Get(ctx,
		types.NamespacedName{Name: os.Getenv(NodenameEnv)}, node)
	if err != nil {
		hc.Logger.Error(err, "error while getting node's info")
		return fmt.Errorf("error while getting node's info: %w", err)
	}

	updateNode := false
	for _, t := range InitTaints {
		for i, v := range node.Spec.Taints {
			if v.Key == t {
				node.Spec.Taints = append(node.Spec.Taints[:i], node.Spec.Taints[i+1:]...)
				updateNode = true
				break
			}
		}
	}
	if updateNode {
		if err := hc.client.Update(ctx, node, &client.UpdateOptions{}); err != nil {
			hc.Logger.Error(err, "")
			return fmt.Errorf("error while updating node: %w", err)
		}
	}

	hc.taintsRemoved = true

	return nil
}

// IsFRRActive checks if FRR daemon is in active and running state.
func (hc *HealthChecker) IsFRRActive() (bool, error) {
	activeState, subState, err := hc.toolkit.frr.GetStatusFRR()
	if err != nil {
		return false, fmt.Errorf("error while ugetting FRR's status: %w", err)
	}

	if activeState != "active" || subState != "running" {
		return false, errors.New("FRR is inactive with ActiveState=" + activeState + " and SubState=" + subState)
	}

	return true, nil
}

// CheckInterfaces checks if all interfaces in the Interfaces slice are in UP state.
func (hc *HealthChecker) CheckInterfaces() error {
	issuesFound := false
	for _, i := range hc.netConfig.Interfaces {
		if err := hc.checkInterface(i); err != nil {
			hc.Logger.Error(err, "problem with network interface "+i)
			issuesFound = true
		}
	}
	if issuesFound {
		return errors.New("one or more problems with network interfaces found")
	}

	return nil
}

// CheckReachability checks if all hosts in Reachability slice are reachable.
func (hc *HealthChecker) CheckReachability() error {
	for _, i := range hc.netConfig.Reachability {
		if err := hc.checkReachabilityItemWithRetry(i); err != nil {
			if strings.Contains(err.Error(), "refused") {
				// refused connection will not return error, as host is reachable,
				// just actively refuses connections (e.g. port is blocked)
				continue
			}
			return err
		}
	}
	return nil
}

// CheckAPIServer checks if Kubernetes Api server is reachable from the pod.
func (hc HealthChecker) CheckAPIServer(ctx context.Context) error {
	if err := hc.client.List(ctx, &corev1.NodeList{}); err != nil {
		return fmt.Errorf("unable to reach API server: %w", err)
	}
	return nil
}

func (hc *HealthChecker) checkInterface(intf string) error {
	link, err := hc.toolkit.linkByName(intf)
	if err != nil {
		return err
	}
	if link.Attrs().OperState != netlink.OperUp {
		return errors.New("link " + intf + " is not up - current state: " + link.Attrs().OperState.String())
	}
	return nil
}

func (hc *HealthChecker) checkReachabilityItem(r netReachabilityItem) error {
	target := r.Host + ":" + strconv.Itoa(r.Port)
	conn, err := hc.toolkit.tcpDialer.Dial("tcp", target)
	if err != nil {
		return fmt.Errorf("error trying to connect to %s: %w", target, err)
	}
	if conn != nil {
		if err = conn.Close(); err != nil {
			return fmt.Errorf("error closing connection: %w", err)
		}
	}
	return nil
}

func (hc *HealthChecker) checkReachabilityItemWithRetry(r netReachabilityItem) error {
	var err error
	for i := 0; i < hc.retries; i++ {
		err = hc.checkReachabilityItem(r)
		if err == nil {
			return nil
		}
		if strings.Contains(err.Error(), "refused") || i >= hc.retries-1 {
			break
		}
	}
	return err
}

// NetHealthcheckConfig is a struct that holds healtcheck config.
type NetHealthcheckConfig struct {
	Interfaces   []string              `yaml:"interfaces,omitempty"`
	Reachability []netReachabilityItem `yaml:"reachability,omitempty"`
	Timeout      string                `yaml:"timeout,omitempty"`
	Retries      int                   `yaml:"retries,omitempty"`
}

type netReachabilityItem struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// LoadConfig loads healtcheck config from file.
func LoadConfig(configFile string) (*NetHealthcheckConfig, error) {
	config := &NetHealthcheckConfig{}

	isMandatory := false
	if val := os.Getenv(configEnv); val != "" {
		isMandatory = true
		configFile = val
	}

	configExists := false

	read, err := os.ReadFile(filepath.Clean(configFile))
	if err != nil {
		if isMandatory {
			return nil, fmt.Errorf("error opening config file: %w", err)
		}
	} else {
		configExists = true
	}

	if configExists {
		err = yaml.Unmarshal(read, &config)
		if err != nil {
			return nil, fmt.Errorf("error unmarshalling config file: %w", err)
		}
	}

	return config, nil
}

// Toolkit is a helper structure that holds interfaces and functions used by HealthChecker.
type Toolkit struct {
	frr        FRRInterface
	linkByName func(name string) (netlink.Link, error)
	tcpDialer  TCPDialerInterface
}

// NewHealthCheckToolkit returns new HealthCheckToolkit.
func NewHealthCheckToolkit(frr FRRInterface, linkByName func(name string) (netlink.Link, error), tcpDialer TCPDialerInterface) *Toolkit {
	return &Toolkit{
		frr:        frr,
		linkByName: linkByName,
		tcpDialer:  tcpDialer,
	}
}

// NewTCPDialer returns new tcpDialerInterface.
func NewTCPDialer(dialerTimeout string) TCPDialerInterface {
	timeout, err := time.ParseDuration(dialerTimeout)
	logger := log.Log.WithName("HealthCheck - TCP dialer")
	if err != nil {
		logger.Info("unable to parse TCP dialer timeout provided in HealtCheck config as duration", "timeout", timeout, "value", dialerTimeout)
		seconds, err := strconv.Atoi(dialerTimeout)
		if err != nil {
			logger.Info("unable to parse TCP dialer timeout provided in HealtCheck config as integer, will use default Timeout", "timeout", fmt.Sprintf("%ds", defaultTCPTimeout), "value", dialerTimeout)
			timeout = time.Second * defaultTCPTimeout
		} else {
			timeout = time.Second * time.Duration(seconds)
		}
	}
	return &net.Dialer{Timeout: timeout}
}

// NewDefaultHealthcheckToolkit returns.
func NewDefaultHealthcheckToolkit(frr FRRInterface, tcpDialer TCPDialerInterface) *Toolkit {
	return NewHealthCheckToolkit(frr, netlink.LinkByName, tcpDialer)
}

//go:generate mockgen -destination ./mock/mock_healthcheck.go . FRRInterface,TCPDialerInterface
type FRRInterface interface {
	GetStatusFRR() (string, string, error)
}

type TCPDialerInterface interface {
	Dial(network string, address string) (net.Conn, error)
}
