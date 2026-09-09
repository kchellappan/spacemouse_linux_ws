package webui

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/kchellappan/spacemouse_linux_ws/internal/logbuf"
)

type fakeDevice struct {
	cfg      DeviceConfig
	readErr  error
	writeErr error
	saved    int
	restored int
	wrote    *DeviceConfig
}

func (f *fakeDevice) Read() (DeviceConfig, error) { return f.cfg, f.readErr }
func (f *fakeDevice) Write(c DeviceConfig) error {
	if f.writeErr != nil {
		return f.writeErr
	}
	f.wrote = &c
	f.cfg = c
	return nil
}
func (f *fakeDevice) Save() error    { f.saved++; return nil }
func (f *fakeDevice) Restore() error { f.restored++; return nil }

func deviceHandlerFor(t *testing.T, d DeviceConfigStore) *Handler {
	t.Helper()
	return New(func() Snapshot { return Snapshot{} }, logbuf.New(10), nil, d)
}

func do(t *testing.T, h *Handler, method, path, ct, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if ct != "" {
		req.Header.Set("Content-Type", ct)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func supportedConfig() DeviceConfig {
	return DeviceConfig{
		Supported: true, Sensitivity: 1,
		AxisSensitivity: [6]float64{1, 1, 1, 1, 1, 1},
		Deadzone:        []int32{2, 2, 2, 2, 2, 2},
	}
}

// Every mutating endpoint needs the content-type check, not just the first
// one written: it is what forces a cross-origin caller to preflight.
func TestDeviceWritesRequireJSONContentType(t *testing.T) {
	d := &fakeDevice{cfg: supportedConfig()}
	h := deviceHandlerFor(t, d)

	for _, tc := range []struct{ method, path string }{
		{http.MethodPut, "/api/device"},
		{http.MethodPost, "/api/device/save"},
		{http.MethodPost, "/api/device/restore"},
	} {
		rec := do(t, h, tc.method, tc.path, "text/plain", `{}`)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("%s %s with text/plain returned %d, want 415", tc.method, tc.path, rec.Code)
		}
	}
	if d.wrote != nil || d.saved != 0 || d.restored != 0 {
		t.Error("a request with a simple content type reached the device")
	}
}

// Saving rewrites a root-owned system file, so it must not be reachable by a
// method a page could issue incidentally.
func TestDeviceActionsRequirePOST(t *testing.T) {
	d := &fakeDevice{cfg: supportedConfig()}
	h := deviceHandlerFor(t, d)

	for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodDelete} {
		rec := do(t, h, method, "/api/device/save", "application/json", `{}`)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s on save returned %d, want 405", method, rec.Code)
		}
	}
	if d.saved != 0 {
		t.Error("save ran for a non-POST request")
	}
}

func TestDeviceReadAndWrite(t *testing.T) {
	d := &fakeDevice{cfg: supportedConfig()}
	h := deviceHandlerFor(t, d)

	if rec := do(t, h, http.MethodGet, "/api/device", "", ""); rec.Code != http.StatusOK {
		t.Fatalf("GET returned %d", rec.Code)
	}

	body := `{"supported":true,"sensitivity":2,"axisSensitivity":[1,1,1,1,1,1],"deadzone":[2,2,2,2,2,2]}`
	rec := do(t, h, http.MethodPut, "/api/device", "application/json", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT returned %d: %s", rec.Code, rec.Body.String())
	}
	if d.wrote == nil || d.wrote.Sensitivity != 2 {
		t.Errorf("device received %+v", d.wrote)
	}
	// The response is the daemon's state after the write, not the request
	// echoed back, so the page cannot drift from what actually applied.
	if !strings.Contains(rec.Body.String(), `"sensitivity":2`) {
		t.Errorf("response did not carry the re-read config: %s", rec.Body.String())
	}
}

func TestDeviceSaveAndRestore(t *testing.T) {
	d := &fakeDevice{cfg: supportedConfig()}
	h := deviceHandlerFor(t, d)

	if rec := do(t, h, http.MethodPost, "/api/device/save", "application/json", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("save returned %d", rec.Code)
	}
	if d.saved != 1 {
		t.Errorf("save ran %d times", d.saved)
	}
	if rec := do(t, h, http.MethodPost, "/api/device/restore", "application/json", `{}`); rec.Code != http.StatusOK {
		t.Fatalf("restore returned %d", rec.Code)
	}
	if d.restored != 1 {
		t.Errorf("restore ran %d times", d.restored)
	}
}

// An old daemon is an ordinary state the page explains, not a failure it
// should retry — so the read reports it in the body rather than as a 5xx.
func TestDeviceReportsUnsupportedInTheBody(t *testing.T) {
	d := &fakeDevice{readErr: errors.New("needs protocol 1 or later")}
	h := deviceHandlerFor(t, d)

	rec := do(t, h, http.MethodGet, "/api/device", "", "")
	if rec.Code != http.StatusOK {
		t.Errorf("status = %d, want 200 with an explanation", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), `"supported":false`) {
		t.Errorf("body = %q, want supported:false", rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "protocol") {
		t.Errorf("body = %q, want the reason", rec.Body.String())
	}
}

func TestDeviceAbsentWithoutAStore(t *testing.T) {
	h := deviceHandlerFor(t, nil)

	rec := do(t, h, http.MethodGet, "/api/device", "", "")
	if !strings.Contains(rec.Body.String(), `"supported":false`) {
		t.Errorf("body = %q", rec.Body.String())
	}
	if rec := do(t, h, http.MethodPost, "/api/device/save", "application/json", `{}`); rec.Code != http.StatusServiceUnavailable {
		t.Errorf("save without a device returned %d, want 503", rec.Code)
	}
}

// A rejected write must not reach the daemon: a bad value applied to
// spacenavd affects every application on the machine.
func TestDeviceWriteErrorIsReported(t *testing.T) {
	d := &fakeDevice{cfg: supportedConfig(), writeErr: errors.New("sensitivity must be greater than zero")}
	h := deviceHandlerFor(t, d)

	rec := do(t, h, http.MethodPut, "/api/device", "application/json", `{"sensitivity":0}`)
	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "sensitivity") {
		t.Errorf("body = %q, want it to name the offending field", rec.Body.String())
	}
}
