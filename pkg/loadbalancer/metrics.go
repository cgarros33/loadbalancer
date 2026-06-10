package loadbalancer

import (
	"github.com/prometheus/client_golang/prometheus"
)

// latencyBuckets covers 1ms–2s with fine spacing in the tail range expected
// under Prequal experiments (~5ms baseline, saturation tails up to ~500ms).
var latencyBuckets = []float64{
	0.001, 0.005, 0.010, 0.015, 0.020, 0.025, 0.030, 0.040, 0.050,
	0.075, 0.100, 0.150, 0.200, 0.300, 0.500, 1.0, 2.0,
}

type Metrics struct {
	requestDuration *prometheus.HistogramVec
	rifAtDispatch   *prometheus.HistogramVec
	activeRequests  *prometheus.GaugeVec
	serverHealth    *prometheus.GaugeVec
	serverRIF       *prometheus.GaugeVec
	selectionsTotal *prometheus.CounterVec
}

func NewMetrics() *Metrics {
	m := &Metrics{
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "request_duration_seconds",
				Help:    "End-to-end request latency as seen by the load balancer",
				Buckets: latencyBuckets,
			},
			[]string{"algorithm"},
		),
		rifAtDispatch: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "rif_at_dispatch",
				Help:    "RIF of the selected server at the moment the request was dispatched",
				Buckets: []float64{0, 1, 2, 4, 8, 12, 16, 24, 32, 48, 64},
			},
			[]string{"algorithm"},
		),
		activeRequests: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "active_requests",
				Help: "Number of requests currently being processed",
			},
			[]string{"algorithm"},
		),
		serverHealth: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "server_health",
				Help: "Health status of servers (1=healthy, 0=unhealthy)",
			},
			[]string{"server_id", "algorithm"},
		),
		serverRIF: prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "server_rif",
				Help: "Requests in flight per server (LB-local counter)",
			},
			[]string{"server_id", "algorithm"},
		),
		selectionsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "selections_total",
				Help: "Number of backend selections by HCL category (cold/hot/fallback)",
			},
			[]string{"algorithm", "type"},
		),
	}

	prometheus.MustRegister(m.requestDuration)
	prometheus.MustRegister(m.rifAtDispatch)
	prometheus.MustRegister(m.activeRequests)
	prometheus.MustRegister(m.serverHealth)
	prometheus.MustRegister(m.serverRIF)
	prometheus.MustRegister(m.selectionsTotal)

	return m
}
