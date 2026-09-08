// Package webui serves the bridge's own status page.
//
// It is read-only. That does not make it public: the log records which sites
// connected to this bridge, so serving it to any origin would disclose
// browsing activity. The server mounts /api behind a same-origin check for
// exactly that reason — see docs/10-configuration.md.
package webui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io/fs"
	"net/http"
	"time"

	"github.com/kchellappan/spacemouse_linux_ws/internal/logbuf"
)

//go:embed assets
var assets embed.FS

// Snapshot is everything the page displays, gathered at one instant.
type Snapshot struct {
	Version   string    `json:"version"`
	StartedAt time.Time `json:"startedAt"`
	Listen    string    `json:"listen"`

	Spacenavd Spacenavd   `json:"spacenavd"`
	Device    Device      `json:"device"`
	Clients   []Client    `json:"clients"`
	Certs     Certs       `json:"certs"`
	Settings  any         `json:"settings"`
	Warnings  []string    `json:"warnings,omitempty"`
	Profiles  []NSSStatus `json:"profiles"`
}

// Spacenavd reports the daemon connection.
type Spacenavd struct {
	// Enabled is false outside drive mode, where no connection is attempted.
	// Without it the page cannot tell "not reading the device" from "tried
	// and failed", and would show an alarming red for an ordinary state.
	Enabled   bool   `json:"enabled"`
	Connected bool   `json:"connected"`
	Socket    string `json:"socket,omitempty"`
	Dropped   uint64 `json:"dropped"`
	Error     string `json:"error,omitempty"`
}

// Device is the current puck deflection, in raw device units.
type Device struct {
	X  int32 `json:"x"`
	Y  int32 `json:"y"`
	Z  int32 `json:"z"`
	RX int32 `json:"rx"`
	RY int32 `json:"ry"`
	RZ int32 `json:"rz"`
	// FullScale and RotationFullScale let the page draw each axis as a
	// fraction of its own range rather than a bare number.
	FullScale         float64 `json:"fullScale"`
	RotationFullScale float64 `json:"rotationFullScale"`
	AtRest            bool    `json:"atRest"`
}

// Client is one connected page.
type Client struct {
	ID           string    `json:"id"`
	Origin       string    `json:"origin"`
	ConnectedAt  time.Time `json:"connectedAt"`
	Name         string    `json:"name,omitempty"`
	LibVersion   float64   `json:"libVersion,omitempty"`
	MatrixLayout string    `json:"matrixLayout,omitempty"`
	FrameTiming  bool      `json:"clientFrameTiming"`
}

// Certs summarises the TLS credentials.
type Certs struct {
	Dir          string    `json:"dir"`
	CAExpiry     time.Time `json:"caExpiry"`
	LeafExpiry   time.Time `json:"leafExpiry"`
	KeyModeOK    bool      `json:"keyModeOK"`
	Present      bool      `json:"present"`
	CertutilOK   bool      `json:"certutilOK"`
	RunningBrows []string  `json:"runningBrowsers,omitempty"`
}

// NSSStatus is one browser profile and whether it trusts our CA.
type NSSStatus struct {
	Dir     string `json:"dir"`
	Label   string `json:"label"`
	Trusted bool   `json:"trusted"`
}

// Source provides the current state. Implemented by main, which is the only
// place that can see the device, the settings and the certificates at once.
type Source func() Snapshot

// Handler serves the page, its assets, and the API beneath it.
type Handler struct {
	mux    *http.ServeMux
	source Source
	logs   *logbuf.Buffer
}

// New returns a handler serving the status UI.
func New(source Source, logs *logbuf.Buffer) *Handler {
	h := &Handler{mux: http.NewServeMux(), source: source, logs: logs}

	sub, err := fs.Sub(assets, "assets")
	if err != nil {
		panic("webui: embedded assets missing: " + err.Error())
	}
	files := http.FileServer(http.FS(sub))

	h.mux.Handle("/assets/", http.StripPrefix("/assets/", files))
	h.mux.HandleFunc("/api/status", h.status)
	h.mux.HandleFunc("/api/logs", h.logsHandler)
	h.mux.HandleFunc("/api/events", h.events)
	h.mux.HandleFunc("/", h.index)
	return h
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, r *http.Request) { h.mux.ServeHTTP(w, r) }

func (h *Handler) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	page, err := assets.ReadFile("assets/index.html")
	if err != nil {
		http.Error(w, "status page missing", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	// The page is served from the same origin CAD sites talk to, so pin it
	// shut: no external loads, no framing, nothing inline but our own script.
	w.Header().Set("Content-Security-Policy",
		"default-src 'none'; script-src 'self'; style-src 'self'; connect-src 'self'; img-src 'self' data:; frame-ancestors 'none'")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = w.Write(page)
}

func (h *Handler) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.source())
}

func (h *Handler) logsHandler(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, h.logs.Since(parseSeq(r.URL.Query().Get("since"))))
}

// events streams status and log updates.
//
// Server-sent events rather than a WebSocket: the traffic is one-way, the
// browser reconnects on its own, and the WAMP endpoint already owns the only
// WebSocket on this origin — two different socket protocols on one server is
// a debugging trap nobody needs.
func (h *Handler) events(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	wake, release := h.logs.Subscribe()
	defer release()

	seq := parseSeq(r.URL.Query().Get("since"))

	// The device moves at 125Hz but a page redrawing that fast is wasted
	// work and a lot of JSON. Ten frames a second is enough to see a puck
	// move and cheap enough to leave open all day.
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()

	send := func(event string, v any) bool {
		data, err := json.Marshal(v)
		if err != nil {
			return true
		}
		if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, data); err != nil {
			return false
		}
		flusher.Flush()
		return true
	}

	if !send("status", h.source()) {
		return
	}
	if recs := h.logs.Since(seq); len(recs) > 0 {
		seq = recs[len(recs)-1].Seq
		if !send("logs", recs) {
			return
		}
	}

	for {
		select {
		case <-r.Context().Done():
			return
		case <-wake:
			recs := h.logs.Since(seq)
			if len(recs) == 0 {
				continue
			}
			seq = recs[len(recs)-1].Seq
			if !send("logs", recs) {
				return
			}
		case <-tick.C:
			if !send("status", h.source()) {
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		// The status code is already sent; nothing useful is left to do.
		return
	}
}

func parseSeq(s string) uint64 {
	var n uint64
	if _, err := fmt.Sscanf(s, "%d", &n); err != nil {
		return 0
	}
	return n
}
