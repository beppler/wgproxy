package middleware

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/oklog/ulid/v2"
)

func TestRequestIdContextRoundTrip(t *testing.T) {
	ctx := SetRequestId(context.Background(), "abc")
	if got := GetRequestID(ctx); got != "abc" {
		t.Errorf("GetRequestID() = %q, want %q", got, "abc")
	}
}

func TestGetRequestIDEmptyContext(t *testing.T) {
	if got := GetRequestID(context.Background()); got != "" {
		t.Errorf("GetRequestID() = %q, want empty", got)
	}
}

func TestGetRequestIDNonStringValue(t *testing.T) {
	ctx := context.WithValue(context.Background(), requestIdKey, 42)
	if got := GetRequestID(ctx); got != "" {
		t.Errorf("GetRequestID() = %q, want empty", got)
	}
}

func TestRequestIdMiddleware(t *testing.T) {
	tests := []struct {
		name        string
		allowRemote bool
		header      string
		want        string // empty means a generated ULID is expected
	}{
		{name: "no header generates", allowRemote: false},
		{name: "remote allowed uses header", allowRemote: true, header: "remote-id", want: "remote-id"},
		{name: "remote not allowed ignores header", allowRemote: false, header: "remote-id"},
		{name: "remote allowed with empty header generates", allowRemote: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				got = GetRequestID(r.Context())
			})

			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != "" {
				r.Header.Set("X-Request-ID", tt.header)
			}
			NewRequestIdMiddleware(capture, tt.allowRemote).ServeHTTP(httptest.NewRecorder(), r)

			if tt.want != "" {
				if got != tt.want {
					t.Errorf("request id = %q, want %q", got, tt.want)
				}
				return
			}
			if _, err := ulid.Parse(got); err != nil {
				t.Errorf("request id %q is not a valid ULID: %v", got, err)
			}
		})
	}
}

func decodeLogLine(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("decoding log line %q: %v", buf.String(), err)
	}
	return entry
}

func TestRequestIdHandler(t *testing.T) {
	tests := []struct {
		name      string
		requestId string
	}{
		{name: "with id", requestId: "abc"},
		{name: "without id"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := slog.New(NewRequestIdHandler(slog.NewJSONHandler(&buf, nil)))

			ctx := context.Background()
			if tt.requestId != "" {
				ctx = SetRequestId(ctx, tt.requestId)
			}
			logger.InfoContext(ctx, "message")

			entry := decodeLogLine(t, &buf)
			got, present := entry["request-id"]
			if tt.requestId == "" {
				if present {
					t.Errorf("request-id = %v, want absent", got)
				}
				return
			}
			if got != tt.requestId {
				t.Errorf("request-id = %v, want %q", got, tt.requestId)
			}
		})
	}
}

func TestRequestIdHandlerWithAttrsAndGroup(t *testing.T) {
	tests := []struct {
		name string
		wrap func(slog.Handler) slog.Handler
	}{
		{name: "WithAttrs", wrap: func(h slog.Handler) slog.Handler { return h.WithAttrs([]slog.Attr{slog.String("k", "v")}) }},
		{name: "WithGroup", wrap: func(h slog.Handler) slog.Handler { return h.WithGroup("g") }},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var buf bytes.Buffer
			handler := tt.wrap(NewRequestIdHandler(slog.NewJSONHandler(&buf, nil)))
			if _, ok := handler.(*RequestIdHandler); !ok {
				t.Fatalf("handler type = %T, want *RequestIdHandler", handler)
			}

			slog.New(handler).InfoContext(SetRequestId(context.Background(), "abc"), "message")

			if !bytes.Contains(buf.Bytes(), []byte(`"request-id":"abc"`)) {
				t.Errorf("log line %q does not contain the request id", buf.String())
			}
		})
	}
}
