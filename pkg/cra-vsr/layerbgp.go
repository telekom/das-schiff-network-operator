/*
Copyright 2025.

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

package cra

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strconv"

	"github.com/telekom/das-schiff-network-operator/api/v1alpha1"
	"github.com/telekom/das-schiff-network-operator/pkg/config"
	"github.com/telekom/das-schiff-network-operator/pkg/helpers/types"
)

type LayerBGP struct {
	nodeCfg *v1alpha1.NodeNetworkConfigSpec
	vrouter *VRouter
	ns      *Namespace
	mgr     *Manager
}

func NewLayerBGP(
	nodeCfg *v1alpha1.NodeNetworkConfigSpec,
	vrouter *VRouter,
	ns *Namespace,
	mgr *Manager,
) *LayerBGP {
	return &LayerBGP{
		nodeCfg: nodeCfg,
		vrouter: vrouter,
		mgr:     mgr,
		ns:      ns,
	}
}

func (LayerBGP) hash(s string) string {
	hash := sha256.New()
	hash.Write([]byte(s)) // discard error
	hashBytes := hash.Sum(nil)
	hashHex := hex.EncodeToString(hashBytes)

	return hashHex[:8]
}

func (l *LayerBGP) getBGPPeerName(peer v1alpha1.BGPPeer) string {
	if peer.Address != nil {
		return l.hash(*peer.Address)
	}

	return l.hash(*peer.ListenRange)
}

func (LayerBGP) convActionToPolicy(action v1alpha1.Action) Policy {
	if action.Type == v1alpha1.Accept {
		return Permit
	}

	return Deny
}

func (LayerBGP) convStaticRoute(from v1alpha1.StaticRoute) StaticRoute {
	to := StaticRoute{
		Destination: from.Prefix,
	}
	nh := NextHop{
		NextHop: "blackhole",
	}

	if from.NextHop != nil {
		if from.NextHop.Address != nil {
			nh.NextHop = *from.NextHop.Address
		}
		if from.NextHop.Vrf != nil {
			nh.NextHop = *from.NextHop.Vrf
			nh.VRF = from.NextHop.Vrf
		}
	}
	to.NextHops = append(to.NextHops, nh)

	return to
}

func (LayerBGP) mkStaticRoute(routing *Routing, routes ...StaticRoute) {
	if routing.Static == nil {
		routing.Static = &StaticRouting{}
	}

	for _, rt := range routes {
		if isIPv4(rt.Destination) {
			routing.Static.IPv4 = append(routing.Static.IPv4, rt)
		} else {
			routing.Static.IPv6 = append(routing.Static.IPv6, rt)
		}
	}
}

func (LayerBGP) mkRule(routing *Routing, rules ...Rule) {
	if routing.PBR == nil {
		routing.PBR = &PolicyBasedRouting{}
	}

	for _, rule := range rules {
		var ip string

		if rule.Match.SourceIP != nil {
			ip = *rule.Match.SourceIP
		} else {
			ip = *rule.Match.DestinationIP
		}

		if isIPv4(ip) {
			routing.PBR.IPv4 = append(routing.PBR.IPv4, rule)
		} else {
			if rule.Match != nil {
				rule.Match.Interface = nil
			}
			routing.PBR.IPv6 = append(routing.PBR.IPv6, rule)
		}
	}
}

func (l *LayerBGP) mkPrefixList(ipv IPvX, name string, seqs ...PrefixListSeq) {
	list := &l.vrouter.Routing.PrefixListV6
	if ipv == IPv4 {
		list = &l.vrouter.Routing.PrefixListV4
	}

	for i, pl := range *list {
		if pl.Name == name {
			(*list)[i].Seqs = append((*list)[i].Seqs, seqs...)
			return
		}
	}

	*list = append(*list, PrefixList{
		Name: name,
		Seqs: seqs,
	})
}

func (l *LayerBGP) mkRouteMap(name string, seqs ...RtMapSeq) {
	routing := l.vrouter.Routing

	for i, rtmap := range routing.RouteMaps {
		if rtmap.Name == name {
			routing.RouteMaps[i].Seqs = append(routing.RouteMaps[i].Seqs, seqs...)
			return
		}
	}

	routing.RouteMaps = append(routing.RouteMaps, RouteMap{
		Name: name,
		Seqs: seqs,
	})
}

func (l *LayerBGP) mkCommunityList(name string, seqs ...BGPCommunityListSeq) {
	for _, comml := range l.vrouter.Routing.BGP.CommunityLists {
		if comml.Name == name {
			comml.Seqs = append(comml.Seqs, seqs...)
			return
		}
	}

	bgp := l.vrouter.Routing.BGP
	bgp.CommunityLists = append(bgp.CommunityLists, BGPCommunityList{
		Name: name,
		Seqs: seqs,
	})
}

func (l *LayerBGP) setupRouteMap(name string, i int, conf v1alpha1.FilterItem) {
	rtmap := RtMapSeq{
		Num:    i + 10, //nolint:mnd
		Policy: l.convActionToPolicy(conf.Action),
	}
	matcher := conf.Matcher

	if matcher.Prefix != nil {
		name := "pl_" + name + "_" + strconv.Itoa(i)
		prfx := matcher.Prefix
		ipv := getIPvX(prfx.Prefix)

		l.mkPrefixList(getIPvX(prfx.Prefix), name, PrefixListSeq{
			Num:     DefaultPrefixListSeqNum,
			Policy:  Permit,
			Address: &prfx.Prefix,
			LE:      prfx.Le,
			GE:      prfx.Ge,
		})

		rtmap.Match = &RtMapMatch{}
		if ipv == IPv4 {
			rtmap.Match.IPv4 = &RtMapMatchIP{
				PrefixList: &name,
			}
		} else {
			rtmap.Match.IPv6 = &RtMapMatchIP{
				PrefixList: &name,
			}
		}
	}

	if matcher.BGPCommunity != nil {
		name := "cm_" + name + "_" + strconv.Itoa(i)
		comm := matcher.BGPCommunity

		l.mkCommunityList(name, BGPCommunityListSeq{
			Num:    DefaultCommunityListSeqNum,
			Policy: Permit,
			Attrs:  []string{comm.Community},
		})

		if rtmap.Match == nil {
			rtmap.Match = &RtMapMatch{}
		}
		rtmap.Match.Community = &RtMapMatchCommunity{
			ID: name,
		}
	}

	if conf.Action.ModifyRoute != nil {
		modify := conf.Action.ModifyRoute

		switch {
		case len(modify.AddCommunities) > 0:
			rtmap.Set = &RtMapSet{
				Community: &RtMapSetCommunity{},
			}

			if modify.AdditiveCommunities != nil {
				rtmap.Set.Community.Add = &RtMapSetCommAdd{
					Attrs: modify.AddCommunities,
				}
			} else {
				rtmap.Set.Community.Replace = &RtMapSetCommReplace{
					Attrs: modify.AddCommunities,
				}
			}
		case len(modify.RemoveCommunities) > 0:
			name := "cm_remove_" + name + "_" + strconv.Itoa(i)

			l.mkCommunityList(name, BGPCommunityListSeq{
				Num:    DefaultCommunityListSeqNum,
				Policy: Permit,
				Attrs:  modify.RemoveCommunities,
			})

			rtmap.Set = &RtMapSet{
				CommListDelete: &name,
			}
		case modify.RemoveAllCommunities != nil:
			rtmap.Set = &RtMapSet{
				Community: &RtMapSetCommunity{
					None: types.ToPtr(NoString),
				},
			}
		}

		if conf.Action.Type == v1alpha1.Next {
			rtmap.OnMatch = types.ToPtr("next")
		}
	}

	l.mkRouteMap("rm_"+name, rtmap)
}

func (l *LayerBGP) setupRouteMaps(name string, conf v1alpha1.Filter) {
	for i, item := range conf.Items {
		l.setupRouteMap(name, i, item)
	}

	l.mkRouteMap("rm_"+name, RtMapSeq{
		Num:    len(conf.Items) + 10, //nolint:mnd
		Policy: l.convActionToPolicy(conf.DefaultAction),
	})
}

func (l *LayerBGP) setupPolicyRoute(i int, conf v1alpha1.PolicyRoute) error {
	if conf.TrafficMatch.SrcPrefix == nil || *conf.TrafficMatch.SrcPrefix == "" {
		if conf.TrafficMatch.DstPrefix == nil || *conf.TrafficMatch.DstPrefix == "" {
			return fmt.Errorf("invalid policy-route without prefix matcher")
		}
	}

	if conf.NextHop.Vrf == nil || *conf.NextHop.Vrf == "" {
		return fmt.Errorf("invalid policy-route without nexthop vrf")
	}

	nhVrf := lookupVRF(l.ns, *conf.NextHop.Vrf)
	if nhVrf == nil {
		return fmt.Errorf("invalid policy-route nexthop vrf: %s", *conf.NextHop.Vrf)
	}

	l.mkRule(l.ns.Routing, Rule{
		Priority: i,
		Match: &RuleMatch{
			Interface:     &l.mgr.baseConfig.TrunkInterfaceName,
			SourceIP:      conf.TrafficMatch.SrcPrefix,
			DestinationIP: conf.TrafficMatch.DstPrefix,
		},
		Action: &RuleAction{
			Lookup: strconv.Itoa(nhVrf.TableID),
		},
	})

	return nil
}

func (l *LayerBGP) setupVRFImport(vrf *VRF, i int, conf v1alpha1.VRFImport) {
	l.mkRouteMap("rm_"+vrf.Name+"_import", RtMapSeq{
		Num:    i + 10, //nolint:mnd
		Policy: Permit,
		Match: &RtMapMatch{
			SourceVRF: &conf.FromVRF,
		},
		Call: types.ToPtr("rm_" + vrf.Name + "_import_" + conf.FromVRF),
	})

	l.setupRouteMaps(vrf.Name+"_import_"+conf.FromVRF, conf.Filter)

	if ucast := vrf.Routing.BGP.AF.UcastV4; ucast != nil {
		vrfImports := ucast.VRFImports.Imports
		vrfImports.VRFs = append(vrfImports.VRFs, conf.FromVRF)
	}
	if ucast := vrf.Routing.BGP.AF.UcastV6; ucast != nil {
		vrfImports := ucast.VRFImports.Imports
		vrfImports.VRFs = append(vrfImports.VRFs, conf.FromVRF)
	}
}

func (l *LayerBGP) setupNeighbor(bgp *BGP, conf v1alpha1.BGPPeer) {
	name := l.getBGPPeerName(conf)

	var neigh *BGPNeighbor

	switch {
	case conf.Address != nil:
		bgp.NeighborIPs = append(bgp.NeighborIPs, BGPNeighborIP{
			Address: *conf.Address,
		})
		neigh = &bgp.NeighborIPs[len(bgp.NeighborIPs)-1].BGPNeighbor
	case conf.ListenRange != nil:
		bgp.NeighGroups = append(bgp.NeighGroups, BGPNeighborGroup{
			Name: name,
		})
		neigh = &bgp.NeighGroups[len(bgp.NeighGroups)-1].BGPNeighbor
		if bgp.Listen == nil {
			bgp.Listen = &BGPListen{}
		}
		bgp.Listen.Ranges = append(bgp.Listen.Ranges, BGPNeighRange{
			Range: *conf.ListenRange,
			Group: name,
		})
	default:
		return
	}

	neigh.EnforceFirstAS = types.ToPtr(true)
	neigh.RemoteAS = types.ToPtr(strconv.Itoa(int(conf.RemoteASN)))

	if conf.KeepaliveTime != nil || conf.HoldTime != nil {
		neigh.Timers = &BGPNeighTimers{}
		if conf.KeepaliveTime != nil {
			neigh.Timers.KeepAliveInterval = types.ToPtr(int(conf.KeepaliveTime.Seconds()))
		}
		if conf.HoldTime != nil {
			neigh.Timers.HoldTime = types.ToPtr(int(conf.HoldTime.Seconds()))
		}
	}

	if conf.Multihop != nil {
		neigh.TTLSecHops = types.ToPtr(int(*conf.Multihop))
	}

	dict := map[IPvX]*v1alpha1.AddressFamily{
		IPv4: conf.IPv4,
		IPv6: conf.IPv6,
	}
	for ipv, conf := range dict {
		if conf == nil {
			continue
		}

		var ucast *BGPNeighUcast
		var ipname string

		if neigh.AF == nil {
			neigh.AF = &BGPNeighAF{}
		}

		if ipv == IPv4 {
			neigh.AF.UcastV4 = &BGPNeighUcast{}
			ucast = neigh.AF.UcastV4
			ipname = name + "-ipv4"
		} else {
			neigh.AF.UcastV6 = &BGPNeighUcast{}
			ucast = neigh.AF.UcastV6
			ipname = name + "-ipv6"
		}

		if conf.ImportFilter != nil {
			l.setupRouteMaps(ipname+"-in", *conf.ImportFilter)
			ucast.RouteMaps = append(ucast.RouteMaps, BGPNeighRouteMap{
				Name:      "rm_" + ipname + "-in",
				Direction: IN,
			})
		}

		if conf.ExportFilter != nil {
			l.setupRouteMaps(ipname+"-out", *conf.ExportFilter)
			ucast.RouteMaps = append(ucast.RouteMaps, BGPNeighRouteMap{
				Name:      "rm_" + ipname + "-out",
				Direction: OUT,
			})
		}

		if conf.MaxPrefixes != nil {
			ucast.MaxPrefix = &BGPNeighMaxPrefix{
				Maximum: int(*conf.MaxPrefixes),
			}
		}
	}
}

//nolint:funlen
func (LayerBGP) setupBaseNeighbor(bgp *BGP, conf *config.Neighbor, isUnderlay bool) {
	var neigh *BGPNeighbor

	switch {
	case conf.IP != nil:
		bgp.NeighborIPs = append(bgp.NeighborIPs, BGPNeighborIP{
			Address: *conf.IP,
		})
		neigh = &bgp.NeighborIPs[len(bgp.NeighborIPs)-1].BGPNeighbor
	case conf.Interface != nil:
		bgp.NeighborIFs = append(bgp.NeighborIFs, BGPNeighborIF{
			Interface: *conf.Interface,
			IPv6Only:  types.ToPtr(false),
		})
		neigh = &bgp.NeighborIFs[len(bgp.NeighborIFs)-1].BGPNeighbor
	default:
		return
	}

	neigh.EnforceFirstAS = types.ToPtr(true)
	neigh.RemoteAS = &conf.RemoteASN
	neigh.Timers = &BGPNeighTimers{
		KeepAliveInterval: &conf.KeepaliveTime,
		HoldTime:          &conf.HoldTime,
	}

	if isUnderlay {
		neigh.Track = types.ToPtr("bfd")
	}

	if conf.LocalASN != nil {
		neigh.LocalAS = &BGPNeighLocalAS{
			Number:    *conf.LocalASN,
			NoPrepend: types.ToPtr(true),
			ReplaceAS: types.ToPtr(true),
		}
	}

	if conf.UpdateSource != nil {
		neigh.UpdateSrc = conf.UpdateSource
		neigh.EnforceMHops = types.ToPtr(true)
	}

	dict := map[IPvX]bool{
		IPv4: conf.IPv4,
		IPv6: conf.IPv6,
	}
	for ipv, conf := range dict {
		if !conf {
			continue
		}

		if neigh.AF == nil {
			neigh.AF = &BGPNeighAF{}
		}

		var ucast *BGPNeighUcast
		if ipv == IPv4 {
			neigh.AF.UcastV4 = &BGPNeighUcast{}
			ucast = neigh.AF.UcastV4
			ucast.AllowASIn = types.ToPtr(DefaultAllowASIn)
		} else {
			neigh.AF.UcastV6 = &BGPNeighUcast{}
			ucast = neigh.AF.UcastV6
		}

		if isUnderlay {
			ucast.RouteMaps = append(ucast.RouteMaps,
				BGPNeighRouteMap{
					Name:      "TAG-FABRIC-IN",
					Direction: IN,
				},
				BGPNeighRouteMap{
					Name:      "DENY-TAG-FABRIC-OUT",
					Direction: OUT,
				},
			)
		} else {
			ucast.PrefixLists = append(ucast.PrefixLists, BGPNeighPrefixList{
				Name:      "ANY",
				Direction: IN,
			})
		}
	}

	if conf.EVPN {
		if neigh.AF == nil {
			neigh.AF = &BGPNeighAF{}
		}
		neigh.AF.EVPN = &BGPNeighEVPN{}

		if isUnderlay {
			neigh.AF.EVPN.RouteMaps = append(neigh.AF.EVPN.RouteMaps,
				BGPNeighRouteMap{
					Name:      "TAG-FABRIC-IN",
					Direction: IN,
				},
				BGPNeighRouteMap{
					Name:      "DENY-TAG-FABRIC-OUT",
					Direction: OUT,
				},
			)
		}
	}
}

func (l *LayerBGP) setupRedistributeRtMap(
	vrfName string,
	bgp *BGP,
	conf v1alpha1.Redistribute,
) {
	var bgpConnectedRtMap *string
	var bgpStaticRtMap *string

	if conf.Connected != nil {
		rtmap := vrfName + "_redist_connected"
		l.setupRouteMaps(rtmap, *conf.Connected)
		bgpConnectedRtMap = types.ToPtr("rm_" + rtmap)
	}

	if conf.Static != nil {
		rtmap := vrfName + "_redist_static"
		l.setupRouteMaps(rtmap, *conf.Static)
		bgpStaticRtMap = types.ToPtr("rm_" + rtmap)
	}

	for _, ucast := range []*BGPUcast{bgp.AF.UcastV4, bgp.AF.UcastV6} {
		if ucast == nil {
			continue
		}
		for i := range ucast.Redists {
			if ucast.Redists[i].Protocol == BGPRedistConnect {
				ucast.Redists[i].RouteMap = bgpConnectedRtMap
			}
			if ucast.Redists[i].Protocol == BGPRedistStatic {
				ucast.Redists[i].RouteMap = bgpStaticRtMap
			}
		}
	}
}

func (l *LayerBGP) setupLocalVRF(name string, conf *v1alpha1.VRF) error {
	vrf := lookupVRF(l.ns, name)
	if vrf == nil {
		return fmt.Errorf("vrf %s not found in netns %s", name, l.ns.Name)
	}

	bgp := vrf.Routing.BGP
	bgp.AS = strconv.Itoa(l.mgr.baseConfig.LocalASN)
	bgp.RouterID = &l.mgr.baseConfig.VTEPLoopbackIP
	bgp.SuppressDuplicates = types.ToPtr(false)

	bgp.AF = &BGPAddrFamily{}
	bgp.AF.UcastV4 = &BGPUcast{
		Redists: []BGPRedist{
			{
				Protocol: BGPRedistConnect,
			}, {
				Protocol: BGPRedistStatic,
			},
		},
		VRFImports: &BGPUcastVRF{
			Imports: &BGPUcastImportVRF{
				RouteMaps: []string{"rm_" + name + "_import"},
			},
		},
	}
	bgp.AF.UcastV6 = &BGPUcast{
		Redists: []BGPRedist{
			{
				Protocol: BGPRedistConnect,
			}, {
				Protocol: BGPRedistStatic,
			},
		},
		VRFImports: &BGPUcastVRF{
			Imports: &BGPUcastImportVRF{
				RouteMaps: []string{"rm_" + name + "_import"},
			},
		},
	}

	if conf.Redistribute != nil {
		l.setupRedistributeRtMap(name, bgp, *conf.Redistribute)
	}

	for _, rt := range conf.StaticRoutes {
		l.mkStaticRoute(vrf.Routing, l.convStaticRoute(rt))
	}
	for i, imprt := range conf.VRFImports {
		l.setupVRFImport(vrf, i, imprt)
	}
	for _, peer := range conf.BGPPeers {
		l.setupNeighbor(bgp, peer)
	}

	return nil
}

func (l *LayerBGP) setupFabricVRF(name string, conf *v1alpha1.FabricVRF) error {
	vni := int(conf.VNI)

	vrf := lookupVRF(l.ns, name)
	if vrf == nil {
		return fmt.Errorf("vrf %s not found in netns %s", name, l.ns.Name)
	}

	bgp := vrf.Routing.BGP
	bgp.AS = strconv.Itoa(l.mgr.baseConfig.LocalASN)
	bgp.RouterID = &l.mgr.baseConfig.VTEPLoopbackIP
	bgp.VNI = &vni
	bgp.SuppressDuplicates = types.ToPtr(false)

	bgp.AF = &BGPAddrFamily{}
	bgp.AF.UcastV4 = &BGPUcast{
		Redists: []BGPRedist{
			{
				Protocol: BGPRedistConnect,
			}, {
				Protocol: BGPRedistStatic,
			},
		},
		VRFImports: &BGPUcastVRF{
			Imports: &BGPUcastImportVRF{
				RouteMaps: []string{"rm_" + name + "_import"},
			},
		},
	}
	bgp.AF.UcastV6 = &BGPUcast{
		Redists: []BGPRedist{
			{
				Protocol: BGPRedistConnect,
			}, {
				Protocol: BGPRedistStatic,
			},
		},
		VRFImports: &BGPUcastVRF{
			Imports: &BGPUcastImportVRF{
				RouteMaps: []string{"rm_" + name + "_import"},
			},
		},
	}
	bgp.AF.EVPN = &BGPEtherVPN{
		Advertise: &BGPAdvert{
			UcastV4: &BGPAdvertUcast{
				RouteMap: types.ToPtr("rm_export_local"),
			},
			UcastV6: &BGPAdvertUcast{
				RouteMap: types.ToPtr("rm_export_local"),
			},
		},
		Exports: &BGPExportEVPN{
			RouteTargets: conf.EVPNExportRouteTargets,
		},
		Imports: &BGPImportEVPN{
			RouteTargets: conf.EVPNImportRouteTargets,
		},
	}

	if conf.EVPNExportFilter != nil {
		rtmap := vrf.Name + "_export"
		l.setupRouteMaps(rtmap, *conf.EVPNExportFilter)
		bgp.AF.EVPN.Advertise.UcastV4.RouteMap = types.ToPtr("rm_" + rtmap)
		bgp.AF.EVPN.Advertise.UcastV6.RouteMap = types.ToPtr("rm_" + rtmap)
	}

	if conf.Redistribute != nil {
		l.setupRedistributeRtMap(name, bgp, *conf.Redistribute)
	}

	for _, rt := range conf.StaticRoutes {
		l.mkStaticRoute(vrf.Routing, l.convStaticRoute(rt))
	}
	for i, imprt := range conf.VRFImports {
		l.setupVRFImport(vrf, i, imprt)
	}
	for _, peer := range conf.BGPPeers {
		l.setupNeighbor(bgp, peer)
	}

	return nil
}

//nolint:funlen
func (l *LayerBGP) setupClusterVRF() error {
	baseCfg := l.mgr.baseConfig
	name := baseCfg.ClusterVRF.Name
	vni := baseCfg.ClusterVRF.VNI
	conf := l.nodeCfg.ClusterVRF

	vrf := lookupVRF(l.ns, name)
	if vrf == nil {
		return fmt.Errorf("vrf %s not found in netns %s", name, l.ns.Name)
	}

	bgp := vrf.Routing.BGP
	bgp.AS = strconv.Itoa(baseCfg.LocalASN)
	bgp.RouterID = &baseCfg.VTEPLoopbackIP
	bgp.VNI = &vni
	bgp.SuppressDuplicates = types.ToPtr(false)

	bgp.AF = &BGPAddrFamily{}
	bgp.AF.UcastV4 = &BGPUcast{
		Redists: []BGPRedist{
			{
				Protocol: BGPRedistConnect,
			}, {
				Protocol: BGPRedistStatic,
			},
		},
		VRFImports: &BGPUcastVRF{
			Imports: &BGPUcastImportVRF{
				RouteMaps: []string{"rm_" + name + "_import"},
				VRFs:      []string{baseCfg.ManagementVRF.Name},
			},
		},
	}
	bgp.AF.UcastV6 = &BGPUcast{
		Redists: []BGPRedist{
			{
				Protocol: BGPRedistConnect,
			}, {
				Protocol: BGPRedistStatic,
			},
		},
		VRFImports: &BGPUcastVRF{
			Imports: &BGPUcastImportVRF{
				RouteMaps: []string{"rm_" + name + "_import"},
				VRFs:      []string{baseCfg.ManagementVRF.Name},
			},
		},
	}
	bgp.AF.EVPN = &BGPEtherVPN{
		Advertise: &BGPAdvert{
			UcastV4: &BGPAdvertUcast{
				RouteMap: types.ToPtr("rm_export_local"),
			},
			UcastV6: &BGPAdvertUcast{
				RouteMap: types.ToPtr("rm_export_local"),
			},
		},
		Exports: &BGPExportEVPN{
			RouteTargets: []string{baseCfg.ClusterVRF.EVPNRouteTarget},
		},
		Imports: &BGPImportEVPN{
			RouteTargets: []string{baseCfg.ClusterVRF.EVPNRouteTarget},
		},
	}

	if conf != nil {
		if conf.Redistribute != nil {
			l.setupRedistributeRtMap(name, bgp, *conf.Redistribute)
		}

		for _, rt := range conf.StaticRoutes {
			l.mkStaticRoute(vrf.Routing, l.convStaticRoute(rt))
		}
	}

	for _, cidr := range baseCfg.ExportCIDRs {
		l.mkStaticRoute(vrf.Routing,
			l.convStaticRoute(v1alpha1.StaticRoute{
				Prefix: cidr,
			}),
		)
	}
	for _, neigh := range baseCfg.ClusterNeighbors {
		if neigh.IP == nil {
			continue
		}

		mask := "/128"
		if isIPv4(*neigh.IP) {
			mask = "/32"
		}

		l.mkStaticRoute(vrf.Routing, StaticRoute{
			Destination: *neigh.IP + mask,
			NextHops: []NextHop{
				{
					NextHop: baseCfg.TrunkInterfaceName,
				},
			},
		})
	}

	for i := range baseCfg.ClusterNeighbors {
		peer := baseCfg.ClusterNeighbors[i]
		l.setupBaseNeighbor(bgp, &peer, false)
	}

	if conf := l.nodeCfg.ClusterVRF; conf != nil {
		for i, imprt := range conf.VRFImports {
			l.setupVRFImport(vrf, i, imprt)
		}
		for _, peer := range conf.BGPPeers {
			l.setupNeighbor(bgp, peer)
		}
		for i, pr := range conf.PolicyRoutes {
			if err := l.setupPolicyRoute((i + 1), pr); err != nil {
				return err
			}
		}
	}

	return nil
}

func (l *LayerBGP) setupManagementVRF() error {
	baseCfg := l.mgr.baseConfig
	name := baseCfg.ManagementVRF.Name
	vni := baseCfg.ManagementVRF.VNI

	vrf := lookupVRF(l.ns, name)
	if vrf == nil {
		return fmt.Errorf("vrf %s not found in netns %s", name, l.ns.Name)
	}

	bgp := vrf.Routing.BGP
	bgp.AS = strconv.Itoa(baseCfg.LocalASN)
	bgp.RouterID = &baseCfg.VTEPLoopbackIP
	bgp.VNI = &vni
	bgp.SuppressDuplicates = types.ToPtr(false)

	bgp.AF = &BGPAddrFamily{}
	bgp.AF.UcastV4 = &BGPUcast{
		Redists: []BGPRedist{
			{
				Protocol: BGPRedistConnect,
			}, {
				Protocol: BGPRedistStatic,
			},
		},
		VRFImports: &BGPUcastVRF{
			Imports: &BGPUcastImportVRF{
				RouteMaps: []string{"rm_" + name + "_import"},
				VRFs:      []string{baseCfg.ClusterVRF.Name},
			},
		},
	}
	bgp.AF.UcastV6 = &BGPUcast{
		Redists: []BGPRedist{
			{
				Protocol: BGPRedistConnect,
			}, {
				Protocol: BGPRedistStatic,
			},
		},
		VRFImports: &BGPUcastVRF{
			Imports: &BGPUcastImportVRF{
				RouteMaps: []string{"rm_" + name + "_import"},
				VRFs:      []string{baseCfg.ClusterVRF.Name},
			},
		},
	}
	bgp.AF.EVPN = &BGPEtherVPN{
		Advertise: &BGPAdvert{
			UcastV4: &BGPAdvertUcast{
				RouteMap: types.ToPtr("rm_export_local"),
			},
			UcastV6: &BGPAdvertUcast{
				RouteMap: types.ToPtr("rm_export_local"),
			},
		},
		Exports: &BGPExportEVPN{
			RouteTargets: []string{baseCfg.ManagementVRF.EVPNRouteTarget},
		},
		Imports: &BGPImportEVPN{
			RouteTargets: []string{baseCfg.ManagementVRF.EVPNRouteTarget},
		},
	}

	conf, ok := l.nodeCfg.FabricVRFs[name]
	if ok {
		if conf.Redistribute != nil {
			l.setupRedistributeRtMap(name, bgp, *conf.Redistribute)
		}

		for _, rt := range conf.StaticRoutes {
			l.mkStaticRoute(vrf.Routing, l.convStaticRoute(rt))
		}
		for i, imprt := range conf.VRFImports {
			l.setupVRFImport(vrf, i, imprt)
		}
	}

	return nil
}

func (l *LayerBGP) setupDefaultVRF() {
	baseCfg := l.mgr.baseConfig

	bgp := l.ns.Routing.BGP
	bgp.AS = strconv.Itoa(baseCfg.LocalASN)
	bgp.RouterID = &baseCfg.VTEPLoopbackIP
	bgp.SuppressDuplicates = types.ToPtr(false)
	bgp.EBGPNeedPolicy = types.ToPtr(false)

	bgp.Bestpath = &BGPBestpath{
		ASPath: &BGPBestpathASPath{
			MultipathRelax: types.ToPtr(BGPMultipathRelaxNoSet),
		},
	}

	bgp.AF = &BGPAddrFamily{
		UcastV4: &BGPUcast{
			Network: []BGPUcastNetwork{
				{
					Prefix: baseCfg.VTEPLoopbackIP + "/32",
				},
			},
		},
		EVPN: &BGPEtherVPN{
			AdvertAllVNI: types.ToPtr(true),
		},
	}

	for i := range baseCfg.UnderlayNeighbors {
		peer := baseCfg.UnderlayNeighbors[i]
		l.setupBaseNeighbor(bgp, &peer, true)
	}

	for _, l2 := range l.nodeCfg.Layer2s {
		if l2.RouteTarget == "" {
			continue
		}
		bgp.AF.EVPN.VNIs = append(bgp.AF.EVPN.VNIs, BGPVniEVPN{
			VNI: int(l2.VNI),
			Exports: &BGPExportEVPN{
				RouteTargets: []string{l2.RouteTarget},
			},
			Imports: &BGPImportEVPN{
				RouteTargets: []string{l2.RouteTarget},
			},
		})
	}
}

//nolint:funlen
func (l *LayerBGP) setupGlobalConfiguration() {
	for _, cidr := range l.mgr.baseConfig.ExportCIDRs {
		le := 128
		if isIPv4(cidr) {
			le = 32
		}
		l.mkPrefixList(getIPvX(cidr), "pl_export_base", PrefixListSeq{
			Num:     DefaultPrefixListSeqNum,
			Policy:  Permit,
			Address: types.ToPtr(cidr),
			LE:      &le,
		})
	}

	l.mkPrefixList(IPv4, "pl_link_local", PrefixListSeq{
		Num:     DefaultPrefixListSeqNum,
		Policy:  Permit,
		Address: types.ToPtr("169.254.0.0/16"),
		LE:      types.ToPtr(32), //nolint:mnd
	})
	l.mkPrefixList(IPv6, "pl_link_local", PrefixListSeq{
		Num:     DefaultPrefixListSeqNum,
		Policy:  Permit,
		Address: types.ToPtr("fd00:7:caa5::/48"),
		LE:      types.ToPtr(128), //nolint:mnd
	})

	l.mkPrefixList(IPv4, "ANY", PrefixListSeq{
		Num:    DefaultPrefixListSeqNum,
		Policy: Permit,
	})
	l.mkPrefixList(IPv6, "ANY", PrefixListSeq{
		Num:    DefaultPrefixListSeqNum,
		Policy: Permit,
	})

	l.mkPrefixList(IPv4, "DEFAULT", PrefixListSeq{
		Num:     DefaultPrefixListSeqNum,
		Policy:  Permit,
		Address: types.ToPtr("0.0.0.0/0"),
	})
	l.mkPrefixList(IPv6, "DEFAULT", PrefixListSeq{
		Num:     DefaultPrefixListSeqNum,
		Policy:  Permit,
		Address: types.ToPtr("::/0"),
	})

	l.mkCommunityList("cm-received-fabric", BGPCommunityListSeq{
		Num:    DefaultCommunityListSeqNum,
		Policy: Permit,
		Attrs:  []string{"65169:200"},
	})

	//nolint:mnd
	l.mkRouteMap("TAG-FABRIC-IN", RtMapSeq{
		Num:    10,
		Policy: Permit,
		Set: &RtMapSet{
			Community: &RtMapSetCommunity{
				Add: &RtMapSetCommAdd{
					Attrs: []string{"65169:200"},
				},
			},
			LocalPreference: types.ToPtr(100),
		},
	})

	//nolint:mnd
	l.mkRouteMap("DENY-TAG-FABRIC-OUT",
		RtMapSeq{
			Num:    10,
			Policy: Deny,
			Match: &RtMapMatch{
				Community: &RtMapMatchCommunity{
					ID: "cm-received-fabric",
				},
			},
		},
		RtMapSeq{
			Num:    20,
			Policy: Permit,
		},
	)

	//nolint:mnd
	l.mkRouteMap("rm_export_local",
		RtMapSeq{
			Num:    10,
			Policy: Deny,
			Match: &RtMapMatch{
				Community: &RtMapMatchCommunity{
					ID: "cm-received-fabric",
				},
			},
		},
		RtMapSeq{
			Num:    11,
			Policy: Deny,
			Match: &RtMapMatch{
				IPv4: &RtMapMatchIP{
					PrefixList: types.ToPtr("pl_link_local"),
				},
			},
		},
		RtMapSeq{
			Num:    12,
			Policy: Deny,
			Match: &RtMapMatch{
				IPv6: &RtMapMatchIP{
					PrefixList: types.ToPtr("pl_link_local"),
				},
			},
		},
		RtMapSeq{
			Num:    20,
			Policy: Permit,
		},
	)

	//nolint:mnd
	l.mkRouteMap("rm_cluster_import",
		RtMapSeq{
			Num:    1,
			Policy: Permit,
			Match: &RtMapMatch{
				IPv4: &RtMapMatchIP{
					PrefixList: types.ToPtr("ANY"),
				},
			},
			Set: &RtMapSet{
				IPv4: &RtMapSetIP{
					NextHop: types.ToPtr("0.0.0.0"),
				},
			},
			OnMatch: types.ToPtr("next"),
		},
		RtMapSeq{
			Num:    2,
			Policy: Permit,
			Set: &RtMapSet{
				LocalPreference: types.ToPtr(50),
			},
			OnMatch: types.ToPtr("next"),
		},
		RtMapSeq{
			Num:    65533,
			Policy: Deny,
			Match: &RtMapMatch{
				IPv4: &RtMapMatchIP{
					PrefixList: types.ToPtr("DEFAULT"),
				},
				SourceVRF: &l.mgr.baseConfig.ManagementVRF.Name,
			},
		},
		RtMapSeq{
			Num:    65534,
			Policy: Deny,
			Match: &RtMapMatch{
				IPv6: &RtMapMatchIP{
					PrefixList: types.ToPtr("DEFAULT"),
				},
				SourceVRF: &l.mgr.baseConfig.ManagementVRF.Name,
			},
		},
		RtMapSeq{
			Num:    65535,
			Policy: Permit,
			Match: &RtMapMatch{
				SourceVRF: &l.mgr.baseConfig.ManagementVRF.Name,
			},
		},
	)

	//nolint:mnd
	l.mkRouteMap("rm_"+l.mgr.baseConfig.ManagementVRF.Name+"_import",
		RtMapSeq{
			Num:    2,
			Policy: Permit,
			Match: &RtMapMatch{
				IPv4: &RtMapMatchIP{
					PrefixList: types.ToPtr("pl_export_base"),
				},
				SourceVRF: &l.mgr.baseConfig.ClusterVRF.Name,
			},
		},
		RtMapSeq{
			Num:    3,
			Policy: Permit,
			Match: &RtMapMatch{
				IPv6: &RtMapMatchIP{
					PrefixList: types.ToPtr("pl_export_base"),
				},
				SourceVRF: &l.mgr.baseConfig.ClusterVRF.Name,
			},
		},
	)
}

func (l *LayerBGP) setup() error {
	l.setupGlobalConfiguration()
	l.setupDefaultVRF()

	if err := l.setupManagementVRF(); err != nil {
		return fmt.Errorf("failed to setup BGP management VRF: %w", err)
	}

	if err := l.setupClusterVRF(); err != nil {
		return fmt.Errorf("failed to setup BGP cluster VRF: %w", err)
	}

	for name := range l.nodeCfg.FabricVRFs {
		fabric := l.nodeCfg.FabricVRFs[name]
		if l.mgr.isReservedVRF(name) {
			continue
		}
		if err := l.setupFabricVRF(name, &fabric); err != nil {
			return fmt.Errorf("failed to setup BGP fabric VRF %s: %w", name, err)
		}
	}

	for name := range l.nodeCfg.LocalVRFs {
		local := l.nodeCfg.LocalVRFs[name]
		if l.mgr.isReservedVRF(name) {
			continue
		}
		if err := l.setupLocalVRF(name, &local); err != nil {
			return fmt.Errorf("failed to setup BGP local VRF %s: %w", name, err)
		}
	}

	return nil
}
