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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"
)

const (
	// NetHealthcheckFile is default path for healtcheck config file.
	NetHealthcheckFile = "/opt/network-operator/net-healthcheck-config.yaml"
	// NodenameEnv is an env variable that holds Kubernetes node's name.
	NodenameEnv = "NODE_NAME"

	// NetworkOperatorReadyConditionType is the custom Node condition type signalling
	// that the network operator has successfully initialised networking on the node.
	NetworkOperatorReadyConditionType corev1.NodeConditionType = "NetworkOperatorReady"

	// Condition reasons.
	ReasonHealthChecksPassed   = "HealthChecksPassed"
	ReasonInterfaceCheckFailed = "InterfaceCheckFailed"
	ReasonReachabilityFailed   = "ReachabilityCheckFailed"
	ReasonAPIServerFailed      = "APIServerCheckFailed"
	// Additional agent specific reasons.
	ReasonNetplanInitFailed     = "NetplanInitializationFailed"
	ReasonNetplanApplyFailed    = "NetplanApplyFailed"
	ReasonVLANReconcileFailed   = "VLANReconcileFailed"
	ReasonLoopbackReconcileFail = "LoopbackReconcileFailed"
	ReasonConfigFetchFailed     = "ConfigFetchFailed"

	configEnv         = "OPERATOR_NETHEALTHCHECK_CONFIG"
	defaultTCPTimeout = 3
	defaultRetries    = 3
)

// HealthCheckerInterface defines the interface for health checking operations.
// This allows for easier testing by enabling mock implementations.
type HealthCheckerInterface interface {
	CheckInterfaces() error
	CheckReachability() error
	CheckAPIServer(ctx context.Context) error
	TaintsRemoved() bool
	RemoveTaints(ctx context.Context) error
	UpdateReadinessCondition(ctx context.Context, status corev1.ConditionStatus, reason, message string) error
}

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
// If taint removal fails due to a conflict (node was modified by another process),
// it logs a warning and returns nil - the taints will be removed on the next reconciliation.
// This prevents transient conflicts from invalidating the NodeNetworkConfig.
func (hc *HealthChecker) RemoveTaints(ctx context.Context) error {
	node := &corev1.Node{}
	err := hc.client.Get(ctx,
		types.NamespacedName{Name: os.Getenv(NodenameEnv)}, node)
	if err != nil {
		hc.Logger.Error(err, "error while getting node's info")
		return fmt.Errorf("error while getting node's info: %w", err)
	}

	updateNode := false
	for _, t := range hc.netConfig.Taints {
		for i, v := range node.Spec.Taints {
			if v.Key == t {
				node.Spec.Taints = append(node.Spec.Taints[:i], node.Spec.Taints[i+1:]...)
				updateNode = true
				break
			}
		}
	}

	if !updateNode {
		// No taints to remove, mark as done
		hc.taintsRemoved = true
		RecordTaintsRemoved(true)
		return nil
	}

	if err := hc.client.Update(ctx, node, &client.UpdateOptions{}); err != nil {
		// Conflict errors are transient - the node was modified by another process.
		// Don't fail the healthcheck; taints will be removed on next reconciliation.
		if strings.Contains(err.Error(), "the object has been modified") {
			hc.Logger.Info("node object was modified by another process, will retry taint removal on next reconciliation")
			return nil
		}
		hc.Logger.Error(err, "error while updating node")
		return fmt.Errorf("error while updating node: %w", err)
	}

	hc.taintsRemoved = true
	RecordTaintsRemoved(true)

	return nil
}

// UpdateReadinessCondition sets or updates the custom NetworkOperatorReady Node condition.
// status: corev1.ConditionTrue or corev1.ConditionFalse
// reason & message provide contextual information visible to users.
func (hc *HealthChecker) UpdateReadinessCondition(ctx context.Context, status corev1.ConditionStatus, reason, message string) error {
	node := &corev1.Node{}
	if err := hc.client.Get(ctx, types.NamespacedName{Name: os.Getenv(NodenameEnv)}, node); err != nil {
		return fmt.Errorf("error retrieving node to update readiness condition: %w", err)
	}

	now := metav1.Now()
	updated := false
	found := false
	for i := range node.Status.Conditions {
		c := &node.Status.Conditions[i]
		if c.Type != NetworkOperatorReadyConditionType { // reduce nesting per gocritic suggestion
			continue
		}
		found = true
		// Transition time only changes if status changed
		if c.Status != status {
			c.LastTransitionTime = now
		}
		c.LastHeartbeatTime = now
		c.Status = status
		c.Reason = reason
		c.Message = message
		updated = true
		break
	}
	if !found { // append new condition
		node.Status.Conditions = append(node.Status.Conditions, corev1.NodeCondition{
			Type:               NetworkOperatorReadyConditionType,
			Status:             status,
			Reason:             reason,
			Message:            message,
			LastHeartbeatTime:  now,
			LastTransitionTime: now,
		})
		updated = true
	}
	if updated {
		if err := hc.client.Status().Update(ctx, node); err != nil {
			return fmt.Errorf("error updating node readiness condition: %w", err)
		}
		// Record the node readiness condition metric
		RecordNodeReadinessCondition(status == corev1.ConditionTrue, reason)
	}
	return nil
}

// CheckInterfaces checks if all interfaces in the Interfaces slice are in UP state.
// Interface names support glob patterns (e.g., "eth*", "bond?", "br-*").
func (hc *HealthChecker) CheckInterfaces() error {
	start := time.Now()
	issuesFound := false

	// Expand glob patterns and check each matching interface
	interfaces, err := hc.expandInterfacePatterns(hc.netConfig.Interfaces)
	if err != nil {
		RecordHealthCheckResult(HealthCheckTypeInterfaces, false, time.Since(start))
		return fmt.Errorf("error expanding interface patterns: %w", err)
	}

	if len(interfaces) == 0 && len(hc.netConfig.Interfaces) > 0 {
		RecordHealthCheckResult(HealthCheckTypeInterfaces, false, time.Since(start))
		return errors.New("no interfaces matched the configured patterns")
	}

	for _, i := range interfaces {
		if err := hc.checkInterface(i); err != nil {
			hc.Logger.Error(err, "problem with network interface "+i)
			issuesFound = true
		}
	}
	duration := time.Since(start)
	if issuesFound {
		RecordHealthCheckResult(HealthCheckTypeInterfaces, false, duration)
		return errors.New("one or more problems with network interfaces found")
	}

	RecordHealthCheckResult(HealthCheckTypeInterfaces, true, duration)
	return nil
}

// expandInterfacePatterns expands glob patterns in interface names to actual interface names.
// If a pattern contains no glob characters, it is returned as-is.
func (hc *HealthChecker) expandInterfacePatterns(patterns []string) ([]string, error) {
	var result []string
	seen := make(map[string]bool)

	for _, pattern := range patterns {
		// Check if pattern contains glob characters
		if !containsGlobChar(pattern) {
			// No glob, use as-is
			if !seen[pattern] {
				result = append(result, pattern)
				seen[pattern] = true
			}
			continue
		}

		// Get all links and match against the pattern
		links, err := hc.toolkit.linkList()
		if err != nil {
			return nil, fmt.Errorf("error listing network interfaces: %w", err)
		}

		matched := false
		for _, link := range links {
			name := link.Attrs().Name
			match, err := filepath.Match(pattern, name)
			if err != nil {
				return nil, fmt.Errorf("invalid glob pattern %q: %w", pattern, err)
			}
			if match && !seen[name] {
				result = append(result, name)
				seen[name] = true
				matched = true
			}
		}

		if !matched {
			hc.Logger.Info("glob pattern matched no interfaces", "pattern", pattern)
		}
	}

	return result, nil
}

// containsGlobChar returns true if the string contains glob metacharacters.
func containsGlobChar(s string) bool {
	return strings.ContainsAny(s, "*?[")
}

// CheckReachability checks if all hosts in Reachability slice are reachable.
func (hc *HealthChecker) CheckReachability() error {
	start := time.Now()
	for _, i := range hc.netConfig.Reachability {
		if err := hc.checkReachabilityItemWithRetry(i); err != nil {
			if strings.Contains(err.Error(), "refused") {
				// refused connection will not return error, as host is reachable,
				// just actively refuses connections (e.g. port is blocked)
				continue
			}
			RecordHealthCheckResult(HealthCheckTypeReachability, false, time.Since(start))
			return err
		}
	}
	RecordHealthCheckResult(HealthCheckTypeReachability, true, time.Since(start))
	return nil
}

// CheckAPIServer checks if Kubernetes Api server is reachable from the pod.
func (hc HealthChecker) CheckAPIServer(ctx context.Context) error {
	start := time.Now()
	if err := hc.client.List(ctx, &corev1.NodeList{}); err != nil {
		RecordHealthCheckResult(HealthCheckTypeAPIServer, false, time.Since(start))
		return fmt.Errorf("unable to reach API server: %w", err)
	}
	RecordHealthCheckResult(HealthCheckTypeAPIServer, true, time.Since(start))
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
	Taints       []string              `yaml:"taints,omitempty"`

	// External sources for configuration fields.
	// These allow reading specific config sections from separate files (e.g., hostPath mounts).
	// If the file doesn't exist, it is silently ignored (graceful degradation).
	InterfacesFile   string `yaml:"interfacesFile,omitempty"`
	ReachabilityFile string `yaml:"reachabilityFile,omitempty"`
	TaintsFile       string `yaml:"taintsFile,omitempty"`
}

type netReachabilityItem struct {
	Host string `yaml:"host"`
	Port int    `yaml:"port"`
}

// LoadConfig loads healtcheck config from file and merges external sources.
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

	// Load external sources and merge into config
	if err := config.loadExternalSources(); err != nil {
		return nil, err
	}

	return config, nil
}

// loadExternalSources reads configuration from external files and merges them into the config.
// Missing files are silently ignored (graceful degradation).
func (c *NetHealthcheckConfig) loadExternalSources() error {
	// Load interfaces from external file
	if c.InterfacesFile != "" {
		interfaces, err := loadStringListFromFile(c.InterfacesFile)
		if err != nil {
			return fmt.Errorf("error loading interfaces from %s: %w", c.InterfacesFile, err)
		}
		if interfaces != nil {
			c.Interfaces = append(c.Interfaces, interfaces...)
		}
	}

	// Load reachability from external file
	if c.ReachabilityFile != "" {
		reachability, err := loadReachabilityFromFile(c.ReachabilityFile)
		if err != nil {
			return fmt.Errorf("error loading reachability from %s: %w", c.ReachabilityFile, err)
		}
		if reachability != nil {
			c.Reachability = append(c.Reachability, reachability...)
		}
	}

	// Load taints from external file
	if c.TaintsFile != "" {
		taints, err := loadStringListFromFile(c.TaintsFile)
		if err != nil {
			return fmt.Errorf("error loading taints from %s: %w", c.TaintsFile, err)
		}
		if taints != nil {
			c.Taints = append(c.Taints, taints...)
		}
	}

	return nil
}

// loadStringListFromFile reads a YAML file containing a string list.
// Returns nil (not an error) if the file doesn't exist.
func loadStringListFromFile(path string) ([]string, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist - graceful degradation
			return nil, nil
		}
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	var items []string
	if err := yaml.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("error unmarshalling file: %w", err)
	}

	return items, nil
}

// loadReachabilityFromFile reads a YAML file containing reachability items.
// Returns nil (not an error) if the file doesn't exist.
func loadReachabilityFromFile(path string) ([]netReachabilityItem, error) {
	data, err := os.ReadFile(filepath.Clean(path))
	if err != nil {
		if os.IsNotExist(err) {
			// File doesn't exist - graceful degradation
			return nil, nil
		}
		return nil, fmt.Errorf("error reading file: %w", err)
	}

	var items []netReachabilityItem
	if err := yaml.Unmarshal(data, &items); err != nil {
		return nil, fmt.Errorf("error unmarshalling file: %w", err)
	}

	return items, nil
}

// Toolkit is a helper structure that holds interfaces and functions used by HealthChecker.
type Toolkit struct {
	linkByName func(name string) (netlink.Link, error)
	linkList   func() ([]netlink.Link, error)
	tcpDialer  TCPDialerInterface
}

// NewHealthCheckToolkit returns new HealthCheckToolkit.
func NewHealthCheckToolkit(linkByName func(name string) (netlink.Link, error), linkList func() ([]netlink.Link, error), tcpDialer TCPDialerInterface) *Toolkit {
	return &Toolkit{
		linkByName: linkByName,
		linkList:   linkList,
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
func NewDefaultHealthcheckToolkit(tcpDialer TCPDialerInterface) *Toolkit {
	return NewHealthCheckToolkit(netlink.LinkByName, netlink.LinkList, tcpDialer)
}

type TCPDialerInterface interface {
	Dial(network string, address string) (net.Conn, error)
}
