package webui

import (
	"bufio"
	"context"
	"encoding/json"
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
	return New(func() Snapshot { return Snapshot{Version: "test", Listen: "127.51.68.120:8181"} }, logs), logs
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
