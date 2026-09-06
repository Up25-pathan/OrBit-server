package logger

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"runtime/debug"
	"strings"
	"time"
)

var (
	targetWebsiteURL   string
	controlServerSecret string
	httpClient         = &http.Client{Timeout: 5 * time.Second}
)

// Init sets up standard structured logging (log/slog) outputting only to os.Stdout
// (zero disk storage footprint on the Go server) and configures the Website Server
// async alert dispatcher.
func Init(websiteURL, serverSecret string) {
	targetWebsiteURL = strings.TrimRight(websiteURL, "/")
	controlServerSecret = serverSecret

	format := strings.ToLower(os.Getenv("LOG_FORMAT"))
	levelStr := strings.ToLower(os.Getenv("LOG_LEVEL"))

	var level slog.Level
	switch levelStr {
	case "debug":
		level = slog.LevelDebug
	case "warn":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}

	opts := &slog.HandlerOptions{
		Level: level,
	}

	var handler slog.Handler
	if format == "text" {
		handler = slog.NewTextHandler(os.Stdout, opts)
	} else {
		// Default to JSON structured output for cloud container log drains (Render, Railway, Docker)
		handler = slog.NewJSONHandler(os.Stdout, opts)
	}

	logger := slog.New(handler)
	slog.SetDefault(logger)

	slog.Info("Structured logger initialized",
		"format", format,
		"level", level.String(),
		"destination", "stdout",
		"forward_target", targetWebsiteURL,
	)
}

// ForwardServerError asynchronously sends critical server-side errors and panics to
// the Website Server so they immediately reflect on the Website Admin Panel without
// storing any crash files on the Go server disk.
func ForwardServerError(errType, message, stack string, metadata map[string]any) {
	if targetWebsiteURL == "" {
		return
	}

	// Dispatch in background goroutine to guarantee zero latency penalty on Go server requests
	go func() {
		defer func() {
			if r := recover(); r != nil {
				slog.Error("Failed to dispatch server error alert to Webserver", "panic", r)
			}
		}()

		payload := map[string]any{
			"source":    "control-server",
			"errorType": errType,
			"message":   message,
			"stack":     stack,
			"metadata":  metadata,
			"timestamp": time.Now().UTC().Format(time.RFC3339),
		}

		body, err := json.Marshal(payload)
		if err != nil {
			return
		}

		reqURL := fmt.Sprintf("%s/api/telemetry/server-error", targetWebsiteURL)
		req, err := http.NewRequest(http.MethodPost, reqURL, bytes.NewReader(body))
		if err != nil {
			return
		}

		req.Header.Set("Content-Type", "application/json")
		if controlServerSecret != "" {
			req.Header.Set("X-Control-Server-Secret", controlServerSecret)
		}

		resp, err := httpClient.Do(req)
		if err != nil {
			slog.Debug("Website server telemetry webhook unreachable", "endpoint", reqURL, "err", err.Error())
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			slog.Debug("Server error forwarded to Website Admin Panel", "status", resp.StatusCode)
		}
	}()
}

// responseWriterInterceptor wraps http.ResponseWriter to capture status and bytes written
type responseWriterInterceptor struct {
	http.ResponseWriter
	statusCode int
	bytes      int
}

func (w *responseWriterInterceptor) WriteHeader(code int) {
	w.statusCode = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *responseWriterInterceptor) Write(b []byte) (int, error) {
	if w.statusCode == 0 {
		w.statusCode = http.StatusOK
	}
	n, err := w.ResponseWriter.Write(b)
	w.bytes += n
	return n, err
}

// HTTPLogger provides structured HTTP access logging and automatic 5xx error alerting
func HTTPLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path

		// Skip health check endpoints to keep stdout completely quiet from automated keepalive pings
		if path == "/health" || path == "/api/v1/health" || path == "/" {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		rw := &responseWriterInterceptor{ResponseWriter: w, statusCode: http.StatusOK}

		next.ServeHTTP(rw, r)

		duration := time.Since(start)
		durationMs := float64(duration.Microseconds()) / 1000.0

		// Extract client IP
		ip := r.Header.Get("X-Forwarded-For")
		if ip == "" {
			ip = r.Header.Get("X-Real-IP")
		}
		if ip == "" {
			ip = r.RemoteAddr
		}
		if idx := strings.LastIndex(ip, ":"); idx != -1 {
			ip = ip[:idx]
		}

		fields := []any{
			"method", r.Method,
			"path", path,
			"status", rw.statusCode,
			"duration_ms", durationMs,
			"bytes", rw.bytes,
			"ip", ip,
		}

		if rw.statusCode >= 500 {
			slog.Error("HTTP Server Error", fields...)
			ForwardServerError("HTTP_5XX", fmt.Sprintf("%s %s responded with status %d", r.Method, path, rw.statusCode), "", map[string]any{
				"method":     r.Method,
				"path":       path,
				"status":     rw.statusCode,
				"durationMs": durationMs,
				"ip":         ip,
			})
		} else if rw.statusCode >= 400 {
			slog.Warn("HTTP Client Error", fields...)
		} else {
			slog.Info("HTTP Request", fields...)
		}
	})
}

// Recoverer intercepts unhandled panics inside HTTP handlers, logs the stack trace
// via slog, forwards the event to the Website Server, and returns a clean 500 JSON response.
func Recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rvr := recover(); rvr != nil {
				stack := string(debug.Stack())
				errMessage := fmt.Sprintf("%v", rvr)

				slog.Error("UNHANDLED PANIC in HTTP Handler",
					"error", errMessage,
					"method", r.Method,
					"path", r.URL.Path,
					"stack", stack,
				)

				ForwardServerError("PANIC", errMessage, stack, map[string]any{
					"method": r.Method,
					"path":   r.URL.Path,
				})

				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusInternalServerError)
				w.Write([]byte(`{"error":"internal server error"}`))
			}
		}()

		next.ServeHTTP(w, r)
	})
}
