package wgproxy

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"

	"github.com/botanica-consulting/wiredialer"
)

type Proxy struct {
	logger    *slog.Logger
	dialer    dialer
	transport *http.Transport
	proxyPac  string
}

type dialer interface {
	DialContext(ctx context.Context, network, address string) (net.Conn, error)
}

type proxyConn interface {
	net.Conn
	CloseRead() error
	CloseWrite() error
}

type bufferedConn struct {
	proxyConn
	reader *bufio.Reader
}

func (c *bufferedConn) Read(b []byte) (int, error) {
	return c.reader.Read(b)
}

func NewProxyFromFile(logger *slog.Logger, configuration string, proxyPac string) (*Proxy, error) {
	dialer, err := wiredialer.NewDialerFromFile(configuration)
	if err != nil {
		return nil, fmt.Errorf("error creating wireguard dialer: %w", err)
	}

	return newProxy(logger, dialer, proxyPac), nil
}

func newProxy(logger *slog.Logger, d dialer, proxyPac string) *Proxy {
	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           d.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &Proxy{logger: logger, dialer: d, transport: transport, proxyPac: proxyPac}
}

func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodConnect {
		p.handleConnect(w, r)
	} else if r.URL.IsAbs() {
		p.handleRequest(w, r)
	} else if r.Method == http.MethodGet && r.URL.Path == "/proxy.pac" && p.proxyPac != "" {
		http.ServeFile(w, r, p.proxyPac)
	} else {
		p.handleNotAllowed(w, r)
	}
}

func (p *Proxy) handleConnect(w http.ResponseWriter, r *http.Request) {
	conn, err := p.dialer.DialContext(r.Context(), "tcp", r.Host)
	if err != nil {
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"error connecting host",
			slog.String("error", err.Error()),
			slog.String("host", r.Host),
		)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}

	dest, ok := conn.(proxyConn)
	if !ok {
		conn.Close()
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"destination connection does not support half-close",
			slog.String("host", r.Host),
		)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		dest.Close()
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"error getting hijack interface",
		)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	hijacked, clientBuffer, err := hijacker.Hijack()
	if err != nil {
		dest.Close()
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"error hijacking client connection",
			slog.String("error", err.Error()),
		)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	client, ok := hijacked.(proxyConn)
	if !ok {
		// the connection is hijacked, so no HTTP response can be written anymore
		dest.Close()
		hijacked.Close()
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"client connection does not support half-close",
		)
		return
	}

	// should be w.WriteHeader(http.StatusOK), but the connection is hijacked
	client.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))

	errClientToDest, errDestToClient := p.copy(
		dest,
		&bufferedConn{proxyConn: client, reader: clientBuffer.Reader},
	)
	if errClientToDest != nil {
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"error copying client data",
			slog.String("error", errClientToDest.Error()),
			slog.String("uri", r.RequestURI),
		)
	}
	if errDestToClient != nil {
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"error copying destination data",
			slog.String("error", errDestToClient.Error()),
			slog.String("uri", r.RequestURI),
		)
	}
}

func (p *Proxy) handleRequest(w http.ResponseWriter, r *http.Request) {
	p.removeHopHeaders(r.Header)

	response, err := p.transport.RoundTrip(r)
	if err != nil {
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"error sending request",
			slog.String("error", err.Error()),
			slog.String("uri", r.RequestURI),
		)
		http.Error(w, http.StatusText(http.StatusServiceUnavailable), http.StatusServiceUnavailable)
		return
	}
	defer response.Body.Close()

	p.copyHeader(w.Header(), response.Header)
	w.WriteHeader(response.StatusCode)

	_, err = io.Copy(w, response.Body)
	if err != nil {
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"error copying request/response data",
			slog.String("error", err.Error()),
			slog.String("uri", r.RequestURI),
		)
	}
}

func (p *Proxy) handleNotAllowed(w http.ResponseWriter, r *http.Request) {
	p.logger.LogAttrs(
		r.Context(),
		slog.LevelError,
		"invalid method",
		slog.String("method", r.Method),
		slog.String("uri", r.RequestURI),
	)
	http.Error(w, http.StatusText(http.StatusMethodNotAllowed), http.StatusMethodNotAllowed)
}

func (p *Proxy) copyHeader(dst http.Header, src http.Header) {
	for key, values := range src {
		for _, value := range values {
			dst.Add(key, value)
		}
	}
}

// Hop-by-hop headers. These are removed when sent to the backend.
// http://www.w3.org/Protocols/rfc2616/rfc2616-sec13.html
func (p *Proxy) removeHopHeaders(header http.Header) {
	hopHeaders := []string{
		"Connection",
		"Keep-Alive",
		"Proxy-Authenticate",
		"Proxy-Authorization",
		"Proxy-Connection",
		"Te",
		"Trailer",
		"Transfer-Encoding",
		"Upgrade",
	}
	for _, key := range hopHeaders {
		header.Del(key)
	}
}

func (p *Proxy) copy(dest, client proxyConn) (errClientToDest, errDestToClient error) {
	pipe := func(dst, src proxyConn) error {
		_, err := io.Copy(dst, src)
		// Signal EOF to the peer but keep reading from it: it may still reply
		// after seeing EOF. The opposite copy ends when the peer closes its
		// write side.
		dst.CloseWrite()
		if err != nil {
			dst.CloseRead() // the tunnel is broken, unblock the opposite copy
		}
		return err
	}

	var wg sync.WaitGroup
	wg.Go(func() { errClientToDest = pipe(dest, client) })
	wg.Go(func() { errDestToClient = pipe(client, dest) })
	wg.Wait()

	dest.Close()
	client.Close()

	return
}
