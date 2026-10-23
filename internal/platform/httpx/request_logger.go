package httpx

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	"go.opentelemetry.io/otel/trace"
)

// RequestLogger is a Gin middleware that emits a structured JSON log line for
// every HTTP request/response cycle. It is the structured replacement for
// Gin's built-in text-based Logger middleware.
//
// For each request it:
//  1. Generates a unique request_id (UUID v4) or reads an incoming X-Request-ID
//     header for distributed-tracing continuity.
//  2. Stores the request_id in the Gin context so handlers can include it in
//     their own log lines (c.GetString("request_id")).
//  3. Emits a single log line after the response is written, containing:
//     method, path, status, latency_ms, client_ip, user_agent, and request_id.
//  4. Chooses the log level based on the response status:
//     - 5xx → ERROR
//     - 4xx → WARN
//     - everything else → INFO
//  5. Skips /metrics and /health to avoid noisy, low-value log lines.
func RequestLogger() gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Request.URL.Path

		// Skip noisy infrastructure endpoints.
		if path == "/metrics" || path == "/api/health" || path == "/health" {
			c.Next()
			return
		}

		// Generate or propagate a correlation ID.
		requestID := c.GetHeader("X-Request-ID")
		if requestID == "" {
			requestID = uuid.New().String()
		}
		c.Set("request_id", requestID)

		// Set the response header so the caller can correlate too.
		c.Header("X-Request-ID", requestID)

		start := time.Now()

		// Let the actual handler run.
		c.Next()

		// Measure.
		latency := time.Since(start)
		status := c.Writer.Status()
		method := c.Request.Method

		// Extract trace context from the active span (set by otelgin).
		span := trace.SpanFromContext(c.Request.Context())
		traceID := span.SpanContext().TraceID().String()
		spanID := span.SpanContext().SpanID().String()

		// Use the route pattern (e.g. "/api/course-materials/:id") to avoid
		// high-cardinality label explosion from path parameters.
		routePath := c.FullPath()
		if routePath == "" {
			routePath = path // fallback for unmatched routes
		}

		attrs := []any{
			"request_id", requestID,
			"trace_id", traceID,
			"span_id", spanID,
			"method", method,
			"path", routePath,
			"status", status,
			"latency_ms", float64(latency.Microseconds()) / 1000.0,
			"client_ip", c.ClientIP(),
			"user_agent", c.Request.UserAgent(),
		}

		switch {
		case status >= 500:
			slog.Error("request completed", attrs...)
		case status >= 400:
			slog.Warn("request completed", attrs...)
		default:
			slog.Info("request completed", attrs...)
		}
	}
}
