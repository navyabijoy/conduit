package gateway

import (
	"github.com/prometheus/client_golang/prometheus"
)

var (
	requestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "conduit_request_duration_seconds",
			Help:    "Latency of API requests through connector endpoints.",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"connector_id", "endpoint", "status"},
	)

	requestErrors = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "conduit_request_errors_total",
			Help: "Total number of request execution errors.",
		},
		[]string{"connector_id", "endpoint", "error_type"},
	)

	tokenRefreshes = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "conduit_token_refreshes_total",
			Help: "Total number of OAuth token refresh executions.",
		},
		[]string{"connector_id", "status"},
	)

	webhookFailures = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "conduit_webhook_failures_total",
			Help: "Total number of failed webhook deliveries.",
		},
		[]string{"connector_id", "reason"},
	)

	driftDetections = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "conduit_drift_detections_total",
			Help: "Total schema drift scans executed.",
		},
		[]string{"connector_id", "result"},
	)
)

func init() {
	prometheus.MustRegister(requestDuration)
	prometheus.MustRegister(requestErrors)
	prometheus.MustRegister(tokenRefreshes)
	prometheus.MustRegister(webhookFailures)
	prometheus.MustRegister(driftDetections)
}

func RecordRequestDuration(connectorID, endpoint, status string, durationSeconds float64) {
	requestDuration.WithLabelValues(connectorID, endpoint, status).Observe(durationSeconds)
}

func RecordRequestError(connectorID, endpoint, errType string) {
	requestErrors.WithLabelValues(connectorID, endpoint, errType).Inc()
}

func RecordTokenRefresh(connectorID, status string) {
	tokenRefreshes.WithLabelValues(connectorID, status).Inc()
}

func RecordWebhookFailure(connectorID, reason string) {
	webhookFailures.WithLabelValues(connectorID, reason).Inc()
}

func RecordDriftDetection(connectorID, status string) {
	driftDetections.WithLabelValues(connectorID, status).Inc()
}
