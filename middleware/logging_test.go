package middleware

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func decodeLogLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var entries []map[string]any
	scanner := bufio.NewScanner(buf)
	for scanner.Scan() {
		var entry map[string]any
		if err := json.Unmarshal(scanner.Bytes(), &entry); err != nil {
			t.Fatalf("decoding log line %q: %v", scanner.Text(), err)
		}
		entries = append(entries, entry)
	}
	return entries
}

func serveLogged(t *testing.T, handler http.HandlerFunc) []map[string]any {
	t.Helper()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	r := httptest.NewRequest(http.MethodGet, "/path", nil)
	NewLoggingMiddleware(handler, logger).ServeHTTP(httptest.NewRecorder(), r)

	entries := decodeLogLines(t, &buf)
	if len(entries) != 2 {
		t.Fatalf("got %d log lines, want 2", len(entries))
	}
	return entries
}

func TestLoggingMiddlewareLogsRequest(t *testing.T) {
	entries := serveLogged(t, func(w http.ResponseWriter, r *http.Request) {})

	started, finished := entries[0], entries[1]
	if started["msg"] != "request started" {
		t.Errorf("first message = %v, want %q", started["msg"], "request started")
	}
	if finished["msg"] != "request finished" {
		t.Errorf("second message = %v, want %q", finished["msg"], "request finished")
	}
	for _, entry := range entries {
		for key, want := range map[string]any{"method": "GET", "uri": "/path", "remote": "192.0.2.1:1234"} {
			if entry[key] != want {
				t.Errorf("%s: %s = %v, want %v", entry["msg"], key, entry[key], want)
			}
		}
	}
	if _, ok := finished["duration"]; !ok {
		t.Error("request finished: duration is missing")
	}
}

func TestLoggingMiddlewareStatus(t *testing.T) {
	tests := []struct {
		name      string
		handler   http.HandlerFunc
		wantCode  float64
		wantLevel string
	}{
		{name: "default 200", handler: func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, "ok") }, wantCode: 200, wantLevel: "INFO"},
		{name: "explicit 404", handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNotFound) }, wantCode: 404, wantLevel: "INFO"},
		{name: "500 is error", handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusInternalServerError) }, wantCode: 500, wantLevel: "ERROR"},
		{name: "503 is error", handler: func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) }, wantCode: 503, wantLevel: "ERROR"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			finished := serveLogged(t, tt.handler)[1]
			if finished["status"] != tt.wantCode {
				t.Errorf("status = %v, want %v", finished["status"], tt.wantCode)
			}
			if finished["level"] != tt.wantLevel {
				t.Errorf("level = %v, want %v", finished["level"], tt.wantLevel)
			}
		})
	}
}

func TestLoggingMiddlewareHijackPassthrough(t *testing.T) {
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := w.(http.Hijacker).Hijack()
		if err != nil {
			t.Errorf("Hijack() error = %v", err)
			return
		}
		defer conn.Close()
		rw.WriteString("HTTP/1.1 200 OK\r\nContent-Length: 8\r\nConnection: close\r\n\r\nhijacked")
		rw.Flush()
	})
	server := httptest.NewServer(NewLoggingMiddleware(inner, slog.New(slog.DiscardHandler)))
	t.Cleanup(server.Close)

	client := &http.Client{Timeout: 5 * time.Second}
	response, err := client.Get(server.URL)
	if err != nil {
		t.Fatalf("GET error = %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if string(body) != "hijacked" {
		t.Errorf("body = %q, want %q", body, "hijacked")
	}
}

func TestLoggingMiddlewareHijackNotSupported(t *testing.T) {
	var hijackErr error
	inner := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _, hijackErr = w.(http.Hijacker).Hijack()
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	NewLoggingMiddleware(inner, slog.New(slog.DiscardHandler)).ServeHTTP(httptest.NewRecorder(), r)

	if hijackErr == nil {
		t.Error("Hijack() error = nil, want an error for a writer without Hijacker")
	}
}
