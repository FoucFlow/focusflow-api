// Package logger provides structured JSON logging for the Course Material Service
// using Go's built-in log/slog package.
//
// Call Init() once at application startup (before any logging) to configure the
// global logger with the desired level and JSON output to stdout. All subsequent
// slog.Info / slog.Error / slog.Warn / slog.Debug calls throughout the codebase
// will automatically use the configured logger.
//
// This replaces Go's default stdlib "log" output — any lingering log.Println
// calls are redirected through slog as well.
package logger

import (
	"log"
	"log/slog"
	"os"
	"strings"
)

// Init configures the global slog logger with JSON output to stdout.
//
// The level parameter supports: "debug", "info", "warn", "error".
// An unrecognised value defaults to "info".
//
// Every log line emitted by this logger includes:
//   - "service": "focusflow-api"
//
// Init also redirects Go's stdlib log package through slog, so any
// remaining log.Printf / log.Println calls in third-party code (or
// transitional code that hasn't migrated yet) emit structured JSON too.
func Init(level string) {
	var slogLevel slog.Level
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		slogLevel = slog.LevelDebug
	case "warn", "warning":
		slogLevel = slog.LevelWarn
	case "error":
		slogLevel = slog.LevelError
	default:
		slogLevel = slog.LevelInfo
	}

	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slogLevel,
	})

	// Wrap with a default "service" attribute so every line identifies itself.
	logger := slog.New(handler).With("service", "focusflow-api")
	slog.SetDefault(logger)

	// Redirect stdlib log.* calls through slog. This catches any remaining
	// log.Printf / log.Println from third-party packages or transitional code.
	log.SetFlags(0) // Remove timestamps — slog adds its own.
	log.SetOutput(&slogWriter{})
}

// slogWriter is an io.Writer that forwards stdlib log output to slog.Info.
// This ensures that any code still using log.Printf emits structured JSON.
type slogWriter struct{}

func (w *slogWriter) Write(p []byte) (n int, err error) {
	msg := strings.TrimRight(string(p), "\n")
	slog.Info(msg, "source", "stdlib")
	return len(p), nil
}
