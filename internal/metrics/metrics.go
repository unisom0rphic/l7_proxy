package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests processed",
		},
		[]string{"method", "path", "status"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Histogram of HTTP request latencies",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path", "status"},
	)

	ConfigReloadErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "config_reload_errors_total",
			Help: "Total number of config reload errors",
		},
		[]string{"reason"},
	)
)

type RequestMetrics struct {
	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec
}

var Default = &RequestMetrics{
	RequestsTotal:   HTTPRequestsTotal,
	RequestDuration: HTTPRequestDuration,
}

func (m *RequestMetrics) Observe(method, path string, status int, d time.Duration) {
	s := strconv.Itoa(status)
	m.RequestsTotal.WithLabelValues(method, path, s).Inc()
	m.RequestDuration.WithLabelValues(method, path, s).Observe(d.Seconds())
}
