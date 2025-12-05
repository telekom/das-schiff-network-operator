package monitoring

import (
	"errors"
	"sync"
	"time"

	"github.com/go-logr/logr"
	"github.com/prometheus/client_golang/prometheus"
	ctrl "sigs.k8s.io/controller-runtime"
)

// Namespace defines the common namespace to be used by all metrics.
const (
	namespace        = "nwop"
	defaultEnabled   = true
	defaultDisabled  = false
	defaultVRF       = "main"
	defaultVRFIntern = "default"
)

var (
	scrapeDurationDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "scrape", "collector_duration_seconds"),
		"das_schiff_network_operator: Duration of a collector scrape.",
		[]string{"collector"},
		nil,
	)
	scrapeSuccessDesc = prometheus.NewDesc(
		prometheus.BuildFQName(namespace, "scrape", "collector_success"),
		"das_schiff_network_operator: Whether a collector succeeded.",
		[]string{"collector"},
		nil,
	)
	collectorLogger = ctrl.Log.WithName("collector")
)

var (
	factories              = make(map[string]func() (Collector, error))
	initiatedCollectorsMtx = sync.Mutex{}
	initiatedCollectors    = make(map[string]Collector)
	collectorState         = make(map[string]*bool)
)

type typedFactoryDesc struct {
	desc      *prometheus.Desc
	valueType prometheus.ValueType
}

type basicCollector struct {
	name     string
	mu       sync.Mutex
	wg       sync.WaitGroup
	channels []chan<- prometheus.Metric
	logger   logr.Logger
}

func (c *basicCollector) clearChannels() {
	c.channels = []chan<- prometheus.Metric{}
}
func (d *typedFactoryDesc) mustNewConstMetric(value float64, labels ...string) prometheus.Metric {
	return prometheus.MustNewConstMetric(d.desc, d.valueType, value, labels...)
}

func registerCollector(collector string, isDefaultEnabled bool, factory func() (Collector, error)) {
	collectorState[collector] = &isDefaultEnabled
	factories[collector] = factory
}

// DasSchiffNetworkOperatorCollector implements the prometheus.Collector interface.
type DasSchiffNetworkOperatorCollector struct {
	Collectors map[string]Collector
}

// NewDasSchiffNetworkOperatorCollector creates a new DasSchiffNetworkOperatorCollector.
func NewDasSchiffNetworkOperatorCollector(collectorConfig map[string]bool) (*DasSchiffNetworkOperatorCollector, error) {
	collectors := make(map[string]Collector)
	initiatedCollectorsMtx.Lock()
	defer initiatedCollectorsMtx.Unlock()
	for key, enabled := range collectorState {
		if collectorEnabled, ok := collectorConfig[key]; ok {
			// This overwrites the default state of the Collector.
			enabled = &collectorEnabled
		}
		if !*enabled {
			continue
		}
		if collector, ok := initiatedCollectors[key]; ok {
			collectors[key] = collector
		} else {
			collector, err := factories[key]()
			if err != nil {
				return nil, err
			}
			collectors[key] = collector
			initiatedCollectors[key] = collector
		}
	}
	return &DasSchiffNetworkOperatorCollector{Collectors: collectors}, nil
}

// Describe implements the prometheus.Collector interface.
func (DasSchiffNetworkOperatorCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- scrapeDurationDesc
	ch <- scrapeSuccessDesc
}

// Collect implements the prometheus.Collector interface.
func (n DasSchiffNetworkOperatorCollector) Collect(ch chan<- prometheus.Metric) {
	wg := sync.WaitGroup{}
	for name, c := range n.Collectors {
		wg.Add(1)
		go func(name string, c Collector) {
			defer wg.Done()
			execute(name, c, ch)
		}(name, c)
	}
	wg.Wait()
}

func execute(name string, c Collector, ch chan<- prometheus.Metric) {
	begin := time.Now()
	err := c.Update(ch)
	duration := time.Since(begin)
	var success float64

	if err != nil {
		if IsNoDataError(err) {
			collectorLogger.Error(err, "collector returned no data", "name", name, "duration_seconds", duration.Seconds())
		} else {
			collectorLogger.Error(err, "collector failed", "name", name, "duration_seconds", duration.Seconds())
		}
		success = 0
	} else {
		collectorLogger.Info("collector succeeded", "name", name, "duration_seconds", duration.Seconds())
		success = 1
	}
	ch <- prometheus.MustNewConstMetric(scrapeDurationDesc, prometheus.GaugeValue, duration.Seconds(), name)
	ch <- prometheus.MustNewConstMetric(scrapeSuccessDesc, prometheus.GaugeValue, success, name)
}

// Collector is the interface a collector has to implement.
type Collector interface {
	// Get new metrics and expose them via prometheus registry.
	Update(ch chan<- prometheus.Metric) error
}

// ErrNoData indicates the collector found no data to collect, but had no other error.
var ErrNoData = errors.New("collector returned no data")

func IsNoDataError(err error) bool {
	return errors.Is(err, ErrNoData)
}
