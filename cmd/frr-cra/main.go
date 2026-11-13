package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/log/zap"

	"github.com/telekom/das-schiff-network-operator/pkg/bpf"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/cra-frr"
	"github.com/telekom/das-schiff-network-operator/pkg/frr"
	"github.com/telekom/das-schiff-network-operator/pkg/logging"
	"github.com/telekom/das-schiff-network-operator/pkg/monitoring"
	"github.com/telekom/das-schiff-network-operator/pkg/neighborsync"
	"github.com/telekom/das-schiff-network-operator/pkg/nl"
	"github.com/telekom/das-schiff-network-operator/pkg/utils"
)

const (
	serverCert = "/etc/cra/cert.pem"
	serverKey  = "/etc/cra/key.pem"

	frrConfigPath = "/etc/frr/frr.conf"

	baseConfigPath = "/etc/cra/base-config.yaml"

	defaultSleep = 2 * time.Second
)

var (
	frrManager     *frr.Manager
	nlManager      *nl.Manager
	neighborSyncer *neighborsync.NeighborSync
	baseConfig     *config.BaseConfig
	applyMu        sync.Mutex // serializes applyConfig to prevent concurrent FRR/netlink races
	logger         = ctrl.Log.WithName("frr-cra")
)

// sanitizeLog removes newlines and carriage returns from log messages
// to prevent log injection attacks (CodeQL: Log entries created from user input).
var logSanitizer = strings.NewReplacer("\n", "", "\r", "")

func sanitizeErr(err error) error {
	return errors.New(logSanitizer.Replace(err.Error()))
}

func deleteLayer2(cfg *nl.NetlinkConfiguration) error {
	existing, err := nlManager.ListL2()
	if err != nil {
		return fmt.Errorf("error listing L2: %w", err)
	}

	vrfsToDelete, err := getVRFsToDelete(cfg)
	if err != nil {
		return fmt.Errorf("error getting VRFs to delete: %w", err)
	}

	for i := range existing {
		needsDeletion := true
		for j := range cfg.Layer2s {
			if existing[i].VlanID == cfg.Layer2s[j].VlanID {
				needsDeletion = false
				break
			}
		}
		for j := range vrfsToDelete {
			if existing[i].VRF == vrfsToDelete[j].Name {
				needsDeletion = true
				break
			}
		}
		if needsDeletion {
			if err := nlManager.CleanupL2(&existing[i]); len(err) > 0 {
				return fmt.Errorf("error deleting L2 (VLAN: %d): %v", existing[i].VlanID, err)
			}
		}
	}

	return nil
}

func createLayer2(cfg *nl.NetlinkConfiguration) error {
	existing, err := nlManager.ListL2()
	if err != nil {
		return fmt.Errorf("error listing L2: %w", err)
	}

	var currentConfig *nl.Layer2Information
	for i := range cfg.Layer2s {
		currentConfig = nil
		for j := range existing {
			if existing[j].VlanID == cfg.Layer2s[i].VlanID {
				currentConfig = &existing[j]
				break
			}
		}
		if currentConfig == nil {
			if err := nlManager.CreateL2(&cfg.Layer2s[i]); err != nil {
				return fmt.Errorf("error creating L2 (VLAN: %d): %w", cfg.Layer2s[i].VlanID, err)
			}
		} else {
			if err := nlManager.ReconcileL2(currentConfig, &cfg.Layer2s[i]); err != nil {
				return fmt.Errorf("error reconciling L2 (VLAN: %d): %w", cfg.Layer2s[i].VlanID, err)
			}
		}
	}

	return nil
}

func reconcileNeighborSync(cfg *nl.NetlinkConfiguration) {
	if neighborSyncer == nil {
		return
	}
	for i := range cfg.Layer2s {
		l2 := &cfg.Layer2s[i]
		if len(l2.AnycastGateways) == 0 {
			continue
		}

		bridgeName := fmt.Sprintf("l2.%d", l2.VlanID)
		bridge, err := netlink.LinkByName(bridgeName)
		if err != nil {
			logger.Error(sanitizeErr(err), "neighborsync: bridge not found", "bridge", bridgeName)
			continue
		}
		bridgeIdx := bridge.Attrs().Index

		neighborSyncer.EnsureARPRefresh(bridgeIdx)

		vlanName := fmt.Sprintf("vlan.%d", l2.VlanID)
		vlanIntf, err := netlink.LinkByName(vlanName)
		if err != nil {
			logger.Error(sanitizeErr(err), "neighborsync: vlan interface not found", "interface", vlanName)
			continue
		}

		if err := neighborSyncer.EnsureNeighborSuppression(bridgeIdx, vlanIntf.Attrs().Index); err != nil {
			logger.Error(sanitizeErr(err), "neighborsync: failed to ensure neighbor suppression", "bridge", bridgeName)
		}
	}
}

const policyRoutePriority = 100

// reconcilePolicyRoutes installs ip rules for source-based routing.
// For each PolicyRoute, two rules are created:
//   - IPv4: iif <trunk> (e.g. hbn) — matches IPv4 packets on the bridge
//   - IPv6: iif <clusterVRF> (e.g. cluster) — required because the kernel's IPv6
//     receive path sets flowi6_iif to the VRF device, not the original ingress interface
func reconcilePolicyRoutes(routes []cra.PolicyRoute) error {
	desired := buildDesiredRules(routes)

	for _, family := range []int{unix.AF_INET, unix.AF_INET6} {
		existing, err := netlink.RuleList(family)
		if err != nil {
			return fmt.Errorf("error listing rules for family %d: %w", family, err)
		}

		if err := deleteStaleRules(existing, desired); err != nil {
			return err
		}
		if err := addMissingRules(existing, desired, family); err != nil {
			return err
		}
	}

	return nil
}

func deleteStaleRules(existing, desired []netlink.Rule) error {
	for i := range existing {
		if existing[i].Priority != policyRoutePriority {
			continue
		}
		if !containsRule(desired, &existing[i]) {
			if err := netlink.RuleDel(&existing[i]); err != nil {
				logger.Error(err, "policy-routes: failed to delete stale rule", "rule", existing[i].String())
			}
		}
	}
	return nil
}

func addMissingRules(existing, desired []netlink.Rule, family int) error {
	for i := range desired {
		if desired[i].Family != family {
			continue
		}
		if !containsRule(existing, &desired[i]) {
			if err := netlink.RuleAdd(&desired[i]); err != nil {
				return fmt.Errorf("error adding rule %v: %w", desired[i], err)
			}
			logger.Info("policy-routes: added rule", "rule", desired[i].String())
		}
	}
	return nil
}

func containsRule(rules []netlink.Rule, target *netlink.Rule) bool {
	for i := range rules {
		if rulesEqual(&rules[i], target) {
			return true
		}
	}
	return false
}

func buildDesiredRules(routes []cra.PolicyRoute) []netlink.Rule {
	var rules []netlink.Rule
	for _, pr := range routes {
		if pr.Vrf == "" {
			continue
		}

		tableID, err := vrfTableID(pr.Vrf)
		if err != nil {
			logger.Error(sanitizeErr(err), "policy-routes: cannot resolve VRF table", "vrf", logSanitizer.Replace(pr.Vrf))
			continue
		}

		base := netlink.NewRule()
		base.Priority = policyRoutePriority
		base.Table = tableID

		if pr.SrcPrefix != nil {
			base.Src = parsePrefixOrHost(*pr.SrcPrefix)
		}
		if pr.DstPrefix != nil {
			base.Dst = parsePrefixOrHost(*pr.DstPrefix)
		}
		if pr.SrcPort != nil {
			base.Sport = &netlink.RulePortRange{Start: *pr.SrcPort, End: *pr.SrcPort}
		}
		if pr.DstPort != nil {
			base.Dport = &netlink.RulePortRange{Start: *pr.DstPort, End: *pr.DstPort}
		}
		if pr.Protocol != nil {
			base.IPProto = protoNumber(*pr.Protocol)
		}

		setRuleFamily(base)
		rules = append(rules, *base)
	}
	return rules
}

func setRuleFamily(rule *netlink.Rule) {
	isIPv6 := (rule.Src != nil && rule.Src.IP.To4() == nil) ||
		(rule.Dst != nil && rule.Dst.IP.To4() == nil)

	if isIPv6 {
		rule.Family = unix.AF_INET6
		rule.IifName = baseConfig.ClusterVRF.Name
	} else {
		rule.Family = unix.AF_INET
		rule.IifName = baseConfig.MgmtInterface()
	}
}

func parsePrefixOrHost(s string) *net.IPNet {
	_, cidr, err := net.ParseCIDR(s)
	if err == nil {
		return cidr
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	return hostRoute(ip)
}

func hostRoute(ip net.IP) *net.IPNet {
	if ip.To4() != nil {
		return &net.IPNet{IP: ip, Mask: net.CIDRMask(net.IPv4len*8, net.IPv4len*8)} //nolint:mnd
	}
	return &net.IPNet{IP: ip, Mask: net.CIDRMask(net.IPv6len*8, net.IPv6len*8)} //nolint:mnd
}

func vrfTableID(name string) (int, error) {
	link, err := netlink.LinkByName(name)
	if err != nil {
		return 0, fmt.Errorf("VRF %q not found: %w", name, err)
	}
	vrf, ok := link.(*netlink.Vrf)
	if !ok {
		return 0, fmt.Errorf("%q is not a VRF device", name)
	}
	return int(vrf.Table), nil
}

func rulesEqual(a, b *netlink.Rule) bool {
	if a.Family != b.Family || a.Priority != b.Priority || a.Table != b.Table {
		return false
	}
	if a.IifName != b.IifName {
		return false
	}
	if !ipNetsEqual(a.Src, b.Src) || !ipNetsEqual(a.Dst, b.Dst) {
		return false
	}
	if !portRangesEqual(a.Sport, b.Sport) || !portRangesEqual(a.Dport, b.Dport) {
		return false
	}
	if a.IPProto != b.IPProto {
		return false
	}
	return true
}

func ipNetsEqual(a, b *net.IPNet) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.IP.Equal(b.IP) && a.Mask.String() == b.Mask.String()
}

func portRangesEqual(a, b *netlink.RulePortRange) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil {
		return false
	}
	return a.Start == b.Start && a.End == b.End
}

func protoNumber(proto string) int {
	switch strings.ToLower(proto) {
	case "tcp":
		return unix.IPPROTO_TCP
	case "udp":
		return unix.IPPROTO_UDP
	case "icmp":
		return unix.IPPROTO_ICMP
	case "icmpv6":
		return unix.IPPROTO_ICMPV6
	case "sctp":
		return unix.IPPROTO_SCTP
	default:
		return 0
	}
}

func getVRFsToDelete(cfg *nl.NetlinkConfiguration) ([]nl.VRFInformation, error) {
	existing, err := nlManager.ListL3()
	if err != nil {
		return nil, fmt.Errorf("error listing L3 VRF information: %w", err)
	}

	var toDelete []nl.VRFInformation

	for i := range existing {
		needsDeletion := true
		localOnly := false
		for j := range cfg.VRFs {
			if cfg.VRFs[j].Name == existing[i].Name {
				if cfg.VRFs[j].LocalOnly || cfg.VRFs[j].VNI == existing[i].VNI {
					needsDeletion = false
					localOnly = cfg.VRFs[j].LocalOnly
				}
				break
			}
		}
		if needsDeletion || (existing[i].MarkForDelete && !localOnly) {
			toDelete = append(toDelete, existing[i])
		}
	}

	return toDelete, nil
}

// createVRFs creates VRF devices, L3VNI bridges, and VXLAN interfaces but
// leaves bridges/VXLANs DOWN.  Callers must invoke upNewVRFs after FRR reload
// so that the SVI-up event makes FRR read the bridge hardware address as the
// EVPN Router-MAC (see zebra_vxlan_svi_up → process_l3vni_oper_up in FRR).
func createVRFs(cfg *nl.NetlinkConfiguration) ([]nl.VRFInformation, error) {
	existing, err := nlManager.ListL3()
	if err != nil {
		return nil, fmt.Errorf("error listing L3 VRF information: %w", err)
	}

	var created []nl.VRFInformation
	for i := range cfg.VRFs {
		alreadyExists := false
		for j := range existing {
			if existing[j].Name == cfg.VRFs[i].Name &&
				(cfg.VRFs[i].LocalOnly || (existing[j].VNI == cfg.VRFs[i].VNI && !existing[j].MarkForDelete)) {
				alreadyExists = true
				break
			}
		}
		if !alreadyExists {
			logger.Info("creating VRF", "vrf", logSanitizer.Replace(cfg.VRFs[i].Name))
			if err := nlManager.CreateL3(cfg.VRFs[i]); err != nil {
				return created, fmt.Errorf("error creating L3 (VRF: %s): %w", cfg.VRFs[i].Name, err)
			}
			created = append(created, cfg.VRFs[i])
		}
	}

	return created, nil
}

// upNewVRFs brings up bridges/VXLANs that were created by createVRFs.
// Must be called AFTER FRR reload so that FRR already knows the L3VNI→VRF
// mapping.  The SVI-up netlink event causes zebra to read the bridge MAC and
// advertise the correct EVPN Router-MAC.
func upNewVRFs(vrfs []nl.VRFInformation) error {
	for i := range vrfs {
		logger.Info("bringing up VRF interfaces", "vrf", logSanitizer.Replace(vrfs[i].Name))
		if err := nlManager.UpL3(vrfs[i]); err != nil {
			return fmt.Errorf("error setting up L3 (VRF: %s): %w", vrfs[i].Name, err)
		}
	}
	return nil
}

// reconcileLayer3 deletes stale VRFs and creates new ones (bridges/VXLANs
// DOWN).  Returns the list of newly created VRFs so the caller can bring them
// UP after FRR reload.
func reconcileLayer3(cfg *nl.NetlinkConfiguration) ([]nl.VRFInformation, error) {
	vrfsToDelete, err := getVRFsToDelete(cfg)
	if err != nil {
		return nil, fmt.Errorf("error getting VRFs to delete: %w", err)
	}

	for i := range vrfsToDelete {
		cleanupErrs := nlManager.CleanupL3(vrfsToDelete[i])
		if len(cleanupErrs) > 0 {
			return nil, fmt.Errorf("error cleaning up L3 (VRF: %s): %v", vrfsToDelete[i].Name, cleanupErrs)
		}
	}

	// Create VRFs and L3 VNI bridges but keep DOWN.
	// FRR reads the bridge hardware address as the EVPN Router-MAC only when
	// the SVI comes UP (zebra_vxlan_svi_up).  Bringing bridges UP before FRR
	// knows the L3VNI→VRF mapping would cause FRR to treat them as L2VNI
	// (broadcast storm with hairpin).  Bringing them UP after reload ensures
	// FRR has the VNI config and the SVI-up event triggers correct RMAC
	// advertisement.
	created, err := createVRFs(cfg)
	if err != nil {
		return created, fmt.Errorf("error creating VRFs: %w", err)
	}

	return created, nil
}

func reloadFRR() error {
	err := frrManager.ReloadFRR()
	if err != nil {
		logger.Error(err, "failed to reload FRR, trying to restart")

		err = frrManager.RestartFRR()
		if err != nil {
			logger.Error(err, "failed to restart FRR")
			return fmt.Errorf("error reloading / restarting FRR systemd unit: %w", err)
		}
	}
	logger.Info("reloaded FRR config")
	return nil
}

const (
	vniPollInterval = 500 * time.Millisecond
	vniPollTimeout  = 10 * time.Second
)

// waitForL3VNIs polls FRR's "show vrf vni" until all newly created L3VNIs
// appear in the output.  This ensures zebra has processed the reload config
// and registered the VNI→VRF mappings before we bring bridges UP.
func waitForL3VNIs(vrfs []nl.VRFInformation) {
	// Collect non-local VNIs we need to see.
	needed := map[int]bool{}
	for _, v := range vrfs {
		if !v.LocalOnly && v.VNI != 0 {
			needed[v.VNI] = true
		}
	}
	if len(needed) == 0 {
		return
	}

	deadline := time.Now().Add(vniPollTimeout)
	for time.Now().Before(deadline) {
		vrfVni, err := frrManager.Cli.ShowVRFVnis()
		if err == nil {
			found := 0
			for _, spec := range vrfVni.Vrfs {
				if needed[spec.Vni] {
					found++
				}
			}
			if found >= len(needed) {
				logger.Info("all L3VNIs registered in FRR", "count", found)
				return
			}
		}
		time.Sleep(vniPollInterval)
	}
	logger.Info("timed out waiting for L3VNIs in FRR", "needed", len(needed))
}

func applyConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Serialize config applications — concurrent requests race on FRR/netlink state.
	applyMu.Lock()
	defer applyMu.Unlock()

	// Parse Body into NetlinkConfiguration
	var craConfiguration cra.Configuration
	if err := parseApplyRequest(r, &craConfiguration); err != nil {
		logger.Error(sanitizeErr(err), "failed to parse request")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := writeFRRConfig(craConfiguration.FRRConfiguration); err != nil {
		logger.Error(sanitizeErr(err), "failed to write FRR config")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	if err := reconcileNetlink(&craConfiguration.NetlinkConfiguration); err != nil {
		logger.Error(sanitizeErr(err), "failed to reconcile netlink")
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	// Reconcile SBR policy routes as ip rules
	if err := reconcilePolicyRoutes(craConfiguration.PolicyRoutes); err != nil {
		logger.Error(sanitizeErr(err), "failed to reconcile policy routes")
		http.Error(w, fmt.Sprintf("Failed to reconcile policy routes: %v", err), http.StatusInternalServerError)
		return
	}

	// Reconcile traffic mirroring (loopbacks, GRE tunnels, tc filters).
	// Mirroring is an additive, non-disruptive observability feature, so a mirror
	// programming error must not fail the whole node configuration.
	if err := nlManager.ReconcileMirror(&craConfiguration.NetlinkConfiguration); err != nil {
		logger.Error(sanitizeErr(err), "failed to reconcile mirror configuration, continuing")
	}

	w.WriteHeader(http.StatusOK)
}

// parseApplyRequest reads and unmarshals the HTTP request body into cfg.
func parseApplyRequest(r *http.Request, cfg *cra.Configuration) error {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}
	defer r.Body.Close()

	if err = json.Unmarshal(body, cfg); err != nil {
		return fmt.Errorf("failed to unmarshal request body: %w", err)
	}
	return nil
}

// writeFRRConfig writes the FRR daemon configuration to disk.
func writeFRRConfig(frrConfig string) error {
	file, err := os.OpenFile(frrConfigPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o600) //nolint:mnd
	if err != nil {
		return fmt.Errorf("failed to open FRR config file: %w", err)
	}
	if _, err = io.Copy(file, strings.NewReader(frrConfig)); err != nil {
		_ = file.Close()
		return fmt.Errorf("failed to write FRR config: %w", err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("failed to close FRR config file: %w", err)
	}
	return nil
}

// reconcileNetlink drives the ordered netlink/FRR reconcile sequence.
func reconcileNetlink(cfg *nl.NetlinkConfiguration) error {
	if err := deleteLayer2(cfg); err != nil {
		return fmt.Errorf("failed to reconcile Layer2 (delete): %w", err)
	}

	// Phase 1: Create VRF devices and L3VNI bridges/VXLANs (DOWN).
	// Bridges must exist (with correct MAC) before FRR reload so zebra can
	// map VNI→SVI, but must stay DOWN to avoid L2VNI broadcast storms.
	newVRFs, err := reconcileLayer3(cfg)
	if err != nil {
		return fmt.Errorf("failed to reconcile Layer3: %w", err)
	}

	// Phase 2: Reload FRR so it learns VNI→VRF mapping from the config file.
	// At this point bridges exist (zebra knows the interfaces) but are DOWN,
	// so is_l3vni_oper_up() is false and no RMAC is advertised yet.
	if err := reloadFRR(); err != nil {
		return fmt.Errorf("failed to reload FRR: %w", err)
	}

	// Give zebra time to process the reload and learn VNI→VRF mappings.
	// Without this, the SVI-up event from Phase 3 may arrive before zebra
	// has associated the L3VNI, causing it to treat the VNI as L2.
	if len(newVRFs) > 0 {
		waitForL3VNIs(newVRFs)
	}

	// Phase 3: Bring new bridges/VXLANs UP.  The SVI-up netlink event makes
	// zebra call zebra_vxlan_svi_up → process_l3vni_oper_up which reads the
	// bridge hardware address and advertises the correct EVPN Router-MAC.
	if len(newVRFs) > 0 {
		if err := upNewVRFs(newVRFs); err != nil {
			return fmt.Errorf("failed to bring up new VRFs: %w", err)
		}
	}

	time.Sleep(defaultSleep)

	if err := createLayer2(cfg); err != nil {
		return fmt.Errorf("failed to reconcile Layer2 (create): %w", err)
	}

	reconcileNeighborSync(cfg)
	return nil
}

func executeFrr(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	if r.Body == nil {
		logger.Info("command request body is empty")
		http.Error(w, "Request body is empty", http.StatusBadRequest)
		return
	}

	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		logger.Error(err, "failed to read command request body")
		http.Error(w, "Failed to read request body", http.StatusInternalServerError)
		return
	}
	bodyContent := string(bodyBytes)

	data := frrManager.Cli.Execute(strings.Split(bodyContent, " "))
	w.WriteHeader(http.StatusOK)

	if _, err := w.Write(data); err != nil {
		logger.Error(err, "failed to write command response")
		return
	}
}

func setupTLS(address net.IP) error {
	certPrivKey, err := rsa.GenerateKey(rand.Reader, 4096) //nolint:mnd
	if err != nil {
		return fmt.Errorf("failed to generate private key: %w", err)
	}

	certTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject: pkix.Name{
			Organization: []string{"FRR-CRA"},
		},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().AddDate(10, 0, 0), //nolint:mnd
		KeyUsage:              x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		IPAddresses:           []net.IP{address},
		BasicConstraintsValid: true,
	}

	certBytes, err := x509.CreateCertificate(rand.Reader, certTemplate, certTemplate, &certPrivKey.PublicKey, certPrivKey)
	if err != nil {
		return fmt.Errorf("failed to create certificate: %w", err)
	}

	certOut, err := os.Create(serverCert)
	if err != nil {
		return fmt.Errorf("failed to open certificate file: %w", err)
	}
	if err := pem.Encode(certOut, &pem.Block{
		Type:  "CERTIFICATE",
		Bytes: certBytes,
	}); err != nil {
		certOut.Close()
		return fmt.Errorf("failed to encode certificate: %w", err)
	}
	if err := certOut.Close(); err != nil {
		return fmt.Errorf("failed to close certificate file: %w", err)
	}

	certPrivKeyPEM, err := os.Create(serverKey)
	if err != nil {
		return fmt.Errorf("failed to open private key file: %w", err)
	}
	if err := pem.Encode(certPrivKeyPEM, &pem.Block{
		Type:  "RSA PRIVATE KEY",
		Bytes: x509.MarshalPKCS1PrivateKey(certPrivKey),
	}); err != nil {
		certPrivKeyPEM.Close()
		return fmt.Errorf("failed to encode private key: %w", err)
	}
	if err := certPrivKeyPEM.Close(); err != nil {
		return fmt.Errorf("failed to close private key file: %w", err)
	}
	return nil
}

func setupPrometheusRegistry() (*prometheus.Registry, error) {
	// Create a new registry.
	reg := prometheus.NewRegistry()

	// Add Go module build info.
	reg.MustRegister(collectors.NewBuildInfoCollector())
	reg.MustRegister(collectors.NewGoCollector())
	collector, err := monitoring.NewDasSchiffNetworkOperatorCollector(
		map[string]bool{
			"frr":     true,
			"netlink": true,
		})
	if err != nil {
		return nil, fmt.Errorf("failed to create collector %w", err)
	}
	reg.MustRegister(collector)

	return reg, nil
}

func createListener(ip net.IP, port int, bindInterface string) (net.Listener, error) {
	var domain int
	var socketAddress syscall.Sockaddr
	if ip.To4() != nil {
		domain = syscall.AF_INET
		socketAddress = &syscall.SockaddrInet4{
			Port: port,
		}
		copy(socketAddress.(*syscall.SockaddrInet4).Addr[:], ip.To4())
	} else {
		domain = syscall.AF_INET6
		socketAddress = &syscall.SockaddrInet6{
			Port: port,
		}
		copy(socketAddress.(*syscall.SockaddrInet6).Addr[:], ip.To16())
	}

	fd, err := syscall.Socket(domain, syscall.SOCK_STREAM, syscall.IPPROTO_TCP)
	if err != nil {
		return nil, fmt.Errorf("failed to create socket: %w", err)
	}

	if bindInterface != "" {
		err = syscall.BindToDevice(fd, bindInterface)
		if err != nil {
			return nil, fmt.Errorf("failed to bind to device %s: %w", bindInterface, err)
		}
	}

	err = syscall.SetsockoptInt(fd, syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
	if err != nil {
		return nil, fmt.Errorf("failed to set socket options: %w", err)
	}

	if err := syscall.Bind(fd, socketAddress); err != nil {
		return nil, fmt.Errorf("failed to bind socket: %w", err)
	}

	if err := syscall.Listen(fd, syscall.SOMAXCONN); err != nil {
		return nil, fmt.Errorf("failed to listen on socket: %w", err)
	}

	file := os.NewFile(uintptr(fd), fmt.Sprintf("%s:%d", ip, port))
	listener, err := net.FileListener(file)
	if err != nil {
		return nil, fmt.Errorf("failed to create listener: %w", err)
	}
	return listener, nil
}

// parseFlagsAndSetupLogging parses the command line, including the --zap-*
// log flags, and sets up the logger.
func parseFlagsAndSetupLogging() {
	opts := zap.Options{Development: true}
	opts.BindFlags(flag.CommandLine)
	flag.Parse()
	ctrl.SetLogger(zap.New(zap.UseFlagOptions(&opts)))
	logging.RedirectLibraryLoggers(ctrl.Log)
}

func main() {
	ip := flag.String("ip", "fd00:7:caa5::", "IP to listen on and generate certificate for")
	bindInterface := flag.String("bind-interface", "cluster", "Bind interface to use for netlink")
	port := flag.Int("port", 8443, "Port to listen on") //nolint:mnd
	parseFlagsAndSetupLogging()

	parsedIP := net.ParseIP(*ip)
	if parsedIP == nil {
		logger.Error(fmt.Errorf("invalid IP %q", *ip), "invalid --ip")
		os.Exit(1)
	}

	var err error
	baseConfig, err = config.LoadBaseConfig(baseConfigPath)
	if err != nil {
		logger.Error(err, "failed to load base config")
		os.Exit(1)
	}

	frrManager = frr.NewFRRManager()
	nlManager = nl.NewManager(&nl.Toolkit{}, baseConfig)

	// Initialize BPF and neighbor synchronization
	if err := bpf.InitBPF(); err != nil {
		logger.Error(err, "failed to initialize BPF, neighbor sync disabled")
	} else {
		neighborSyncer = neighborsync.NewNeighborSync()
		neighborSyncer.StartNeighborSync()
		logger.Info("BPF and neighbor sync initialized")
	}

	registry, err := setupPrometheusRegistry()
	if err != nil {
		logger.Error(err, "failed to setup Prometheus registry")
		os.Exit(1)
	}

	http.HandleFunc("/frr/configuration", applyConfig)
	http.HandleFunc("/frr/command", executeFrr)
	http.Handle("/frr/metrics", promhttp.HandlerFor(
		registry,
		promhttp.HandlerOpts{
			// Opt into OpenMetrics to support exemplars.
			EnableOpenMetrics: true,
			Timeout:           time.Minute,
		},
	))

	exporterURL, err := url.Parse("http://localhost:9100")
	if err != nil {
		logger.Error(err, "failed to parse URL")
		os.Exit(1)
	}
	// Build proxy for local node-exporter
	proxy := httputil.NewSingleHostReverseProxy(exporterURL)
	http.Handle("/node-exporter/metrics", utils.ExactPathHandler("/node-exporter/metrics", http.StripPrefix("/node-exporter", proxy)))

	// Check if the server certificate and key exist
	if _, err := os.Stat(serverCert); os.IsNotExist(err) {
		err = setupTLS(parsedIP)
		if err != nil {
			logger.Error(err, "failed to setup TLS")
			os.Exit(1)
		}
	}
	if _, err := os.Stat(serverKey); os.IsNotExist(err) {
		err = setupTLS(parsedIP)
		if err != nil {
			logger.Error(err, "failed to setup TLS")
			os.Exit(1)
		}
	}

	caCert, err := os.ReadFile(serverCert)
	if err != nil {
		logger.Error(err, "failed to read CA certificate")
		os.Exit(1)
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)

	//nolint:gosec
	tlsConfig := &tls.Config{
		ClientCAs:  caCertPool,
		ClientAuth: tls.RequireAndVerifyClientCert,
	}

	//nolint:gosec
	server := &http.Server{
		TLSConfig: tlsConfig,
	}

	listener, err := createListener(parsedIP, *port, *bindInterface)
	if err != nil {
		logger.Error(err, "failed to create listener")
		os.Exit(1)
	}

	err = server.ServeTLS(listener, serverCert, serverKey)
	if err != nil {
		logger.Error(err, "failed to start server")
		os.Exit(1)
	}
}
