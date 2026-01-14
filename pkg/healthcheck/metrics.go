// Package healthcheck provides metrics for network operator health checks.
package healthcheck

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"sigs.k8s.io/controller-runtime/pkg/metrics"
)

const (
	metricsNamespace = "nwop"
	metricsSubsystem = "healthcheck"
	labelCheck       = "check"
	labelReason      = "reason"

	// HealthCheckTypeInterfaces is the check type for interface health checks.
	HealthCheckTypeInterfaces = "interfaces"
	// HealthCheckTypeReachability is the check type for reachability health checks.
	HealthCheckTypeReachability = "reachability"
	// HealthCheckTypeAPIServer is the check type for API server health checks.
	HealthCheckTypeAPIServer = "apiserver"
)

var (
	// HealthCheckStatus is a gauge that indicates the status of each health check
	// (1 = passing, 0 = failing).
	HealthCheckStatus = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "status",
			Help:      "Status of network operator health checks (1=passing, 0=failing)",
		},
		[]string{labelCheck},
	)

	// HealthCheckLastSuccess is a gauge that records the Unix timestamp of the last successful health check.
	HealthCheckLastSuccess = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "last_success_timestamp_seconds",
			Help:      "Unix timestamp of the last successful health check",
		},
		[]string{labelCheck},
	)

	// HealthCheckDuration is a histogram that records the duration of health checks.
	HealthCheckDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "duration_seconds",
			Help:      "Duration of health check execution in seconds",
			Buckets:   []float64{0.001, 0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10},
		},
		[]string{labelCheck},
	)

	// NodeReadinessCondition is a gauge that indicates the current node readiness condition
	// (1 = True, 0 = False, -1 = Unknown).
	NodeReadinessCondition = prometheus.NewGaugeVec(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "node_ready",
			Help:      "Node readiness condition status (1=True, 0=False, -1=Unknown)",
		},
		[]string{labelReason},
	)

	// TaintsRemoved is a gauge that indicates whether taints have been removed
	// (1 = removed, 0 = still present).
	TaintsRemoved = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Namespace: metricsNamespace,
			Subsystem: metricsSubsystem,
			Name:      "taints_removed",
			Help:      "Whether initialization taints have been removed from the node (1=removed, 0=present)",
		},
	)
)

func init() {
	// Register metrics with the controller-runtime metrics registry
	metrics.Registry.MustRegister(
		HealthCheckStatus,
		HealthCheckLastSuccess,
		HealthCheckDuration,
		NodeReadinessCondition,
		TaintsRemoved,
	)
}

// RecordHealthCheckResult records the result of a health check.
func RecordHealthCheckResult(checkName string, success bool, duration time.Duration) {
	if success {
		HealthCheckStatus.WithLabelValues(checkName).Set(1)
		HealthCheckLastSuccess.WithLabelValues(checkName).SetToCurrentTime()
	} else {
		HealthCheckStatus.WithLabelValues(checkName).Set(0)
	}
	HealthCheckDuration.WithLabelValues(checkName).Observe(duration.Seconds())
}

// RecordNodeReadinessCondition records the node readiness condition status.
func RecordNodeReadinessCondition(ready bool, reason string) {
	// Reset all reasons first to ensure only one is active
	NodeReadinessCondition.Reset()
	if ready {
		NodeReadinessCondition.WithLabelValues(reason).Set(1)
	} else {
		NodeReadinessCondition.WithLabelValues(reason).Set(0)
	}
}

// RecordTaintsRemoved records whether taints have been removed.
func RecordTaintsRemoved(removed bool) {
	if removed {
		TaintsRemoved.Set(1)
	} else {
		TaintsRemoved.Set(0)
	}
}
