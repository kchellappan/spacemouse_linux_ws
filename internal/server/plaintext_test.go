package server_test

import (
	"crypto/tls"
	"net"
	"net/http"
	"testing"
	"time"

	"github.com/kchellappan/spacemouse_linux_ws/internal/certs"
	"github.com/kchellappan/spacemouse_linux_ws/internal/server"
)

// tlsTestServer serves the bridge over TLS behind the plaintext-redirecting
// listener, the way main does.
func tlsTestServer(t *testing.T) string {
	t.Helper()

	// Real credentials from the package that generates them in production;
	// the client skips verification, so the SAN mismatch with 127.0.0.1 is
	// irrelevant here.
	paths := certs.At(t.TempDir())
	if _, err := certs.Ensure(paths); err != nil {
		t.Fatalf("generating test credentials: %v", err)
	}
	cert, key := paths.FullChain, paths.LeafKey

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{
		Handler:           server.New(server.Options{Log: quietLogger()}),
		ReadHeaderTimeout: 5 * time.Second,
		TLSNextProto:      map[string]func(*http.Server, *tls.Conn, http.Handler){},
	}
	go func() { _ = srv.ServeTLS(server.PlaintextRedirect(ln, quietLogger()), cert, key) }()
	t.Cleanup(func() { _ = srv.Close() })

	return ln.Addr().String()
}

// Browsers default a bare host:port to http://, so this is what a user typing
// the address actually sends. Go's bare TLS listener answers it with "Client
// sent an HTTP request to an HTTPS server", which reads like a crash.
func TestPlaintextRequestIsRedirectedToHTTPS(t *testing.T) {
	addr := tlsTestServer(t)

	client := &http.Client{
		Timeout: 5 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	resp, err := client.Get("http://" + addr + "/api/status")
	if err != nil {
		t.Fatalf("plaintext request failed outright: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusPermanentRedirect {
		t.Errorf("status = %d, want 308", resp.StatusCode)
	}
	// The path has to survive, or a bookmark to a sub-page lands on the root.
	if got, want := resp.Header.Get("Location"), "https://"+addr+"/api/status"; got != want {
		t.Errorf("Location = %q, want %q", got, want)
	}
}

// The redirect must not disturb the TLS path it wraps.
func TestTLSStillServesThroughTheWrappedListener(t *testing.T) {
	addr := tlsTestServer(t)

	client := &http.Client{
		Timeout: 5 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
		},
	}
	resp, err := client.Get("https://" + addr + "/3dconnexion/nlproxy")
	if err != nil {
		t.Fatalf("TLS request failed: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("status = %d, want 200", resp.StatusCode)
	}
}

// A connection that opens and says nothing must not wedge Accept: the loop
// classifies each connection by peeking, and a silent client would otherwise
// hold every later connection behind it.
func TestSilentConnectionDoesNotBlockTheListener(t *testing.T) {
	addr := tlsTestServer(t)

	silent, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer silent.Close()

	done := make(chan error, 1)
	go func() {
		client := &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			},
		}
		resp, err := client.Get("https://" + addr + "/3dconnexion/nlproxy")
		if err == nil {
			resp.Body.Close()
		}
		done <- err
	}()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("a later TLS request failed: %v", err)
		}
	case <-time.After(8 * time.Second):
		t.Fatal("a silent connection blocked the listener")
	}
}
