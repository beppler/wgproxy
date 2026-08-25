package wgproxy

import (
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
	dialer    *wiredialer.WireDialer
	transport *http.Transport
	proxyPac  string
}

type ProxyConn interface {
	net.Conn
	CloseRead() error
	CloseWrite() error
}

func NewProxyFromFile(logger *slog.Logger, configuration string, proxyPac string) (*Proxy, error) {
	dialer, err := wiredialer.NewDialerFromFile(configuration)
	if err != nil {
		return nil, fmt.Errorf("error creating wireguard dialer: %w", err)
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           dialer.DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          100,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   10 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	}

	return &Proxy{logger: logger, dialer: dialer, transport: transport, proxyPac: proxyPac}, nil
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
	dest, err := p.dialer.DialContext(r.Context(), "tcp", r.Host)
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

	hijacker, ok := w.(http.Hijacker)
	if !ok {
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"error getting hijack interface",
		)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	client, _, err := hijacker.Hijack()
	if err != nil {
		p.logger.LogAttrs(
			r.Context(),
			slog.LevelError,
			"error hijacking client connection",
			slog.String("error", err.Error()),
		)
		http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
		return
	}

	// should be w.WriteHeader(http.StatusOK), but the connection is hijacked
	client.Write([]byte("HTTP/1.1 200 OK\r\n\r\n"))

	errClientToDest, errDestToClient := p.copy(dest.(ProxyConn), client.(ProxyConn))
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

func (p *Proxy) copy(dest, client ProxyConn) (error, error) {
	var wg sync.WaitGroup
	wg.Add(2)

	var errClientToDest error = nil
	go func() {
		_, errClientToDest = io.Copy(client, dest)
		dest.CloseWrite()
		client.CloseRead()
		wg.Done()
	}()

	var errDestToClient error = nil
	go func() {
		_, errDestToClient = io.Copy(dest, client)
		client.CloseWrite()
		dest.CloseRead()
		wg.Done()
	}()

	wg.Wait()

	dest.Close()
	client.Close()

	return errClientToDest, errDestToClient
}
