package metrics

import (
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// Mirroring outcomes, used as the result label of MirrorRequestsTotal.
const (
	MirrorSent    = "sent"
	MirrorFailed  = "failed"
	MirrorDropped = "dropped"
)

var (
	HTTPRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests processed",
		},
		[]string{"method", "upstream", "status"},
	)

	HTTPRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name:    "http_request_duration_seconds",
			Help:    "Histogram of HTTP request latencies",
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "upstream", "status"},
	)

	ConfigReloadErrorsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "config_reload_errors_total",
			Help: "Total number of config reload errors",
		},
		[]string{"reason"},
	)

	MirrorRequestsTotal = newMirrorRequestsTotal()
)

// newMirrorRequestsTotal is a constructor so tests can build a counter set that
// does not collide with the registered one.
func newMirrorRequestsTotal() *prometheus.CounterVec {
	return prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "mirror_requests_total",
			Help: "Total number of mirrored requests by result",
		},
		[]string{"result"},
	)
}

type RequestMetrics struct {
	RequestsTotal   *prometheus.CounterVec
	RequestDuration *prometheus.HistogramVec
}

var Default = &RequestMetrics{
	RequestsTotal:   HTTPRequestsTotal,
	RequestDuration: HTTPRequestDuration,
}

// Observe records a single request. upstream is the matched upstream name
// rather than the request path: names are bounded by the config, paths are not.
func (m *RequestMetrics) Observe(method, upstream string, status int, d time.Duration) {
	s := strconv.Itoa(status)
	m.RequestsTotal.WithLabelValues(method, upstream, s).Inc()
	m.RequestDuration.WithLabelValues(method, upstream, s).Observe(d.Seconds())
}

type MirrorMetrics struct {
	RequestsTotal *prometheus.CounterVec
}

var Mirror = &MirrorMetrics{RequestsTotal: MirrorRequestsTotal}

// NewMirrorMetrics returns an unregistered counter set, for tests.
func NewMirrorMetrics() *MirrorMetrics {
	return &MirrorMetrics{RequestsTotal: newMirrorRequestsTotal()}
}

func (m *MirrorMetrics) Observe(result string) {
	m.RequestsTotal.WithLabelValues(result).Inc()
}
