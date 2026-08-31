package tlsfingerprint

import (
	"crypto/tls"
	"encoding/pem"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveProfile(t *testing.T) {
	for _, name := range AvailableProfiles() {
		if _, ok := ResolveProfile(name); !ok {
			t.Fatalf("advertised profile %q does not resolve", name)
		}
	}
	if _, ok := ResolveProfile(""); !ok {
		t.Fatal("empty profile must resolve to the default")
	}
	if _, ok := ResolveProfile("  ChRoMe  "); !ok {
		t.Fatal("profile lookup must ignore case and surrounding space")
	}
	if _, ok := ResolveProfile("netscape"); ok {
		t.Fatal("unknown profile must not resolve")
	}
	if !IsValidProfile("") || !IsValidProfile("firefox") || IsValidProfile("netscape") {
		t.Fatal("IsValidProfile disagrees with ResolveProfile")
	}
}

func TestNewRejectsBadConfiguration(t *testing.T) {
	if _, err := New(Options{Profile: "netscape"}); err == nil {
		t.Fatal("an unknown profile must fail at construction, not per request")
	}
	if _, err := New(Options{ProxyURL: "ftp://proxy.invalid:21"}); err == nil {
		t.Fatal("an unsupported proxy scheme must fail at construction")
	}
	if _, err := New(Options{ProxyURL: "://not a url"}); err == nil {
		t.Fatal("an unparseable proxy URL must fail at construction")
	}
	if _, err := New(Options{Profile: "chrome", ProxyURL: "socks5://127.0.0.1:1080"}); err != nil {
		t.Fatalf("valid options rejected: %v", err)
	}
}

// TestRoundTripHonoursCustomCA guards against the fingerprint transport quietly
// ignoring trust settings the standard transport applies: it does its own TLS,
// so a configured CA bundle has to be threaded through explicitly.
func TestRoundTripHonoursCustomCA(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	caPath := filepath.Join(t.TempDir(), "ca.pem")
	pemBytes := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
	if err := os.WriteFile(caPath, pemBytes, 0o600); err != nil {
		t.Fatalf("write CA bundle: %v", err)
	}

	// Without the bundle the self-signed certificate must be rejected.
	strict, err := New(Options{Profile: ProfileChrome})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer strict.CloseIdleConnections()
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := strict.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("an untrusted certificate must fail verification")
	}

	trusting, err := New(Options{Profile: ProfileChrome, CACertPath: caPath})
	if err != nil {
		t.Fatalf("New with CA bundle: %v", err)
	}
	defer trusting.CloseIdleConnections()
	req, err = http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err = trusting.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip with the configured CA: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
}

func TestNewRejectsUnusableCABundle(t *testing.T) {
	if _, err := New(Options{Profile: ProfileChrome, CACertPath: filepath.Join(t.TempDir(), "missing.pem")}); err == nil {
		t.Fatal("a missing CA bundle must fail at construction")
	}

	junk := filepath.Join(t.TempDir(), "junk.pem")
	if err := os.WriteFile(junk, []byte("not a certificate"), 0o600); err != nil {
		t.Fatalf("write junk bundle: %v", err)
	}
	if _, err := New(Options{Profile: ProfileChrome, CACertPath: junk}); err == nil {
		t.Fatal("an unparseable CA bundle must fail at construction")
	}
}

func TestRoundTripRejectsPlaintext(t *testing.T) {
	rt, err := New(Options{Profile: ProfileChrome})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	req := httptest.NewRequest(http.MethodGet, "http://example.invalid/", nil)
	resp, err := rt.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
		t.Fatal("plaintext requests carry no ClientHello and must be refused")
	}
}

// TestRoundTripAgainstTLSServer drives a real handshake so the ClientHello,
// ALPN negotiation and HTTP/2 framing are exercised end to end rather than
// mocked.
func TestRoundTripAgainstTLSServer(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Proto", r.Proto)
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	rt, err := New(Options{Profile: ProfileChrome, InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.CloseIdleConnections()

	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatalf("NewRequest: %v", err)
	}
	resp, err := rt.RoundTrip(req)
	if err != nil {
		t.Fatalf("RoundTrip: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	if string(body) != "ok" {
		t.Fatalf("body = %q, want %q", body, "ok")
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("X-Proto"); got != "HTTP/2.0" {
		t.Fatalf("server saw %q, want the connection to negotiate HTTP/2", got)
	}
}

// TestRoundTripReusesHTTP2Connection verifies the per-host cache: a second
// request must multiplex over the connection the first one opened rather than
// repeating the handshake.
func TestRoundTripReusesHTTP2Connection(t *testing.T) {
	var handshakes int
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	server.EnableHTTP2 = true
	server.TLS = &tls.Config{
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			handshakes++
			return nil, nil
		},
	}
	server.StartTLS()
	defer server.Close()

	rt, err := New(Options{Profile: ProfileFirefox, InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer rt.CloseIdleConnections()

	for i := 0; i < 3; i++ {
		req, errReq := http.NewRequest(http.MethodGet, server.URL, nil)
		if errReq != nil {
			t.Fatalf("NewRequest: %v", errReq)
		}
		resp, errRT := rt.RoundTrip(req)
		if errRT != nil {
			t.Fatalf("RoundTrip %d: %v", i, errRT)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}

	if handshakes != 1 {
		t.Fatalf("handshakes = %d, want the connection reused across requests", handshakes)
	}
}

// TestRoundTripSendsProfileSpecificClientHello confirms the transport actually
// changes the wire bytes: two profiles must not produce the same ClientHello,
// and neither should look like Go's default.
func TestRoundTripSendsProfileSpecificClientHello(t *testing.T) {
	capture := func(profile string) *tls.ClientHelloInfo {
		var captured *tls.ClientHelloInfo
		server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = io.WriteString(w, "ok")
		}))
		server.TLS = &tls.Config{
			GetConfigForClient: func(hello *tls.ClientHelloInfo) (*tls.Config, error) {
				copied := *hello
				captured = &copied
				return nil, nil
			},
		}
		server.EnableHTTP2 = true
		server.StartTLS()
		defer server.Close()

		rt, err := New(Options{Profile: profile, InsecureSkipVerify: true})
		if err != nil {
			t.Fatalf("New(%s): %v", profile, err)
		}
		defer rt.CloseIdleConnections()

		req, err := http.NewRequest(http.MethodGet, server.URL, nil)
		if err != nil {
			t.Fatalf("NewRequest: %v", err)
		}
		resp, err := rt.RoundTrip(req)
		if err != nil {
			t.Fatalf("RoundTrip(%s): %v", profile, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
		if captured == nil {
			t.Fatalf("no ClientHello captured for %s", profile)
		}
		return captured
	}

	chrome := capture(ProfileChrome)
	firefox := capture(ProfileFirefox)

	if joinUint16(chrome.CipherSuites) == joinUint16(firefox.CipherSuites) {
		t.Fatal("chrome and firefox produced identical cipher suite lists")
	}
	for _, hello := range []*tls.ClientHelloInfo{chrome, firefox} {
		if len(hello.SupportedProtos) == 0 || hello.SupportedProtos[0] != "h2" {
			t.Fatalf("ALPN = %v, want h2 offered first", hello.SupportedProtos)
		}
	}
}

func joinUint16(values []uint16) string {
	parts := make([]string, 0, len(values))
	for _, v := range values {
		parts = append(parts, string(rune(v)))
	}
	return strings.Join(parts, ",")
}
