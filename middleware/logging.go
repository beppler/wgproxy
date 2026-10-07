package middleware

import (
	"bufio"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"time"
)

type LoggingMiddleware struct {
	next   http.Handler
	logger *slog.Logger
}

func NewLoggingMiddleware(next http.Handler, logger *slog.Logger) *LoggingMiddleware {
	return &LoggingMiddleware{next: next, logger: logger}
}

func (m *LoggingMiddleware) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	start := time.Now()

	m.logger.LogAttrs(
		r.Context(),
		slog.LevelInfo,
		"request started",
		slog.String("method", r.Method),
		slog.String("uri", r.RequestURI),
		slog.String("remote", r.RemoteAddr),
	)

	writer := &logResponseWriter{w, http.StatusOK}
	m.next.ServeHTTP(writer, r)

	duration := time.Since(start)

	level := slog.LevelInfo
	if writer.statusCode >= http.StatusInternalServerError {
		level = slog.LevelError
	}

	m.logger.LogAttrs(
		r.Context(),
		level,
		"request finished",
		slog.String("method", r.Method),
		slog.String("uri", r.RequestURI),
		slog.String("remote", r.RemoteAddr),
		slog.Int("status", writer.statusCode),
		slog.Duration("duration", duration),
	)
}

type logResponseWriter struct {
	http.ResponseWriter
	statusCode int
}

func (rw *logResponseWriter) WriteHeader(statusCode int) {
	rw.statusCode = statusCode
	rw.ResponseWriter.WriteHeader(statusCode)
}

func (rw *logResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	hijacker, ok := rw.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, errors.New("response writer does not support hijacking")
	}
	return hijacker.Hijack()
}
