package wgproxy

import (
	"bufio"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

const testTimeout = 5 * time.Second

func newTestProxy(t *testing.T, pac string) *Proxy {
	t.Helper()
	return newProxy(slog.New(slog.DiscardHandler), &net.Dialer{}, pac)
}

// startServer starts a TCP listener that runs handle on each accepted connection.
func startServer(t *testing.T, handle func(conn *net.TCPConn)) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { listener.Close() })

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				defer conn.Close()
				conn.SetDeadline(time.Now().Add(testTimeout))
				handle(conn.(*net.TCPConn))
			}()
		}
	}()

	return listener.Addr().String()
}

// startEchoServer echoes data until EOF, then closes its write side.
func startEchoServer(t *testing.T) string {
	t.Helper()
	return startServer(t, func(conn *net.TCPConn) {
		io.Copy(conn, conn)
		conn.CloseWrite()
	})
}

// startReplyAfterEOFServer reads until EOF and only then writes reply, so the
// reply always arrives after the client has half-closed its side.
func startReplyAfterEOFServer(t *testing.T, reply []byte) string {
	t.Helper()
	return startServer(t, func(conn *net.TCPConn) {
		if _, err := io.Copy(io.Discard, conn); err != nil {
			return
		}
		conn.Write(reply)
	})
}

type failingDialer struct{}

func (failingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	return nil, errors.New("dial failed")
}

// plainConnDialer returns conns without CloseRead/CloseWrite.
type plainConnDialer struct {
	t *testing.T
}

func (d plainConnDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	local, remote := net.Pipe()
	d.t.Cleanup(func() { remote.Close() })
	return local, nil
}

// closeRecordingDialer dials over TCP and records whether the conn was closed.
type closeRecordingDialer struct {
	closed atomic.Bool
}

type closeRecordingConn struct {
	*net.TCPConn
	closed *atomic.Bool
}

func (c *closeRecordingConn) Close() error {
	c.closed.Store(true)
	return c.TCPConn.Close()
}

func (d *closeRecordingDialer) DialContext(ctx context.Context, network, address string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, network, address)
	if err != nil {
		return nil, err
	}
	return &closeRecordingConn{TCPConn: conn.(*net.TCPConn), closed: &d.closed}, nil
}

func startProxyServer(t *testing.T, p *Proxy) string {
	t.Helper()
	server := httptest.NewServer(p)
	t.Cleanup(server.Close)
	return server.Listener.Addr().String()
}

// connect sends a CONNECT request for target to the proxy at proxyAddr,
// followed by payload in the same write, and returns the response and a
// reader positioned after it.
func connect(t *testing.T, proxyAddr, target string, payload []byte) (*net.TCPConn, *http.Response, *bufio.Reader) {
	t.Helper()
	conn, err := net.Dial("tcp", proxyAddr)
	if err != nil {
		t.Fatalf("dial proxy: %v", err)
	}
	t.Cleanup(func() { conn.Close() })
	conn.SetDeadline(time.Now().Add(testTimeout))

	request := "CONNECT " + target + " HTTP/1.1\r\nHost: " + target + "\r\n\r\n"
	if _, err := conn.Write(append([]byte(request), payload...)); err != nil {
		t.Fatalf("write CONNECT: %v", err)
	}

	reader := bufio.NewReader(conn)
	response, err := http.ReadResponse(reader, &http.Request{Method: http.MethodConnect})
	if err != nil {
		t.Fatalf("read CONNECT response: %v", err)
	}
	return conn.(*net.TCPConn), response, reader
}

func readN(t *testing.T, r io.Reader, n int) string {
	t.Helper()
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		t.Fatalf("read %d bytes: %v", n, err)
	}
	return string(buf)
}

func TestServeHTTPNotAllowed(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
		pac    string
	}{
		{name: "relative GET to other path", method: http.MethodGet, target: "/other", pac: "proxy.pac"},
		{name: "POST proxy.pac", method: http.MethodPost, target: "/proxy.pac", pac: "proxy.pac"},
		{name: "GET proxy.pac without pac file", method: http.MethodGet, target: "/proxy.pac"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := httptest.NewRecorder()
			newTestProxy(t, tt.pac).ServeHTTP(recorder, httptest.NewRequest(tt.method, tt.target, nil))
			if recorder.Code != http.StatusMethodNotAllowed {
				t.Errorf("status = %d, want %d", recorder.Code, http.StatusMethodNotAllowed)
			}
		})
	}
}

func TestServeHTTPProxyPac(t *testing.T) {
	const content = "function FindProxyForURL(url, host) { return \"DIRECT\"; }"
	pac := filepath.Join(t.TempDir(), "proxy.pac")
	if err := os.WriteFile(pac, []byte(content), 0o600); err != nil {
		t.Fatalf("write pac file: %v", err)
	}

	recorder := httptest.NewRecorder()
	newTestProxy(t, pac).ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/proxy.pac", nil))

	if recorder.Code != http.StatusOK {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusOK)
	}
	if body := recorder.Body.String(); body != content {
		t.Errorf("body = %q, want %q", body, content)
	}
}

func TestForwarding(t *testing.T) {
	received := make(chan http.Header, 1)
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		received <- r.Header.Clone()
		w.Header().Add("X-Multi", "one")
		w.Header().Add("X-Multi", "two")
		w.WriteHeader(http.StatusTeapot)
		io.WriteString(w, "backend body")
	}))
	t.Cleanup(backend.Close)

	proxyURL := &url.URL{Scheme: "http", Host: startProxyServer(t, newTestProxy(t, ""))}
	client := &http.Client{
		Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		Timeout:   testTimeout,
	}

	request, err := http.NewRequest(http.MethodGet, backend.URL+"/path", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	request.Header.Set("X-Custom", "kept")
	request.Header.Set("Proxy-Authorization", "Basic secret")
	request.Header.Set("Keep-Alive", "timeout=5")
	request.Header.Set("Te", "trailers")
	request.Header.Set("Proxy-Connection", "keep-alive")

	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("request through proxy: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}

	if response.StatusCode != http.StatusTeapot {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusTeapot)
	}
	if string(body) != "backend body" {
		t.Errorf("body = %q, want %q", body, "backend body")
	}
	if got := response.Header.Values("X-Multi"); len(got) != 2 || got[0] != "one" || got[1] != "two" {
		t.Errorf("X-Multi = %q, want [one two]", got)
	}

	header := <-received
	if got := header.Get("X-Custom"); got != "kept" {
		t.Errorf("backend X-Custom = %q, want %q", got, "kept")
	}
	for _, key := range []string{"Proxy-Authorization", "Keep-Alive", "Te", "Proxy-Connection"} {
		if got, ok := header[key]; ok {
			t.Errorf("backend received hop header %s = %q", key, got)
		}
	}
}

func TestForwardingError(t *testing.T) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	closedAddr := listener.Addr().String()
	listener.Close()

	recorder := httptest.NewRecorder()
	newTestProxy(t, "").ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "http://"+closedAddr+"/", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestConnect(t *testing.T) {
	proxyAddr := startProxyServer(t, newTestProxy(t, ""))

	conn, response, reader := connect(t, proxyAddr, startEchoServer(t), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	if _, err := conn.Write([]byte("hello")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got := readN(t, reader, 5); got != "hello" {
		t.Errorf("echo = %q, want %q", got, "hello")
	}
}

// Regression test for data sent together with the CONNECT request (1e5cf9d).
func TestConnectBufferedData(t *testing.T) {
	proxyAddr := startProxyServer(t, newTestProxy(t, ""))

	_, response, reader := connect(t, proxyAddr, startEchoServer(t), []byte("early"))
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	if got := readN(t, reader, 5); got != "early" {
		t.Errorf("echo = %q, want %q", got, "early")
	}
}

func TestConnectHalfClose(t *testing.T) {
	proxyAddr := startProxyServer(t, newTestProxy(t, ""))

	conn, response, reader := connect(t, proxyAddr, startReplyAfterEOFServer(t, []byte("pong")), nil)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusOK)
	}

	if _, err := conn.Write([]byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := conn.CloseWrite(); err != nil {
		t.Fatalf("close write: %v", err)
	}

	reply, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read reply: %v", err)
	}
	if string(reply) != "pong" {
		t.Errorf("reply = %q, want %q", reply, "pong")
	}
}

func TestConnectDialFailure(t *testing.T) {
	p := newProxy(slog.New(slog.DiscardHandler), failingDialer{}, "")

	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, httptest.NewRequest(http.MethodConnect, "example.com:443", nil))

	if recorder.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusServiceUnavailable)
	}
}

func TestConnectNotHijackable(t *testing.T) {
	dialer := &closeRecordingDialer{}
	p := newProxy(slog.New(slog.DiscardHandler), dialer, "")

	recorder := httptest.NewRecorder()
	p.ServeHTTP(recorder, httptest.NewRequest(http.MethodConnect, startEchoServer(t), nil))

	if recorder.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", recorder.Code, http.StatusInternalServerError)
	}
	if !dialer.closed.Load() {
		t.Error("destination connection was not closed")
	}
}

func TestConnectDestinationWithoutHalfClose(t *testing.T) {
	p := newProxy(slog.New(slog.DiscardHandler), plainConnDialer{t: t}, "")
	proxyAddr := startProxyServer(t, p)

	_, response, _ := connect(t, proxyAddr, "example.com:443", nil)
	if response.StatusCode != http.StatusInternalServerError {
		t.Errorf("status = %d, want %d", response.StatusCode, http.StatusInternalServerError)
	}
}

func TestRemoveHopHeaders(t *testing.T) {
	header := http.Header{}
	hopHeaders := []string{
		"Connection", "Keep-Alive", "Proxy-Authenticate", "Proxy-Authorization",
		"Proxy-Connection", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	}
	for _, key := range hopHeaders {
		header.Set(key, "value")
	}
	header.Set("Content-Type", "text/plain")
	header.Set("X-Custom", "kept")

	newTestProxy(t, "").removeHopHeaders(header)

	for _, key := range hopHeaders {
		if _, ok := header[key]; ok {
			t.Errorf("hop header %s was not removed", key)
		}
	}
	for _, key := range []string{"Content-Type", "X-Custom"} {
		if _, ok := header[key]; !ok {
			t.Errorf("header %s was removed", key)
		}
	}
}

func TestCopyHeader(t *testing.T) {
	tests := []struct {
		name string
		dst  http.Header
		src  http.Header
		want http.Header
	}{
		{
			name: "into empty",
			dst:  http.Header{},
			src:  http.Header{"A": {"1"}, "B": {"2", "3"}},
			want: http.Header{"A": {"1"}, "B": {"2", "3"}},
		},
		{
			name: "appends to existing",
			dst:  http.Header{"A": {"0"}, "C": {"x"}},
			src:  http.Header{"A": {"1", "2"}},
			want: http.Header{"A": {"0", "1", "2"}, "C": {"x"}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			newTestProxy(t, "").copyHeader(tt.dst, tt.src)
			if len(tt.dst) != len(tt.want) {
				t.Fatalf("header = %v, want %v", tt.dst, tt.want)
			}
			for key, want := range tt.want {
				got := tt.dst[key]
				if strings.Join(got, ",") != strings.Join(want, ",") {
					t.Errorf("%s = %q, want %q", key, got, want)
				}
			}
		})
	}
}

// tcpPair returns both ends of a loopback TCP connection.
func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer listener.Close()

	accepted := make(chan net.Conn, 1)
	go func() {
		conn, _ := listener.Accept()
		accepted <- conn
	}()

	dialed, err := net.Dial("tcp", listener.Addr().String())
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	server := <-accepted
	if server == nil {
		t.Fatal("accept failed")
	}

	for _, conn := range []net.Conn{dialed, server} {
		t.Cleanup(func() { conn.Close() })
		conn.SetDeadline(time.Now().Add(testTimeout))
	}
	return dialed.(*net.TCPConn), server.(*net.TCPConn)
}

type copyResult struct {
	errClientToDest, errDestToClient error
}

// startCopy runs copy between two loopback pairs and returns the outer ends:
// client is the client's side of the tunnel and dest is the destination's.
func startCopy(t *testing.T) (client, dest, clientInner, destInner *net.TCPConn, done <-chan copyResult) {
	t.Helper()
	client, clientInner = tcpPair(t)
	dest, destInner = tcpPair(t)

	result := make(chan copyResult, 1)
	go func() {
		var r copyResult
		r.errClientToDest, r.errDestToClient = newTestProxy(t, "").copy(destInner, clientInner)
		result <- r
	}()
	return client, dest, clientInner, destInner, result
}

func waitCopy(t *testing.T, done <-chan copyResult) copyResult {
	t.Helper()
	select {
	case r := <-done:
		return r
	case <-time.After(testTimeout):
		t.Fatal("copy did not return")
		return copyResult{}
	}
}

func assertClosed(t *testing.T, name string, conn net.Conn) {
	t.Helper()
	if _, err := conn.Write([]byte("x")); !errors.Is(err, net.ErrClosed) {
		t.Errorf("%s: write after copy error = %v, want net.ErrClosed", name, err)
	}
}

func TestCopy(t *testing.T) {
	client, dest, clientInner, destInner, done := startCopy(t)

	if _, err := client.Write([]byte("to-dest")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	if got := readN(t, dest, 7); got != "to-dest" {
		t.Errorf("dest read = %q, want %q", got, "to-dest")
	}
	if _, err := dest.Write([]byte("to-client")); err != nil {
		t.Fatalf("dest write: %v", err)
	}
	if got := readN(t, client, 9); got != "to-client" {
		t.Errorf("client read = %q, want %q", got, "to-client")
	}

	client.CloseWrite()
	dest.CloseWrite()

	r := waitCopy(t, done)
	if r.errClientToDest != nil || r.errDestToClient != nil {
		t.Errorf("copy errors = %v, %v, want nil", r.errClientToDest, r.errDestToClient)
	}
	assertClosed(t, "client", clientInner)
	assertClosed(t, "dest", destInner)
}

func TestCopyHalfClose(t *testing.T) {
	client, dest, _, _, done := startCopy(t)

	if _, err := client.Write([]byte("request")); err != nil {
		t.Fatalf("client write: %v", err)
	}
	client.CloseWrite()

	request, err := io.ReadAll(dest)
	if err != nil {
		t.Fatalf("dest read: %v", err)
	}
	if string(request) != "request" {
		t.Errorf("dest read = %q, want %q", request, "request")
	}

	if _, err := dest.Write([]byte("reply")); err != nil {
		t.Fatalf("dest write: %v", err)
	}
	dest.CloseWrite()

	reply, err := io.ReadAll(client)
	if err != nil {
		t.Fatalf("client read: %v", err)
	}
	if string(reply) != "reply" {
		t.Errorf("client read = %q, want %q", reply, "reply")
	}

	waitCopy(t, done)
}

func TestCopyAbortsOnError(t *testing.T) {
	client, _, _, _, done := startCopy(t)

	// closing with zero linger resets the connection instead of a clean EOF
	client.SetLinger(0)
	client.Close()

	// the destination stays open and silent, so only the reset ends the tunnel
	r := waitCopy(t, done)
	if r.errClientToDest == nil {
		t.Error("errClientToDest = nil, want the reset error")
	}
}

func TestNewProxyFromFileMissingConfiguration(t *testing.T) {
	_, err := NewProxyFromFile(slog.New(slog.DiscardHandler), filepath.Join(t.TempDir(), "missing.conf"), "")
	if err == nil {
		t.Fatal("error = nil, want an error")
	}
	if !strings.HasPrefix(err.Error(), "error creating wireguard dialer: ") {
		t.Errorf("error = %q, want it wrapped with the dialer context", err)
	}
	if errors.Unwrap(err) == nil {
		t.Errorf("error %q does not wrap the underlying error", err)
	}
}
