package bpf

import (
	"github.com/cilium/ebpf"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	ctrl "sigs.k8s.io/controller-runtime"
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
	logger := ctrl.Log.WithName("bpf-monitoring")
	registerMap(router.routerMaps.EbpfRetStatsMap, "ebpf_return_reasons", epbfReturnReasons, logger)
	registerMap(router.routerMaps.EbpfFibLkupStatsMap, "ebpf_fib_lookup", ebpfFibLookuPResult, logger)
}

type StatsRecord struct {
	RXPackets uint64
	RXBytes   uint64
}

func registerMap(m *ebpf.Map, prefix string, keys []string, logger logr.Logger) {
	mapInfo, err := m.Info()
	if err != nil {
		panic(err)
	}
	mapID, _ := mapInfo.ID()
	logger.Info("adding monitoring for map %s (id: %d) with prefix %s", mapInfo.Name, mapID, prefix)
	for idx, name := range keys {
		// eBPF map by packets
		metrics.Registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: prefix + "_packets",
			ConstLabels: prometheus.Labels{
				"key": name,
			},
		}, func() float64 {
			stats := fetchEbpfStatistics(m, uint32(idx), logger)
			return float64(stats.RXPackets)
		}))

		// eBPF map by bytes
		metrics.Registry.MustRegister(prometheus.NewCounterFunc(prometheus.CounterOpts{
			Name: prefix + "_bytes",
			ConstLabels: prometheus.Labels{
				"key": name,
			},
		}, func() float64 {
			stats := fetchEbpfStatistics(m, uint32(idx), logger)
			return float64(stats.RXBytes)
		}))
	}
}

func fetchEbpfStatistics(m *ebpf.Map, key uint32, logger logr.Logger) *StatsRecord {
	var perCPUStats []*StatsRecord
	err := m.Lookup(key, &perCPUStats)
	if err != nil {
		logger.Error(err, "error reading eBPF statistics from map")
		return nil
	}
	var aggregatedStats StatsRecord
	for _, stat := range perCPUStats {
		aggregatedStats.RXBytes += stat.RXBytes
		aggregatedStats.RXPackets += stat.RXPackets
	}
	return &aggregatedStats
}
