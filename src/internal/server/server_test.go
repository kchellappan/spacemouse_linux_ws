package server_test

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"

	"spacemouse-bridge/internal/navlib"
	"spacemouse-bridge/internal/server"
	"spacemouse-bridge/internal/wamp"
)

// fakeClient plays the part of 3DconnexionJS: it performs the handshake and
// answers the driver's self:read / self:update RPCs from a canned property
// table, replying CALLERROR for anything absent (which is what the real
// library does for properties an app does not implement).
type fakeClient struct {
	t    *testing.T
	conn *websocket.Conn

	props map[string]any

	mu      sync.Mutex
	reads   []string
	writes  map[string]json.RawMessage
	pending map[string]chan []json.RawMessage

	topic string
	done  chan struct{}

	// closed guards the background read loop against reporting failures after
	// the test has finished, which panics the test binary.
	closed atomic.Bool
}

func dialFake(t *testing.T, url string, props map[string]any) *fakeClient {
	t.Helper()
	d := websocket.Dialer{Subprotocols: []string{wamp.Subprotocol}}
	conn, resp, err := d.Dial(url, nil)
	if err != nil {
		if resp != nil {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("dial: %v (%s)", err, b)
		}
		t.Fatalf("dial: %v", err)
	}
	if got := conn.Subprotocol(); got != wamp.Subprotocol {
		t.Fatalf("subprotocol = %q, want %q", got, wamp.Subprotocol)
	}
	c := &fakeClient{
		t: t, conn: conn, props: props,
		writes:  map[string]json.RawMessage{},
		pending: map[string]chan []json.RawMessage{},
		done:    make(chan struct{}),
	}
	t.Cleanup(func() {
		c.closed.Store(true)
		conn.Close()
		// Let the read loop notice and unwind before the test frame goes away.
		select {
		case <-c.done:
		case <-time.After(time.Second):
		}
	})
	return c
}

func (c *fakeClient) send(v ...any) {
	if c.closed.Load() {
		return
	}
	b, err := json.Marshal(v)
	if err != nil {
		c.t.Errorf("marshal: %v", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if err := c.conn.WriteMessage(websocket.TextMessage, b); err != nil && !c.closed.Load() {
		c.t.Errorf("write: %v", err)
	}
}

// readLoop dispatches inbound frames: EVENT-wrapped driver RPCs get answered,
// CALLRESULTs resolve our own outstanding calls.
func (c *fakeClient) readLoop() {
	defer close(c.done)
	for {
		_, raw, err := c.conn.ReadMessage()
		if err != nil || c.closed.Load() {
			return
		}
		var msg []json.RawMessage
		if err := json.Unmarshal(raw, &msg); err != nil || len(msg) == 0 {
			continue
		}
		var typ int
		_ = json.Unmarshal(msg[0], &typ)

		switch wamp.Type(typ) {
		case wamp.TypeCallResult:
			var id string
			_ = json.Unmarshal(msg[1], &id)
			c.mu.Lock()
			ch := c.pending[id]
			delete(c.pending, id)
			c.mu.Unlock()
			if ch != nil {
				ch <- msg[2:]
			}
		case wamp.TypeEvent:
			c.handleEvent(msg)
		}
	}
}

// handleEvent unwraps the CALL the driver smuggled inside an EVENT payload.
func (c *fakeClient) handleEvent(msg []json.RawMessage) {
	if len(msg) < 3 {
		return
	}
	var inner []json.RawMessage
	if err := json.Unmarshal(msg[2], &inner); err != nil || len(inner) < 5 {
		return
	}
	var innerType int
	_ = json.Unmarshal(inner[0], &innerType)
	if wamp.Type(innerType) != wamp.TypeCall {
		return
	}
	var callID, proc, prop string
	_ = json.Unmarshal(inner[1], &callID)
	_ = json.Unmarshal(inner[2], &proc)
	_ = json.Unmarshal(inner[4], &prop)

	switch proc {
	case "self:read":
		c.mu.Lock()
		c.reads = append(c.reads, prop)
		v, ok := c.props[prop]
		c.mu.Unlock()
		if !ok {
			c.send(int(wamp.TypeCallError), callID, "self:read#generic", prop+" unknown property")
			return
		}
		c.send(int(wamp.TypeCallResult), callID, v)
	case "self:update":
		c.mu.Lock()
		if len(inner) > 5 {
			c.writes[prop] = inner[5]
			// Apply the write so later reads observe it, as a real page does.
			// Marshalling a json.RawMessage re-emits it verbatim.
			if _, readable := c.props[prop]; readable {
				c.props[prop] = inner[5]
			}
		}
		c.mu.Unlock()
		c.send(int(wamp.TypeCallResult), callID, nil)
	default:
		c.send(int(wamp.TypeCallError), callID, proc+"#generic", "unknown procedure")
	}
}

// call performs a client-initiated RPC and waits for the reply.
func (c *fakeClient) call(proc string, args ...any) []json.RawMessage {
	c.t.Helper()
	id := fmt.Sprintf("0.%d", time.Now().UnixNano())
	ch := make(chan []json.RawMessage, 1)
	c.mu.Lock()
	c.pending[id] = ch
	c.mu.Unlock()

	c.send(append([]any{int(wamp.TypeCall), id, proc}, args...)...)

	select {
	case r := <-ch:
		return r
	case <-time.After(5 * time.Second):
		c.t.Fatalf("timed out waiting for reply to %s", proc)
		return nil
	}
}

// handshake performs the same sequence 3dconnexion.js does.
func (c *fakeClient) handshake(info navlib.ClientInfo) {
	c.t.Helper()

	_, raw, err := c.conn.ReadMessage()
	if err != nil {
		c.t.Fatalf("reading WELCOME: %v", err)
	}
	var welcome []json.RawMessage
	if err := json.Unmarshal(raw, &welcome); err != nil {
		c.t.Fatalf("WELCOME not JSON: %v", err)
	}
	var typ int
	_ = json.Unmarshal(welcome[0], &typ)
	if wamp.Type(typ) != wamp.TypeWelcome {
		c.t.Fatalf("first frame = %v, want WELCOME", wamp.Type(typ))
	}

	go c.readLoop()

	const base = "wss://127.51.68.120/3dconnexion"
	c.send(int(wamp.TypePrefix), "3dx_rpc", base+"#")
	c.send(int(wamp.TypePrefix), "3dconnexion", base)
	c.send(int(wamp.TypePrefix), "self", "https://example.test/page.html")

	res := c.call("3dx_rpc:create", "3dconnexion:3dmouse", "0.8.1")
	var mouse struct {
		Connexion string `json:"connexion"`
	}
	if err := json.Unmarshal(res[0], &mouse); err != nil || mouse.Connexion == "" {
		c.t.Fatalf("create 3dmouse returned %s (err %v)", res[0], err)
	}

	res = c.call("3dx_rpc:create", "3dconnexion:3dcontroller", mouse.Connexion, info)
	var ctl struct {
		Instance string `json:"instance"`
	}
	if err := json.Unmarshal(res[0], &ctl); err != nil || ctl.Instance == "" {
		c.t.Fatalf("create 3dcontroller returned %s (err %v)", res[0], err)
	}

	c.topic = "3dconnexion:3dcontroller/" + ctl.Instance
	c.send(int(wamp.TypeSubscribe), c.topic)
	c.call("3dx_rpc:update", c.topic, map[string]any{"focus": true})
}

func quietLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestHandshakeAndPropertyExchange(t *testing.T) {
	type ready struct {
		c *navlib.Controller
	}
	readyCh := make(chan ready, 1)
	resultCh := make(chan error, 1)

	var (
		gotAffine  navlib.Matrix4
		gotExtents navlib.Box
		unsupErr   error
	)

	h := server.New(server.Options{
		Log: quietLogger(),
		OnReady: func(ctx context.Context, c *navlib.Controller) {
			go func() {
				readyCh <- ready{c}
				var err error
				if gotAffine, err = c.ReadMatrix4(ctx, navlib.PropViewAffine); err != nil {
					resultCh <- fmt.Errorf("view.affine: %w", err)
					return
				}
				if gotExtents, err = c.ReadBox(ctx, navlib.PropModelExtents); err != nil {
					resultCh <- fmt.Errorf("model.extents: %w", err)
					return
				}
				_, unsupErr = c.ReadBool(ctx, navlib.PropSelectionEmpty)
				if err := c.SetMotion(ctx, true); err != nil {
					resultCh <- fmt.Errorf("motion: %w", err)
					return
				}
				resultCh <- nil
			}()
		},
	})

	srv := httptest.NewServer(h)
	defer srv.Close()

	props := map[string]any{
		navlib.PropViewAffine:   []float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 1.5, 2.5, 10, 1},
		navlib.PropModelExtents: []float64{-3, -0.75, 2.5, 0, 0.75, 5.5},
		// selection.empty deliberately absent -> CALLERROR
	}

	c := dialFake(t, "ws"+strings.TrimPrefix(srv.URL, "http"), props)
	c.handshake(navlib.ClientInfo{Name: "test-harness", Version: 0.8, RowMajorOrder: boolPtr(false)})

	select {
	case r := <-readyCh:
		if r.c.Info.Name != "test-harness" {
			t.Errorf("client name = %q, want test-harness", r.c.Info.Name)
		}
		if r.c.Quirks.Layout != navlib.LayoutColumnMajor {
			t.Errorf("layout = %v, want column-major", r.c.Quirks.Layout)
		}
		if !r.c.Quirks.FrameTiming {
			t.Error("FrameTiming should be true for a 0.8 client")
		}
		if !r.c.Focus() {
			t.Error("focus should be true after the client set it")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnReady never fired — handshake did not complete")
	}

	select {
	case err := <-resultCh:
		if err != nil {
			t.Fatalf("property exchange: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("property exchange timed out")
	}

	if got := gotAffine.Translation(); got != [3]float64{1.5, 2.5, 10} {
		t.Errorf("translation = %v, want [1.5 2.5 10]", got)
	}
	if gotExtents.Center() != [3]float64{-1.5, 0, 4} {
		t.Errorf("model centre = %v, want [-1.5 0 4]", gotExtents.Center())
	}
	if !wamp.IsUnsupported(unsupErr) {
		t.Errorf("selection.empty error = %v, want a CALLERROR", unsupErr)
	}

	c.mu.Lock()
	reads, writes := append([]string(nil), c.reads...), c.writes
	c.mu.Unlock()

	if len(reads) < 3 {
		t.Errorf("expected at least 3 reads, got %v", reads)
	}
	if _, ok := writes[navlib.PropMotion]; !ok {
		t.Errorf("motion was never written; writes = %v", keys(writes))
	}
}

func TestRowMajorClientIsDetected(t *testing.T) {
	readyCh := make(chan *navlib.Controller, 1)
	h := server.New(server.Options{
		Log:     quietLogger(),
		OnReady: func(ctx context.Context, c *navlib.Controller) { readyCh <- c },
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	c := dialFake(t, "ws"+strings.TrimPrefix(srv.URL, "http"), map[string]any{})
	// A pre-0.5 client: version 0, no rowMajorOrder field.
	c.handshake(navlib.ClientInfo{Name: "web_threejs.html", Version: 0})

	select {
	case ctl := <-readyCh:
		if ctl.Quirks.Layout != navlib.LayoutRowMajor {
			t.Errorf("layout = %v, want row-major for a pre-0.5 client", ctl.Quirks.Layout)
		}
		if ctl.Quirks.FrameTiming {
			t.Error("FrameTiming should be false for a 0.x client")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnReady never fired")
	}
}

func TestDiscoveryEndpoint(t *testing.T) {
	srv := httptest.NewServer(server.New(server.Options{Log: quietLogger()}))
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/3dconnexion/nlproxy", nil)
	req.Header.Set("Origin", "https://cad.onshape.com")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "https://cad.onshape.com" {
		t.Errorf("CORS origin = %q, want the request origin echoed", got)
	}
	var body struct {
		Port    int    `json:"port"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Port == 0 || body.Version == "" {
		t.Errorf("discovery body = %+v, want port and version", body)
	}
}

func boolPtr(b bool) *bool { return &b }

func keys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

func TestOrbitRotatesCameraAboutModelCentre(t *testing.T) {
	h := server.New(server.Options{
		Log:     quietLogger(),
		OnReady: server.Orbit(quietLogger(), 720), // fast, so few frames are needed
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	const dist = 10.0
	props := map[string]any{
		navlib.PropViewAffine:   []float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, dist, 1},
		navlib.PropModelExtents: []float64{-1, -1, -1, 1, 1, 1}, // centred on the origin
	}

	c := dialFake(t, "ws"+strings.TrimPrefix(srv.URL, "http"), props)
	c.handshake(navlib.ClientInfo{Name: "orbit-test", Version: 0.8, RowMajorOrder: boolPtr(false)})

	// Wait for the camera to actually move.
	var final []float64
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		raw, ok := c.props[navlib.PropViewAffine].(json.RawMessage)
		c.mu.Unlock()
		if ok {
			var v []float64
			if err := json.Unmarshal(raw, &v); err == nil && len(v) == 16 {
				if v[12] != 0 || v[14] != dist {
					final = v
					break
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if final == nil {
		t.Fatal("camera never moved; the write path is not working")
	}

	// Orbiting about the origin must preserve the camera's distance from it.
	got := math.Sqrt(final[12]*final[12] + final[13]*final[13] + final[14]*final[14])
	if math.Abs(got-dist) > 1e-6 {
		t.Errorf("distance from pivot = %v, want %v (orbit should not translate)", got, dist)
	}

	c.mu.Lock()
	_, motion := c.writes[navlib.PropMotion]
	_, txn := c.writes[navlib.PropTransaction]
	c.mu.Unlock()
	if !motion {
		t.Error("orbit never wrote the motion flag")
	}
	if !txn {
		t.Error("orbit never bracketed frames with transaction")
	}
}

// TestFrameTimingDoesNotDeadlock is the guard on the worst hazard in this
// design: frame.time arrives on the WAMP read loop, and the loop is also what
// delivers replies to our own RPCs. If the frame handler did client work
// inline, the two would wait on each other forever.
//
// The page abandons its animation loop if a frame call takes more than 60ms,
// so this also pins the latency budget.
func TestFrameTimingDoesNotDeadlock(t *testing.T) {
	ready := make(chan *navlib.Controller, 1)

	h := server.New(server.Options{
		Log: quietLogger(),
		OnReady: func(ctx context.Context, c *navlib.Controller) {
			ready <- c
			// Mimic the drive loop: wake on client frame times and do client
			// RPCs in response, off the read loop.
			go func() {
				for {
					select {
					case <-ctx.Done():
						return
					case <-c.FrameTimes():
						_, _ = c.ReadMatrix4(ctx, navlib.PropViewAffine)
					}
				}
			}()
		},
	})
	srv := httptest.NewServer(h)
	defer srv.Close()

	props := map[string]any{
		navlib.PropViewAffine: []float64{1, 0, 0, 0, 0, 1, 0, 0, 0, 0, 1, 0, 0, 0, 10, 1},
	}
	c := dialFake(t, "ws"+strings.TrimPrefix(srv.URL, "http"), props)
	c.handshake(navlib.ClientInfo{Name: "frame-timing", Version: 0.8, RowMajorOrder: boolPtr(false)})

	var ctl *navlib.Controller
	select {
	case ctl = <-ready:
	case <-time.After(5 * time.Second):
		t.Fatal("handshake never completed")
	}

	// The client takes over frame timing.
	c.call("3dx_rpc:update", c.topic, map[string]any{"frame": map[string]any{"timingSource": 1}})
	if !ctl.ClientDrivesFrames() {
		t.Error("frame.timingSource was not recorded")
	}

	// Drive several frames and check each is acknowledged inside the budget.
	for i := 0; i < 10; i++ {
		start := time.Now()
		c.call("3dx_rpc:update", c.topic, map[string]any{
			"frame": map[string]any{"time": float64(i) * 16.7},
		})
		if el := time.Since(start); el > 60*time.Millisecond {
			t.Fatalf("frame %d acknowledged in %v, over the page's 60ms budget", i, el)
		}
	}

	// And the navigation side really did see the frames.
	c.mu.Lock()
	reads := len(c.reads)
	c.mu.Unlock()
	if reads == 0 {
		t.Error("frame times never reached the navigation loop")
	}
}

func TestParseButtons(t *testing.T) {
	got, err := server.ParseButtons("0=fit, 1=menu")
	if err != nil {
		t.Fatalf("ParseButtons: %v", err)
	}
	if got[0] != server.ActionFit || got[1] != server.ActionMenu {
		t.Errorf("got %v", got)
	}

	if m, err := server.ParseButtons(""); err != nil || len(m) != 0 {
		t.Errorf("empty spec = %v, %v; want an empty map and no error", m, err)
	}

	for _, bad := range []string{"fit", "x=fit", "0=teleport", "0="} {
		if _, err := server.ParseButtons(bad); err == nil {
			t.Errorf("ParseButtons(%q) should have failed", bad)
		}
	}
}

func TestParseButtonsAcceptsEveryAction(t *testing.T) {
	for _, a := range server.ButtonActions {
		if _, err := server.ParseButtons("0=" + string(a)); err != nil {
			t.Errorf("action %q rejected: %v", a, err)
		}
	}
}
