package tlsfingerprint

import (
	"bufio"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
)

// connectProxy is a minimal HTTP CONNECT proxy used to exercise the tunnelling
// path, which http.Transport would normally own.
type connectProxy struct {
	listener net.Listener
	wg       sync.WaitGroup

	mu               sync.Mutex
	requireAuth      string
	observedTargets  []string
	observedAuthHdrs []string
}

func newConnectProxy(t *testing.T, requireAuth string) *connectProxy {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	p := &connectProxy{listener: listener, requireAuth: requireAuth}
	p.wg.Add(1)
	go p.serve()
	t.Cleanup(func() {
		_ = listener.Close()
		p.wg.Wait()
	})
	return p
}

func (p *connectProxy) addr() string { return p.listener.Addr().String() }

func (p *connectProxy) targets() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.observedTargets...)
}

func (p *connectProxy) authHeaders() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.observedAuthHdrs...)
}

func (p *connectProxy) serve() {
	defer p.wg.Done()
	for {
		clientConn, err := p.listener.Accept()
		if err != nil {
			return
		}
		p.wg.Add(1)
		go func() {
			defer p.wg.Done()
			p.handle(clientConn)
		}()
	}
}

func (p *connectProxy) handle(clientConn net.Conn) {
	defer func() { _ = clientConn.Close() }()

	reader := bufio.NewReader(clientConn)
	req, err := http.ReadRequest(reader)
	if err != nil {
		return
	}
	if req.Method != http.MethodConnect {
		_, _ = io.WriteString(clientConn, "HTTP/1.1 405 Method Not Allowed\r\n\r\n")
		return
	}

	p.mu.Lock()
	p.observedTargets = append(p.observedTargets, req.Host)
	p.observedAuthHdrs = append(p.observedAuthHdrs, req.Header.Get("Proxy-Authorization"))
	required := p.requireAuth
	p.mu.Unlock()

	if required != "" && req.Header.Get("Proxy-Authorization") != required {
		_, _ = io.WriteString(clientConn, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
		return
	}

	upstream, err := net.Dial("tcp", req.Host)
	if err != nil {
		_, _ = io.WriteString(clientConn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
		return
	}
	defer func() { _ = upstream.Close() }()

	if _, err = io.WriteString(clientConn, "HTTP/1.1 200 Connection Established\r\n\r\n"); err != nil {
		return
	}

	var pipeWG sync.WaitGroup
	pipeWG.Add(2)
	go func() {
		defer pipeWG.Done()
		_, _ = io.Copy(upstream, reader)
	}()
	go func() {
		defer pipeWG.Done()
		_, _ = io.Copy(clientConn, upstream)
	}()
	pipeWG.Wait()
}

func newTLSEchoServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		_, _ = io.WriteString(w, "tunnelled")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	t.Cleanup(server.Close)
	return server
}

func TestRoundTripThroughHTTPConnectProxy(t *testing.T) {
	origin := newTLSEchoServer(t)
	proxy := newConnectProxy(t, "")

	rt, err := New(Options{
		Profile:            ProfileChrome,
		ProxyURL:           "http://" + proxy.addr(),
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.CloseIdleConnections()

	req, err := http.NewRequest(http.MethodGet, origin.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip through CONNECT proxy: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "tunnelled" {
		t.Fatalf("body = %q, want %q", body, "tunnelled")
	}
	// The TLS handshake must happen inside the tunnel, so the origin still sees
	// a fingerprinted HTTP/2 connection rather than something the proxy re-made.
	if got := resp.Header.Get("X-Proto"); got != "HTTP/2.0" {
		t.Fatalf("origin saw %q, want HTTP/2 negotiated inside the tunnel", got)
	}

	targets := proxy.targets()
	if len(targets) != 1 {
		t.Fatalf("proxy saw %d CONNECT requests, want 1", len(targets))
	}
	originHost := strings.TrimPrefix(origin.URL, "https://")
	if targets[0] != originHost {
		t.Fatalf("CONNECT target = %q, want %q", targets[0], originHost)
	}
}

func TestRoundTripSendsProxyCredentials(t *testing.T) {
	origin := newTLSEchoServer(t)

	credentialed := &http.Request{Header: make(http.Header)}
	credentialed.SetBasicAuth("alice", "s3cret")
	expected := credentialed.Header.Get("Authorization")

	proxy := newConnectProxy(t, expected)

	rt, err := New(Options{
		Profile:            ProfileChrome,
		ProxyURL:           "http://alice:s3cret@" + proxy.addr(),
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.CloseIdleConnections()

	req, err := http.NewRequest(http.MethodGet, origin.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip through authenticated proxy: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()

	headers := proxy.authHeaders()
	if len(headers) != 1 || headers[0] != expected {
		t.Fatalf("Proxy-Authorization = %v, want the configured credentials", headers)
	}
}

func TestRoundTripSurfacesProxyRejection(t *testing.T) {
	origin := newTLSEchoServer(t)
	proxy := newConnectProxy(t, "Basic expected-but-not-sent")

	rt, err := New(Options{
		Profile:            ProfileChrome,
		ProxyURL:           "http://" + proxy.addr(),
		InsecureSkipVerify: true,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.CloseIdleConnections()

	req, err := http.NewRequest(http.MethodGet, origin.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("a rejected CONNECT must surface as an error, not a silent fallback")
	}
	if !strings.Contains(err.Error(), "407") {
		t.Fatalf("error = %v, want it to carry the proxy status", err)
	}
}
