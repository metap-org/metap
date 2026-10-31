// GET /metrics — Prometheus text exposition, the Go counterpart to the Rust metap-http's
// axum-prometheus wiring (PrometheusMetricLayerBuilder + a process_collector reading /proc).
// client_golang's own collectors.NewProcessCollector/NewGoCollector stand in for that process
// collector — same "CPU/RSS/fd" coverage (Go's collector has no per-thread-count metric the way
// the Rust one does; a small, accepted gap, not a design choice).
//
// Public (no auth), same convention as /health — see the Rust original's own doc comment for why
// this genuinely leaks more than a bare liveness check (per-route request counts/latencies, plus
// process resource usage) and why that's deliberate for now rather than an oversight.
package runtime

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// MetricsRegistry bundles a Prometheus registry with the HTTP request metrics Middleware records
// into it.
type MetricsRegistry struct {
	Registry      *prometheus.Registry
	requestsTotal *prometheus.CounterVec
	duration      *prometheus.HistogramVec
	inFlight      prometheus.Gauge
}

// NewMetricsRegistry builds a registry with the process/Go runtime collectors plus this
// package's own per-route HTTP metrics already registered.
func NewMetricsRegistry() *MetricsRegistry {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(collectors.NewGoCollector())

	m := &MetricsRegistry{
		Registry: reg,
		requestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "http_requests_total", Help: "Total HTTP requests handled.",
		}, []string{"method", "route", "status"}),
		duration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name: "http_request_duration_seconds", Help: "HTTP request latency in seconds.",
		}, []string{"method", "route", "status"}),
		inFlight: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "http_requests_in_flight", Help: "HTTP requests currently being handled.",
		}),
	}
	reg.MustRegister(m.requestsTotal, m.duration, m.inFlight)
	return m
}

// Middleware records request count/duration per (method, matched route pattern, status) plus a
// single in-flight gauge. Must run after chi's routing has matched a pattern — reads
// chi.RouteContext(r.Context()).RoutePattern() back off the request once next.ServeHTTP
// returns, the same "read state chi populated during the call" trick runtime.RequestContext's
// own doc comment would recognize. Falls back to the raw path (never empty, unlike a pattern on
// a 404) when nothing matched, same as a 404 would have no pattern in the Rust axum-prometheus
// layer either.
func (m *MetricsRegistry) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusCapturingWriter{ResponseWriter: w, status: http.StatusOK}

		m.inFlight.Inc()
		defer m.inFlight.Dec()

		next.ServeHTTP(sw, r)

		route := r.URL.Path
		if rctx := chi.RouteContext(r.Context()); rctx != nil {
			if pattern := rctx.RoutePattern(); pattern != "" {
				route = pattern
			}
		}
		status := strconv.Itoa(sw.status)
		m.requestsTotal.WithLabelValues(r.Method, route, status).Inc()
		m.duration.WithLabelValues(r.Method, route, status).Observe(time.Since(start).Seconds())
	})
}

// Handler is the GET /metrics exposition endpoint.
func (m *MetricsRegistry) Handler() http.Handler {
	return promhttp.HandlerFor(m.Registry, promhttp.HandlerOpts{})
}
