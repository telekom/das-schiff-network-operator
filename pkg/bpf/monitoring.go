package bpf

import (
	"fmt"

	"github.com/cilium/ebpf"
	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

var (
	epbfReturnReasons = []string{
		"route", "route_noneigh", "err_parse_headers", "not_fwd", "err_store_mac", "err_buffer_size", "err_fallthrough",
	}
	ebpfFibLookuPResult = []string{
		"success", "blackhole", "unreacheable", "prohibit", "not_fwded", "fwd_disabled", "unsupp_lwt", "no_neigh", "frag_needed",
	}
)

func initMonitoring() {
	registerMap(router.routerMaps.EbpfRetStatsMap, "ebpf_return_reasons", epbfReturnReasons)
	registerMap(router.routerMaps.EbpfFibLkupStatsMap, "ebpf_fib_lookup", ebpfFibLookuPResult)
}

type StatsRecord struct {
	RXPackets uint64
	RXBytes   uint64
}

func registerMap(m *ebpf.Map, prefix string, keys []string) {
	mapInfo, err := m.Info()
	if err != nil {
		panic(err)
	}
	mapID, _ := mapInfo.ID()
	_, _ = fmt.Printf("adding monitoring for map %s (id: %d) with prefix %s\n", mapInfo.Name, mapID, prefix)
	for idx, name := range keys {
		// eBPF map by packets
		metrics.Registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: prefix + "_packets",
			ConstLabels: prometheus.Labels{
				"key": name,
			},
		}, func() float64 {
			stats := fetchEbpfStatistics(m, uint32(idx))
			return float64(stats.RXPackets)
		}))

		// eBPF map by bytes
		metrics.Registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: prefix + "_bytes",
			ConstLabels: prometheus.Labels{
				"key": name,
			},
		}, func() float64 {
			stats := fetchEbpfStatistics(m, uint32(idx))
			return float64(stats.RXBytes)
		}))
	}
}

func fetchEbpfStatistics(m *ebpf.Map, key uint32) *StatsRecord {
	var perCPUStats []*StatsRecord
	err := m.Lookup(key, &perCPUStats)
	if err != nil {
		_, _ = fmt.Printf("Error reading eBPF statistics from map: %v\n", err)
		return nil
	}
	var aggregatedStats StatsRecord
	for _, stat := range perCPUStats {
		aggregatedStats.RXBytes += stat.RXBytes
		aggregatedStats.RXPackets += stat.RXPackets
	}
	return &aggregatedStats
}
