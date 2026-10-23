package metrics

import (
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ──────────────────────────────────────────────────────────────
// 1. DEFINE THE METRICS
// ──────────────────────────────────────────────────────────────
//
// We follow the RED method (Rate, Errors, Duration):
//
//   • http_requests_total        → COUNTER  (rate + errors)
//   • http_request_duration_seconds → HISTOGRAM (duration / latency)
//   • http_requests_in_flight      → GAUGE     (saturation)
//
// Each metric has LABELS so you can slice and dice the data in
// Grafana (e.g. "show me only POST /api/course 500 errors").

var (
	// HttpRequestsTotal counts every HTTP request that passes through
	// the middleware. Labels let us filter by method, path, and status.
	//
	// Example Prometheus query:  rate(http_requests_total[1m])
	// → "how many requests per second in the last minute?"
	HttpRequestsTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "http_requests_total",
			Help: "Total number of HTTP requests processed, labelled by method, path, and status code.",
		},
		[]string{"method", "path", "status"},
	)

	// HttpRequestDuration measures how long each request takes.
	// Prometheus stores this as a HISTOGRAM — it puts each observation
	// into pre-defined time buckets (5ms, 10ms, 25ms, …, 10s).
	//
	// Example query:  histogram_quantile(0.99, rate(http_request_duration_seconds_bucket[5m]))
	// → "what latency do 99% of requests complete within?"
	HttpRequestDuration = prometheus.NewHistogramVec(
		prometheus.HistogramOpts{
			Name: "http_request_duration_seconds",
			Help: "Time taken to process each HTTP request, in seconds.",
			// These buckets cover typical API response times:
			// 5ms → 10ms → 25ms → 50ms → 100ms → 250ms → 500ms → 1s → 2.5s → 5s → 10s
			Buckets: prometheus.DefBuckets,
		},
		[]string{"method", "path"},
	)

	// HttpRequestsInFlight tracks how many requests are currently
	// being handled. It goes UP when a request starts and DOWN when
	// it finishes. This tells you about SATURATION — if this number
	// keeps climbing, your service is getting overwhelmed.
	//
	// Example query:  http_requests_in_flight
	// → "how many requests are we handling right now?"
	HttpRequestsInFlight = prometheus.NewGauge(
		prometheus.GaugeOpts{
			Name: "http_requests_in_flight",
			Help: "Number of HTTP requests currently being processed.",
		},
	)
)

// ──────────────────────────────────────────────────────────────
// 2. REGISTER METRICS WITH PROMETHEUS
// ──────────────────────────────────────────────────────────────
//
// init() runs automatically when this package is imported.
// It tells the Prometheus client library "hey, these metrics exist".

func init() {
	prometheus.MustRegister(HttpRequestsTotal)
	prometheus.MustRegister(HttpRequestDuration)
	prometheus.MustRegister(HttpRequestsInFlight)
}

// ──────────────────────────────────────────────────────────────
// 3. GIN MIDDLEWARE — THE CORE OF THE INSTRUMENTATION
// ──────────────────────────────────────────────────────────────
//
// This is a Gin middleware function. Gin calls it for EVERY request
// before (and after) your actual handler runs. The flow is:
//
//   Request arrives
//     → PrometheusMiddleware runs (records start time, increments in-flight)
//       → Your handler runs (e.g. GetCourses)
//     → PrometheusMiddleware resumes (records duration, status code)
//   Response sent
//
// This is called the "decorator pattern" — we wrap your handler with
// measurement code without modifying the handler itself.

func PrometheusMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		// Skip the /metrics endpoint itself — we don't want to measure
		// Prometheus scraping us, only real API traffic.
		if c.Request.URL.Path == "/metrics" {
			c.Next()
			return
		}

		// ── BEFORE the request ──
		start := time.Now()        // Record when the request started
		HttpRequestsInFlight.Inc() // +1 request in flight

		// ── Let the actual handler run ──
		c.Next()

		// ── AFTER the request ──
		duration := time.Since(start).Seconds()   // How long did it take?
		status := strconv.Itoa(c.Writer.Status()) // What HTTP status was returned?
		path := c.FullPath()                      // The route pattern (e.g. "/api/course/:id")

		// If path is empty (404 — no matching route), use the raw URL.
		// We cap it to avoid high-cardinality label explosion.
		if path == "" {
			path = "unmatched"
		}

		// Record the measurements
		HttpRequestsTotal.WithLabelValues(c.Request.Method, path, status).Inc()
		HttpRequestDuration.WithLabelValues(c.Request.Method, path).Observe(duration)
		HttpRequestsInFlight.Dec() // -1 request in flight (it's done)
	}
}

// ──────────────────────────────────────────────────────────────
// 4. METRICS HANDLER — EXPOSE THE /metrics ENDPOINT
// ──────────────────────────────────────────────────────────────
//
// This wraps the standard Prometheus HTTP handler so Gin can serve it.
// When Prometheus scrapes http://course-service:8081/metrics, this
// handler returns ALL registered metrics in Prometheus text format.

func MetricsHandler() gin.HandlerFunc {
	h := promhttp.Handler()
	return func(c *gin.Context) {
		h.ServeHTTP(c.Writer, c.Request)
	}
}
