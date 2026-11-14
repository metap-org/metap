// GET /metrics — Prometheus text exposition.
// Public (no auth), same convention as /health.
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
// chi.RouteContext(r.Context()).RoutePattern() back off the request once next.ServeHTTP returns,
// the same "read state chi populated during the call" trick runtime.RequestContext's own doc
// comment would recognize. Falls back to the raw path (never empty, unlike a pattern on a 404) when
// nothing matched.
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

// statusCapturingWriter records the status code written so the metrics middleware can label the
// request after the handler returns.
type statusCapturingWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusCapturingWriter) WriteHeader(status int) {
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}
