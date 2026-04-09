/*
Copyright 2024.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package renderer

import (
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	networkv1alpha1 "github.com/telekom/das-schiff-network-operator/api/v1alpha1"
)

const (
	originAnnotation = "network-connector.sylvaproject.org/origins"

	// ANSI color codes.
	colorReset  = "\033[0m"
	colorGreen  = "\033[32m"
	colorYellow = "\033[33m"
	colorRed    = "\033[31m"
	colorCyan   = "\033[36m"
	colorDim    = "\033[2m"

	// Tree drawing characters.
	treeBranch = "├─"
	treeLast   = "└─"
	treePipe   = "│ "
	treeSpace  = "  "

	tabwriterMinWidth = 0
	tabwriterTabWidth = 2
	tabwriterPadding  = 2

	revisionShortLen = 12
	revisionLongLen  = 16

	hoursPerDay = 24
)

// Origins maps NNC section keys (e.g. "layer2s/prod-vlan100") to source CRDs.
type Origins map[string]string

// ParseOrigins extracts the origins annotation from NNC annotations.
func ParseOrigins(annotations map[string]string) Origins {
	if annotations == nil {
		return nil
	}
	raw, ok := annotations[originAnnotation]
	if !ok {
		return nil
	}
	var origins Origins
	if err := json.Unmarshal([]byte(raw), &origins); err != nil {
		return nil
	}
	return origins
}

// Renderer handles tree + table output for NNC visualization.
type Renderer struct {
	w     io.Writer
	color bool
}

// New creates a new Renderer.
func New(w io.Writer, color bool) *Renderer {
	return &Renderer{w: w, color: color}
}

// RenderNNC renders a full NodeNetworkConfig visualization.
func (r *Renderer) RenderNNC(nnc *networkv1alpha1.NodeNetworkConfig, origins Origins) {
	r.renderHeader(nnc)
	r.renderLayer2s(nnc.Spec.Layer2s, origins)
	r.renderFabricVRFs(nnc.Spec.FabricVRFs, origins)
	r.renderLocalVRFs(nnc.Spec.LocalVRFs, origins)
	r.renderClusterVRF(nnc.Spec.ClusterVRF, origins)
}

// RenderList renders a summary table of all NNCs.
func (r *Renderer) RenderList(list *networkv1alpha1.NodeNetworkConfigList) {
	if len(list.Items) == 0 {
		fmt.Fprintln(r.w, "No NodeNetworkConfigs found.")
		return
	}

	tw := tabwriter.NewWriter(r.w, tabwriterMinWidth, tabwriterTabWidth, tabwriterPadding, ' ', 0)
	fmt.Fprintln(tw, "NODE\tREVISION\tSTATUS\tLAST UPDATE\t#L2\t#FABRIC\t#LOCAL")

	for i := range list.Items {
		nnc := &list.Items[i]
		rev := truncate(nnc.Spec.Revision, revisionShortLen)
		status := nnc.Status.ConfigStatus
		lastUpdate := formatMetaTime(nnc.Status.LastUpdate)

		nL2 := len(nnc.Spec.Layer2s)
		nFabric := len(nnc.Spec.FabricVRFs)
		nLocal := len(nnc.Spec.LocalVRFs)

		statusStr := r.colorStatus(status)
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%d\t%d\n",
			nnc.Name, rev, statusStr, lastUpdate, nL2, nFabric, nLocal)
	}

	tw.Flush()
}

func (r *Renderer) renderHeader(nnc *networkv1alpha1.NodeNetworkConfig) {
	fmt.Fprintf(r.w, "%s: %s\n", r.bold("NodeNetworkConfig"), nnc.Name)
	fmt.Fprintf(r.w, "  Revision: %s\n", truncate(nnc.Spec.Revision, revisionLongLen))
	fmt.Fprintf(r.w, "  Status:   %s", r.colorStatus(nnc.Status.ConfigStatus))
	if !nnc.Status.LastUpdate.IsZero() {
		fmt.Fprintf(r.w, " (last update: %s)", formatMetaTime(nnc.Status.LastUpdate))
	}
	fmt.Fprintln(r.w)
	fmt.Fprintln(r.w)
}

func (r *Renderer) renderLayer2s(layer2s map[string]networkv1alpha1.Layer2, origins Origins) {
	if len(layer2s) == 0 {
		return
	}

	keys := sortedKeys(layer2s)
	fmt.Fprintf(r.w, "%s (%d):\n", r.bold("Layer2s"), len(layer2s))

	for idx, key := range keys {
		l2 := layer2s[key]
		isLast := idx == len(keys)-1
		branch := treeBranch
		if isLast {
			branch = treeLast
		}

		origin := r.originSuffix(origins, "layer2s/"+key)
		fmt.Fprintf(r.w, "  %s %s (VNI=%d, VLAN=%d, MTU=%d)%s\n",
			branch, r.cyan(key), l2.VNI, l2.VLAN, l2.MTU, origin)

		prefix := treeSpace
		if !isLast {
			prefix = treePipe
		}

		if l2.IRB != nil {
			irb := l2.IRB
			fmt.Fprintf(r.w, "  %s  IRB: VRF=%s, MAC=%s, IPs=%v\n",
				prefix, irb.VRF, irb.MACAddress, irb.IPAddresses)
		}

		if len(l2.MirrorACLs) > 0 {
			fmt.Fprintf(r.w, "  %s  MirrorACLs: %d\n", prefix, len(l2.MirrorACLs))
		}
	}
	fmt.Fprintln(r.w)
}

func (r *Renderer) renderFabricVRFs(vrfs map[string]networkv1alpha1.FabricVRF, origins Origins) {
	if len(vrfs) == 0 {
		return
	}

	keys := sortedKeys(vrfs)
	fmt.Fprintf(r.w, "%s (%d):\n", r.bold("FabricVRFs"), len(vrfs))

	for idx, key := range keys {
		fvrf := vrfs[key]
		isLast := idx == len(keys)-1
		branch := treeBranch
		if isLast {
			branch = treeLast
		}

		origin := r.originSuffix(origins, "fabricVRFs/"+key)
		rts := strings.Join(fvrf.EVPNImportRouteTargets, ",")
		fmt.Fprintf(r.w, "  %s %s (VNI=%d, RT=%s)%s\n",
			branch, r.cyan(key), fvrf.VNI, rts, origin)

		prefix := treeSpace
		if !isLast {
			prefix = treePipe
		}

		r.renderVRFDetails(prefix, &fvrf.VRF, origins, "fabricVRFs/"+key)
	}
	fmt.Fprintln(r.w)
}

func (r *Renderer) renderLocalVRFs(vrfs map[string]networkv1alpha1.VRF, origins Origins) {
	if len(vrfs) == 0 {
		return
	}

	keys := sortedKeys(vrfs)
	fmt.Fprintf(r.w, "%s (%d):\n", r.bold("LocalVRFs"), len(vrfs))

	for idx, key := range keys {
		vrf := vrfs[key]
		isLast := idx == len(keys)-1
		branch := treeBranch
		if isLast {
			branch = treeLast
		}

		origin := r.originSuffix(origins, "localVRFs/"+key)
		fmt.Fprintf(r.w, "  %s %s%s\n", branch, r.cyan(key), origin)

		prefix := treeSpace
		if !isLast {
			prefix = treePipe
		}

		r.renderVRFDetails(prefix, &vrf, origins, "localVRFs/"+key)
	}
	fmt.Fprintln(r.w)
}

func (r *Renderer) renderClusterVRF(vrf *networkv1alpha1.VRF, origins Origins) {
	if vrf == nil {
		return
	}

	fmt.Fprintf(r.w, "%s:\n", r.bold("ClusterVRF"))
	r.renderVRFDetails(treeSpace, vrf, origins, "clusterVRF")
	fmt.Fprintln(r.w)
}

func (r *Renderer) renderVRFDetails(prefix string, vrf *networkv1alpha1.VRF, origins Origins, originPrefix string) {
	indent := "  " + prefix + " "
	r.renderBGPPeers(indent, vrf.BGPPeers)
	r.renderStaticRoutes(indent, vrf.StaticRoutes, origins, originPrefix)
	r.renderPolicyRoutes(indent, vrf.PolicyRoutes, origins, originPrefix)
	r.renderVRFImports(indent, vrf.VRFImports)
	r.renderRedistribute(indent, vrf.Redistribute)
	if len(vrf.Loopbacks) > 0 {
		fmt.Fprintf(r.w, "%sLoopbacks: %d\n", indent, len(vrf.Loopbacks))
	}
}

func (r *Renderer) renderBGPPeers(indent string, peers []networkv1alpha1.BGPPeer) {
	if len(peers) == 0 {
		return
	}
	fmt.Fprintf(r.w, "%sBGPPeers:\n", indent)
	tw := tabwriter.NewWriter(r.w, tabwriterMinWidth, tabwriterTabWidth, tabwriterPadding, ' ', 0)
	fmt.Fprintf(tw, "%s  NEIGHBOR\tASN\tFAMILIES\n", indent)
	for _, peer := range peers {
		addr := "<dynamic>"
		if peer.Address != nil {
			addr = *peer.Address
		} else if peer.ListenRange != nil {
			addr = *peer.ListenRange + " (range)"
		}
		var families []string
		if peer.IPv4 != nil {
			families = append(families, "ipv4")
		}
		if peer.IPv6 != nil {
			families = append(families, "ipv6")
		}
		fmt.Fprintf(tw, "%s  %s\t%d\t%s\n", indent, addr, peer.RemoteASN, strings.Join(families, ","))
	}
	tw.Flush()
}

func (r *Renderer) renderStaticRoutes(indent string, routes []networkv1alpha1.StaticRoute, origins Origins, originPrefix string) {
	if len(routes) == 0 {
		return
	}
	fmt.Fprintf(r.w, "%sStaticRoutes:\n", indent)
	tw := tabwriter.NewWriter(r.w, tabwriterMinWidth, tabwriterTabWidth, tabwriterPadding, ' ', 0)
	fmt.Fprintf(tw, "%s  PREFIX\tNEXTHOP\n", indent)
	for _, sr := range routes {
		nh := "-"
		if sr.NextHop != nil {
			if sr.NextHop.Vrf != nil {
				nh = "vrf:" + *sr.NextHop.Vrf
			} else if sr.NextHop.Address != nil {
				nh = *sr.NextHop.Address
			}
		}
		origin := r.originSuffix(origins, originPrefix+"/staticRoutes/"+sr.Prefix)
		fmt.Fprintf(tw, "%s  %s\t%s%s\n", indent, sr.Prefix, nh, origin)
	}
	tw.Flush()
}

func (r *Renderer) renderPolicyRoutes(indent string, routes []networkv1alpha1.PolicyRoute, origins Origins, originPrefix string) {
	if len(routes) == 0 {
		return
	}
	fmt.Fprintf(r.w, "%sPolicyRoutes:\n", indent)
	tw := tabwriter.NewWriter(r.w, tabwriterMinWidth, tabwriterTabWidth, tabwriterPadding, ' ', 0)
	fmt.Fprintf(tw, "%s  SRC\tDST\tNEXTHOP-VRF\n", indent)
	for _, pr := range routes {
		src := ptrOrDash(pr.TrafficMatch.SrcPrefix)
		dst := ptrOrDash(pr.TrafficMatch.DstPrefix)
		vrfStr := ptrOrDash(pr.NextHop.Vrf)
		origin := ""
		if pr.NextHop.Vrf != nil {
			origin = r.originSuffix(origins, originPrefix+"/policyRoutes/"+*pr.NextHop.Vrf)
		}
		fmt.Fprintf(tw, "%s  %s\t%s\t%s%s\n", indent, src, dst, vrfStr, origin)
	}
	tw.Flush()
}

func (r *Renderer) renderVRFImports(indent string, imports []networkv1alpha1.VRFImport) {
	if len(imports) == 0 {
		return
	}
	fmt.Fprintf(r.w, "%sVRFImports:\n", indent)
	for _, imp := range imports {
		fmt.Fprintf(r.w, "%s  from %s (default: %s)\n", indent,
			r.cyan(imp.FromVRF), string(imp.Filter.DefaultAction.Type))
		if len(imp.Filter.Items) == 0 {
			continue
		}
		tw := tabwriter.NewWriter(r.w, tabwriterMinWidth, tabwriterTabWidth, tabwriterPadding, ' ', 0)
		fmt.Fprintf(tw, "%s    ACTION\tPREFIX\n", indent)
		for _, item := range imp.Filter.Items {
			action := string(item.Action.Type)
			prefix := "-"
			if item.Matcher.Prefix != nil {
				prefix = item.Matcher.Prefix.Prefix
				if item.Matcher.Prefix.Le != nil {
					prefix += fmt.Sprintf(" le %d", *item.Matcher.Prefix.Le)
				}
			}
			fmt.Fprintf(tw, "%s    %s\t%s\n", indent, action, prefix)
		}
		tw.Flush()
	}
}

func (r *Renderer) renderRedistribute(indent string, redistribute *networkv1alpha1.Redistribute) {
	if redistribute == nil {
		return
	}
	var parts []string
	if redistribute.Connected != nil {
		parts = append(parts, "connected")
	}
	if redistribute.Static != nil {
		parts = append(parts, "static")
	}
	fmt.Fprintf(r.w, "%sRedistribute: %s\n", indent, strings.Join(parts, ", "))
}

// colorStatus returns the status string with ANSI color codes if color is enabled.
func (r *Renderer) colorStatus(status string) string {
	if !r.color {
		return status
	}
	switch status {
	case "provisioned":
		return colorGreen + status + colorReset
	case "provisioning":
		return colorYellow + status + colorReset
	case "failed", "invalid":
		return colorRed + status + colorReset
	default:
		return status
	}
}

func (r *Renderer) bold(s string) string {
	if !r.color {
		return s
	}
	return "\033[1m" + s + colorReset
}

func (r *Renderer) cyan(s string) string {
	if !r.color {
		return s
	}
	return colorCyan + s + colorReset
}

func (r *Renderer) originSuffix(origins Origins, key string) string {
	if origins == nil {
		return ""
	}
	src, ok := origins[key]
	if !ok {
		return ""
	}
	if r.color {
		return "  " + colorDim + "← " + src + colorReset
	}
	return "  ← " + src
}

func ptrOrDash(p *string) string {
	if p == nil {
		return "-"
	}
	return *p
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func formatMetaTime(t metav1.Time) string {
	if t.IsZero() {
		return "-"
	}
	d := time.Since(t.Time)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds ago", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < hoursPerDay*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd ago", int(d.Hours()/hoursPerDay))
	}
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
