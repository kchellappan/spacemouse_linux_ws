package server_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kchellappan/spacemouse_linux_ws/internal/logbuf"
	"github.com/kchellappan/spacemouse_linux_ws/internal/server"
	"github.com/kchellappan/spacemouse_linux_ws/internal/webui"
)

func uiServer(t *testing.T) *httptest.Server {
	t.Helper()
	logs := logbuf.New(50)
	h := server.New(server.Options{
		Log:            quietLogger(),
		NLProxyVersion: "1.4.8.21486",
		Clients:        server.NewClients(),
		UI:             webui.New(func() webui.Snapshot { return webui.Snapshot{Version: "test"} }, logs, nil, nil),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func get(t *testing.T, url, origin string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp
}

// The log records which sites connected to this bridge, so a read endpoint
// leaks browsing activity if any page can fetch it.
func TestAPIRejectsAForeignOrigin(t *testing.T) {
	srv := uiServer(t)
	resp := get(t, srv.URL+"/api/status", "https://evil.example")
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin /api/status returned %d, want 403", resp.StatusCode)
	}
}

func TestAPIAcceptsItsOwnOrigin(t *testing.T) {
	srv := uiServer(t)
	// httptest serves plain HTTP; the guard requires https, so this stands in
	// for the shape of the check rather than the scheme.
	resp := get(t, srv.URL+"/api/status", "")
	if resp.StatusCode != http.StatusOK {
		t.Errorf("same-origin /api/status returned %d, want 200", resp.StatusCode)
	}
	var snap webui.Snapshot
	if err := json.NewDecoder(resp.Body).Decode(&snap); err != nil {
		t.Fatalf("decoding status: %v", err)
	}
	if snap.Version != "test" {
		t.Errorf("version = %q, want the snapshot's value", snap.Version)
	}
}

// CORS headers on /api would let a page read the response even though the
// request is same-origin-guarded. Belt and braces must both be present.
func TestAPICarriesNoCORSHeaders(t *testing.T) {
	srv := uiServer(t)
	resp := get(t, srv.URL+"/api/status", "")
	for _, h := range []string{"Access-Control-Allow-Origin", "Access-Control-Allow-Methods"} {
		if v := resp.Header.Get(h); v != "" {
			t.Errorf("%s = %q on /api, want absent", h, v)
		}
	}
}

// The discovery endpoint is the one place a permissive Origin is required:
// the client library fetches it cross-origin from the CAD site.
func TestDiscoveryStillEchoesOrigin(t *testing.T) {
	srv := uiServer(t)
	resp := get(t, srv.URL+"/3dconnexion/nlproxy", "https://cad.onshape.com")
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://cad.onshape.com" {
		t.Errorf("discovery Access-Control-Allow-Origin = %q, want the request origin", got)
	}
}

func TestUIIsServedAtTheRoot(t *testing.T) {
	srv := uiServer(t)
	resp := get(t, srv.URL+"/", "")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET / returned %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "spacemouse-bridge") {
		t.Error("the root page does not look like the status UI")
	}
	if csp := resp.Header.Get("Content-Security-Policy"); !strings.Contains(csp, "default-src 'none'") {
		t.Errorf("Content-Security-Policy = %q, want a restrictive policy", csp)
	}
}

func TestUIAssetsAreServed(t *testing.T) {
	srv := uiServer(t)
	for _, path := range []string{"/assets/app.css", "/assets/app.js"} {
		resp := get(t, srv.URL+path, "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s returned %d", path, resp.StatusCode)
		}
	}
}

// Mounting the UI must not shadow the WebSocket the CAD client needs.
func TestWAMPUpgradeSurvivesTheUI(t *testing.T) {
	srv := uiServer(t)
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Upgrade", "websocket")
	req.Header.Set("Connection", "Upgrade")
	req.Header.Set("Sec-WebSocket-Version", "13")
	req.Header.Set("Sec-WebSocket-Key", "x3JJHMbDL1EzLkh9GBhXDw==")
	req.Header.Set("Sec-WebSocket-Protocol", "wamp")

	resp, err := http.DefaultTransport.RoundTrip(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Errorf("upgrade returned %d, want 101 — the UI is shadowing the WAMP endpoint", resp.StatusCode)
	}
}

// Regression: /test was registered on the UI's own mux and tested there, but
// the server's outer mux only routed "/" to the UI, so the page fell through
// to the placeholder. Testing the two halves separately missed the seam
// between them, so this goes through server.New rather than the UI handler.
func TestUIPagesBeyondTheRootAreReachable(t *testing.T) {
	srv := uiServer(t)

	for _, path := range []string{"/", "/test"} {
		resp := get(t, srv.URL+path, "")
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s returned %d", path, resp.StatusCode)
			continue
		}
		body, _ := io.ReadAll(resp.Body)
		if strings.Contains(string(body), "Discovery endpoint") {
			t.Errorf("GET %s served the placeholder page, not the UI", path)
		}
	}
}

// A path the UI does not know must still 404 rather than silently rendering
// the placeholder, which reads as "the page exists but is broken".
func TestUnknownUIPathIsNotFound(t *testing.T) {
	srv := uiServer(t)
	resp := get(t, srv.URL+"/no-such-page", "")
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path returned %d, want 404", resp.StatusCode)
	}
}

// With no UI configured the placeholder is still the right answer.
func TestPlaceholderServedWithoutAUI(t *testing.T) {
	srv := httptest.NewServer(server.New(server.Options{Log: quietLogger()}))
	t.Cleanup(srv.Close)

	resp := get(t, srv.URL+"/", "")
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "Discovery endpoint") {
		t.Error("the placeholder page is missing when no UI is configured")
	}
}

// The same-origin guard has to cover writes, not just reads. This is the
// second of the three rules; the content-type requirement is enforced in the
// UI handler and tested there.
func TestSettingsWriteRejectsAForeignOrigin(t *testing.T) {
	logs := logbuf.New(10)
	var called bool
	h := server.New(server.Options{
		Log: quietLogger(),
		UI: webui.New(
			func() webui.Snapshot { return webui.Snapshot{} }, logs, nil,
			func([]byte, bool) error { called = true; return nil },
		),
	})
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPut, srv.URL+"/api/settings",
		strings.NewReader(`{"panSpeed":2}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", "https://evil.example")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("cross-origin write returned %d, want 403", resp.StatusCode)
	}
	if called {
		t.Error("a cross-origin write reached the settings writer")
	}
}
