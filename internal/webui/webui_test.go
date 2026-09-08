package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/kchellappan/spacemouse_linux_ws/internal/logbuf"
)

func newHandler(t *testing.T) (*Handler, *logbuf.Buffer) {
	t.Helper()
	logs := logbuf.New(50)
	return New(func() Snapshot { return Snapshot{Version: "test", Listen: "127.51.68.120:8181"} }, logs, nil, nil), logs
}

func TestStatusReturnsTheSnapshot(t *testing.T) {
	h, _ := newHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/status", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}
	var got Snapshot
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Version != "test" || got.Listen != "127.51.68.120:8181" {
		t.Errorf("snapshot = %+v", got)
	}
}

func TestLogsHonourSince(t *testing.T) {
	h, logs := newHandler(t)
	for i := 0; i < 4; i++ {
		logs.Append(logbuf.Record{Message: "x"})
	}

	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/logs?since=2", nil))

	var got []logbuf.Record
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d records after seq 2, want 2", len(got))
	}
}

// The stream must send an initial status immediately: a page that has to wait
// for the first tick renders empty, which looks broken.
func TestEventsSendStatusImmediately(t *testing.T) {
	h, _ := newHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q, want text/event-stream", ct)
	}

	sc := bufio.NewScanner(resp.Body)
	var sawStatus bool
	for sc.Scan() {
		if sc.Text() == "event: status" {
			sawStatus = true
			break
		}
	}
	if !sawStatus {
		t.Error("no status event on connect")
	}
}

// A log written after the stream opens must reach it without waiting for the
// status tick.
func TestEventsPushNewLogs(t *testing.T) {
	h, logs := newHandler(t)
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/events", nil)
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	go func() {
		time.Sleep(100 * time.Millisecond)
		logs.Append(logbuf.Record{Message: "a distinctive line", Level: "WARN"})
	}()

	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.Contains(sc.Text(), "a distinctive line") {
			return
		}
	}
	t.Error("the new log record never arrived on the stream")
}

func TestIndexIsNotServedForUnknownPaths(t *testing.T) {
	h, _ := newHandler(t)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/nope", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("unknown path returned %d, want 404", rec.Code)
	}
}

func handlerWithScene(t *testing.T, scenes SceneFactory) *Handler {
	t.Helper()
	return New(func() Snapshot { return Snapshot{Version: "test"} }, logbuf.New(10), scenes, nil)
}

func TestSceneStreamsFrames(t *testing.T) {
	var n int
	h := handlerWithScene(t, func() SceneStepper {
		return func(time.Duration) SceneFrame {
			n++
			return SceneFrame{Camera: make([]float64, 16), Moved: n%2 == 0}
		}
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/scene", nil)
	resp, err := http.DefaultClient.Do(req.WithContext(ctx))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type = %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	for sc.Scan() {
		if strings.HasPrefix(sc.Text(), "data: ") {
			var f SceneFrame
			if err := json.Unmarshal([]byte(strings.TrimPrefix(sc.Text(), "data: ")), &f); err != nil {
				t.Fatalf("frame is not valid JSON: %v", err)
			}
			if len(f.Camera) != 16 {
				t.Errorf("camera has %d elements, want 16", len(f.Camera))
			}
			return
		}
	}
	t.Error("no frame arrived on the scene stream")
}

// Outside drive mode there is no device to drive a scene. Saying so beats a
// 404, which would look like a broken build to someone with the page open.
func TestSceneReportsUnavailableWithoutADevice(t *testing.T) {
	h := handlerWithScene(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/scene", nil))

	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "drive") {
		t.Errorf("body = %q, want it to name the mode required", rec.Body.String())
	}
}

// Each connection gets its own scene, so reloading the page recentres.
func TestEachSceneConnectionGetsItsOwnStepper(t *testing.T) {
	var made int
	h := handlerWithScene(t, func() SceneStepper {
		made++
		return func(time.Duration) SceneFrame { return SceneFrame{Camera: make([]float64, 16)} }
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/scene", nil)
		resp, err := http.DefaultClient.Do(req.WithContext(ctx))
		if err != nil {
			cancel()
			t.Fatal(err)
		}
		buf := make([]byte, 1)
		_, _ = resp.Body.Read(buf)
		resp.Body.Close()
		cancel()
	}
	if made < 2 {
		t.Errorf("the factory ran %d times for 2 connections", made)
	}
}

func TestTestPageIsServed(t *testing.T) {
	h := handlerWithScene(t, nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/test", nil))

	if rec.Code != http.StatusOK {
		t.Fatalf("GET /test returned %d", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "self test") {
		t.Error("the page does not look like the self test")
	}
}

func settingsHandler(t *testing.T, write SettingsWriter) *Handler {
	t.Helper()
	return New(func() Snapshot { return Snapshot{Version: "test"} }, logbuf.New(10), nil, write)
}

func putSettings(t *testing.T, h *Handler, contentType, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodPut, "/api/settings", strings.NewReader(body))
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

// This is the control that actually stops a cross-origin write. CORS decides
// whether a page may read a response, not whether the request is delivered:
// a cross-origin POST with a simple content type still executes. Requiring
// application/json makes the request non-simple, so the browser must
// preflight, and nothing answers a preflight.
func TestSettingsRequiresJSONContentType(t *testing.T) {
	var called bool
	h := settingsHandler(t, func([]byte, bool) error { called = true; return nil })

	for _, ct := range []string{"", "text/plain", "application/x-www-form-urlencoded", "multipart/form-data"} {
		rec := putSettings(t, h, ct, `{"panSpeed":2}`)
		if rec.Code != http.StatusUnsupportedMediaType {
			t.Errorf("Content-Type %q returned %d, want 415", ct, rec.Code)
		}
	}
	if called {
		t.Error("a request with a simple content type reached the writer")
	}
}

func TestSettingsAcceptsJSON(t *testing.T) {
	var got string
	var persisted bool
	h := settingsHandler(t, func(b []byte, p bool) error {
		got, persisted = string(b), p
		return nil
	})

	rec := putSettings(t, h, "application/json; charset=utf-8", `{"panSpeed":2}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rec.Code, rec.Body.String())
	}
	if got != `{"panSpeed":2}` {
		t.Errorf("writer received %q", got)
	}
	if persisted {
		t.Error("a request without ?persist=1 was saved to disk")
	}
}

func TestSettingsPersistFlag(t *testing.T) {
	var persisted bool
	h := settingsHandler(t, func(_ []byte, p bool) error { persisted = p; return nil })

	req := httptest.NewRequest(http.MethodPut, "/api/settings?persist=1", strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	if !persisted {
		t.Error("?persist=1 did not reach the writer")
	}
}

// The message goes on screen, so it has to say what is wrong with the value.
func TestSettingsReturnsTheValidationMessage(t *testing.T) {
	h := settingsHandler(t, func([]byte, bool) error {
		return errBadDeadzone
	})
	rec := putSettings(t, h, "application/json", `{"deadzone":1.5}`)

	if rec.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", rec.Code)
	}
	if !strings.Contains(rec.Body.String(), "deadzone") {
		t.Errorf("body = %q, want it to name the offending field", rec.Body.String())
	}
}

func TestSettingsRejectsOtherMethods(t *testing.T) {
	h := settingsHandler(t, func([]byte, bool) error { return nil })

	for _, method := range []string{http.MethodGet, http.MethodPost, http.MethodDelete} {
		req := httptest.NewRequest(method, "/api/settings", strings.NewReader(`{}`))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("%s returned %d, want 405", method, rec.Code)
		}
	}
}

func TestSettingsUnavailableWithoutAWriter(t *testing.T) {
	h := settingsHandler(t, nil)
	rec := putSettings(t, h, "application/json", `{}`)
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("status = %d, want 503", rec.Code)
	}
}

var errBadDeadzone = errors.New("deadzone must be at least 0 and below 1, not 1.5")
