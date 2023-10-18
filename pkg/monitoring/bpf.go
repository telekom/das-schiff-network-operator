package monitoring

import (
	"github.com/cilium/ebpf"
	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/telekom/das-schiff-network-operator/pkg/bpf"
	ctrl "sigs.k8s.io/controller-runtime"
)

type bpfCollector struct {
	returnReasonsPacketsDesc typedFactoryDesc
	returnReasonsBytesDesc   typedFactoryDesc
	fibLookupPacketsDesc     typedFactoryDesc
	fibLookupBytesDesc       typedFactoryDesc
	logger                   logr.Logger
}

type statsRecord struct {
	RXPackets uint64
	RXBytes   uint64
}

var (
	epbfReturnReasons = []string{
		"route", "route_noneigh", "err_parse_headers", "not_fwd", "err_store_mac", "err_buffer_size", "err_fallthrough",
	}
	ebpfFibLookupResult = []string{
		"success", "blackhole", "unreacheable", "prohibit", "not_fwded", "fwd_disabled", "unsupp_lwt", "no_neigh", "frag_needed",
	}
)

func init() {
	registerCollector("bpf", defaultEnabled, NewBPFCollector)
}

func NewBPFCollector() (Collector, error) {
	collector := bpfCollector{
		returnReasonsPacketsDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "bpf", "return_reasons_packets"),
				"The BPF tc_router program return reasons",
				[]string{"key"},
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		returnReasonsBytesDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "bpf", "return_reasons_bytes"),
				"The BPF tc_router program return reasons",
				[]string{"key"},
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		fibLookupPacketsDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "bpf", "fib_lookup_packets"),
				"The BPF tc_router program lookup results",
				[]string{"key"},
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		fibLookupBytesDesc: typedFactoryDesc{
			desc: prometheus.NewDesc(
				prometheus.BuildFQName(namespace, "bpf", "fib_lookup_bytes"),
				"The BPF tc_router program lookup results",
				[]string{"key"},
				nil,
			),
			valueType: prometheus.CounterValue,
		},
		logger: ctrl.Log.WithName("bpf.collector"),
	}

	return &collector, nil
}

func (c *bpfCollector) fetchEbpfStatistics(m *ebpf.Map, key uint32) (*statsRecord, error) {
	var perCPUStats []*statsRecord
	err := m.Lookup(key, &perCPUStats)
	if err != nil {
		return nil, err
	}
	var aggregatedStats statsRecord
	for _, stat := range perCPUStats {
		aggregatedStats.RXBytes += stat.RXBytes
		aggregatedStats.RXPackets += stat.RXPackets
	}
	return &aggregatedStats, nil
}

func (c *bpfCollector) updateReturnReasons(ch chan<- prometheus.Metric) error {
	if bpf.EbpfRetStatsMap() == nil {
		return ErrNoData
	}
	for idx, name := range epbfReturnReasons {
		stats, err := c.fetchEbpfStatistics(bpf.EbpfRetStatsMap(), uint32(idx))
		if err != nil {
			return err
		}
		ch <- c.returnReasonsPacketsDesc.mustNewConstMetric(float64(stats.RXPackets), name)
		ch <- c.returnReasonsBytesDesc.mustNewConstMetric(float64(stats.RXBytes), name)
	}
	return nil
}

func (c *bpfCollector) updateFibLookupResults(ch chan<- prometheus.Metric) error {
	if bpf.EbpfFibLkupStatsMap() == nil {
		return ErrNoData
	}
	for idx, name := range ebpfFibLookupResult {
		stats, err := c.fetchEbpfStatistics(bpf.EbpfFibLkupStatsMap(), uint32(idx))
		if err != nil {
			return err
		}
		ch <- c.fibLookupPacketsDesc.mustNewConstMetric(float64(stats.RXPackets), name)
		ch <- c.fibLookupBytesDesc.mustNewConstMetric(float64(stats.RXBytes), name)
	}
	return nil
}

func (c *bpfCollector) Update(ch chan<- prometheus.Metric) error {
	if err := c.updateReturnReasons(ch); err != nil {
		return err
	}
	if err := c.updateFibLookupResults(ch); err != nil {
		return err
	}
	return nil
}
