// Package server wires the HTTPS discovery endpoint and the WAMP WebSocket
// that 3DconnexionJS clients expect. See docs/02-wire-protocol.md.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"

	"github.com/gorilla/websocket"

	"spacemouse-bridge/internal/navlib"
	"spacemouse-bridge/internal/wamp"
)

// DefaultHost and DefaultPort are hardcoded in the client library
// (3dconnexion.js:98 and :103) and cannot be configured from the page.
const (
	DefaultHost = "127.51.68.120"
	DefaultPort = 8181
)

// DefaultNLProxyVersion mirrors what the real NL-Proxy reports from the
// discovery endpoint. Some clients may gate on it.
const DefaultNLProxyVersion = "1.4.8.21486"

// Options configures the handler.
type Options struct {
	Log *slog.Logger

	// ServerIdent is echoed in the WAMP WELCOME frame.
	ServerIdent string
	// NLProxyVersion is reported by the discovery endpoint.
	NLProxyVersion string

	// OnReady runs when a client finishes the handshake and subscribes.
	//
	// It runs on the session read loop's goroutine: that loop delivers RPC
	// replies, so anything calling back into the client MUST run in its own
	// goroutine or it will deadlock.
	OnReady func(ctx context.Context, c *navlib.Controller)

	// OnFrameTime runs when a client driving its own animation reports a
	// frame time. Must return fast — the 0.8.1 sample abandons its animation
	// loop after 60ms without a response.
	OnFrameTime func(ctx context.Context, c *navlib.Controller, t float64)
}

func (o *Options) defaults() {
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.ServerIdent == "" {
		o.ServerIdent = "spacemouse-bridge"
	}
	if o.NLProxyVersion == "" {
		o.NLProxyVersion = DefaultNLProxyVersion
	}
}

// New builds the HTTP handler.
func New(o Options) http.Handler {
	o.defaults()

	upgrader := websocket.Upgrader{
		Subprotocols: []string{wamp.Subprotocol},
		// Access is already restricted to loopback; the page's origin is
		// whatever CAD site the user happens to be on, so it is not a useful
		// control here.
		CheckOrigin: func(r *http.Request) bool { return true },
	}

	mux := http.NewServeMux()

	mux.HandleFunc("/3dconnexion/nlproxy", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w, r)
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		o.Log.Info("discovery", "origin", originOf(r))
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"port":    portOf(r),
			"version": o.NLProxyVersion,
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		setCORS(w, r)
		if r.Header.Get("Upgrade") == "" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprint(w, statusPage)
			return
		}
		serveWAMP(w, r, &upgrader, &o)
	})

	return mux
}

func serveWAMP(w http.ResponseWriter, r *http.Request, up *websocket.Upgrader, o *Options) {
	conn, err := up.Upgrade(w, r, nil)
	if err != nil {
		o.Log.Error("websocket upgrade failed", "err", err)
		return
	}
	defer conn.Close()

	o.Log.Info("client connected", "origin", originOf(r), "subprotocol", conn.Subprotocol())

	sess := wamp.NewSession(conn, o.Log)
	bridge := navlib.NewBridge(o.Log)
	bridge.OnReady = o.OnReady
	bridge.OnFrameTime = o.OnFrameTime

	if err := sess.Welcome(o.ServerIdent); err != nil {
		o.Log.Error("failed to send WELCOME", "err", err)
		return
	}
	if err := sess.Serve(r.Context(), bridge); err != nil {
		o.Log.Info("client disconnected", "err", err)
		return
	}
	o.Log.Info("client disconnected")
}

func setCORS(w http.ResponseWriter, r *http.Request) {
	origin := r.Header.Get("Origin")
	if origin == "" {
		origin = "*"
	}
	h := w.Header()
	h.Set("Access-Control-Allow-Origin", origin)
	h.Set("Access-Control-Allow-Methods", "GET, OPTIONS")
	h.Set("Access-Control-Allow-Headers", "*")
	h.Set("Vary", "Origin")
}

func originOf(r *http.Request) string {
	if o := r.Header.Get("Origin"); o != "" {
		return o
	}
	return "-"
}

// portOf reports the port the client reached us on, so the discovery response
// points the WebSocket at this same listener.
func portOf(r *http.Request) int {
	_, p, err := net.SplitHostPort(r.Host)
	if err != nil {
		return DefaultPort
	}
	n, err := strconv.Atoi(p)
	if err != nil {
		return DefaultPort
	}
	return n
}

const statusPage = `<!doctype html><meta charset=utf-8>
<title>spacemouse-bridge</title>
<style>body{font:14px system-ui;margin:3rem auto;max-width:34rem;line-height:1.6}
code{background:#f4f4f5;padding:.15em .4em;border-radius:3px}</style>
<h1>spacemouse-bridge</h1>
<p>Running. If you reached this page without a certificate warning, browser
trust is correctly installed.</p>
<p>Discovery endpoint: <code>/3dconnexion/nlproxy</code></p>`
