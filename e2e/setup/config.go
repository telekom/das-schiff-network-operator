package setup

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"text/template"
)

// GenerateNodeConfigs generates per-node CRA configs into e2e/generated/<node>/.
// These are bind-mounted into containers by containerlab.
func GenerateNodeConfigs(repoRoot string, cluster *Cluster) error {
	genDir := filepath.Join(repoRoot, "e2e", "generated")
	tplDir := filepath.Join(repoRoot, "e2e", "cra-config")

	// Clean and recreate
	if err := os.RemoveAll(genDir); err != nil {
		return fmt.Errorf("removing generated dir: %w", err)
	}

	for _, node := range cluster.Nodes {
		// Strip the clab prefix for directory names
		shortName := strings.TrimPrefix(node.Name, "clab-nwop-")
		nodeDir := filepath.Join(genDir, shortName)

		for _, sub := range []string{
			"cra/netplan",
			"cra/certs",
			"cra/systemd-network/10-netplan-hbn.network.d",
		} {
			if err := os.MkdirAll(filepath.Join(nodeDir, sub), 0o755); err != nil {
				return err
			}
		}

		Logf("Generating configs for %s (VTEP=%s)", shortName, node.VtepIP)

		// Static files
		if err := os.WriteFile(filepath.Join(nodeDir, "cra/interfaces"), []byte("eth1\neth2\n"), 0o644); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(nodeDir, "cra/flavour"), []byte("frr\n"), 0o644); err != nil {
			return err
		}

		data := templateData{
			VtepIP:        node.VtepIP,
			NodeIPv4:      node.IPv4,
			NodeIPv6:      node.IPv6,
			Hostname:      node.Hostname,
			BridgeMAC:     node.BridgeMAC,
			MgmtBridgeMAC: node.MgmtBridgeMAC,
		}

		// Render templates
		templates := []struct {
			src string
			dst string
		}{
			{"base-config.yaml.tpl", "cra/base-config.yaml"},
			{"frr.conf.tpl", "cra/frr.conf"},
			{"netplan/10-base.yaml.tpl", "cra/netplan/10-base.yaml"},
		}
		for _, t := range templates {
			if err := renderTemplate(filepath.Join(tplDir, t.src), filepath.Join(nodeDir, t.dst), data); err != nil {
				return fmt.Errorf("rendering %s for %s: %w", t.src, shortName, err)
			}
		}

		// CRA-side systemd-networkd drop-in: routes for node IPs via hbn
		hbnRoutes := fmt.Sprintf(`[Route]
Destination=%s/32
Gateway=fd00:7:caa5::1

[Route]
Destination=%s/128
Gateway=fd00:7:caa5::1
`, node.IPv4, node.IPv6)
		if err := os.WriteFile(
			filepath.Join(nodeDir, "cra/systemd-network/10-netplan-hbn.network.d/systemd-hbn-routes.conf"),
			[]byte(hbnRoutes), 0o644,
		); err != nil {
			return err
		}

		// Node identity env file
		identity := fmt.Sprintf("NODE_IPV4=%s\nNODE_IPV6=%s\nVTEP_IP=%s\nNODE_HOSTNAME=%s\n",
			node.IPv4, node.IPv6, node.VtepIP, node.Hostname)
		if err := os.WriteFile(filepath.Join(nodeDir, "node-identity.env"), []byte(identity), 0o644); err != nil {
			return err
		}
	}

	Logf("All configs generated in %s", genDir)
	return nil
}

type templateData struct {
	VtepIP        string
	NodeIPv4      string
	NodeIPv6      string
	Hostname      string
	BridgeMAC     string
	MgmtBridgeMAC string
}

// renderTemplate renders a Go template file with {{ .Var }} placeholders.
func renderTemplate(srcPath, dstPath string, data templateData) error {
	content, err := os.ReadFile(srcPath)
	if err != nil {
		return err
	}

	tmpl, err := template.New(filepath.Base(srcPath)).Parse(string(content))
	if err != nil {
		return fmt.Errorf("parsing template %s: %w", srcPath, err)
	}

	f, err := os.Create(dstPath)
	if err != nil {
		return err
	}
	defer f.Close()

	return tmpl.Execute(f, data)
}
