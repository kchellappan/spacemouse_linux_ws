package navlib

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"spacemouse-bridge/internal/wamp"
)

// ClientInfo is the object the page sends when creating a 3dcontroller. It is
// the only reliable signal for the client's capabilities and matrix layout.
type ClientInfo struct {
	Name          string  `json:"name"`
	Version       float64 `json:"version"`
	RowMajorOrder *bool   `json:"rowMajorOrder"`
}

// Quirks captures per-client behavioural differences.
// See docs/03-client-library-versions.md.
type Quirks struct {
	Layout Layout
	// FrameTiming reports whether the client understands
	// frame.timingSource / frame.time (added in 3DconnexionJS 0.6).
	FrameTiming bool
}

// QuirksFor derives behaviour from what the client announced.
func QuirksFor(info ClientInfo) Quirks {
	q := Quirks{Layout: LayoutColumnMajor, FrameTiming: info.Version >= 0.6}

	switch {
	case info.RowMajorOrder != nil:
		// 0.5+ tells us explicitly.
		if *info.RowMajorOrder {
			q.Layout = LayoutRowMajor
		}
	case info.Version < 0.5:
		// Pre-0.5 clients predate the default change and are row-major.
		q.Layout = LayoutRowMajor
	}
	return q
}

// Controller is one page's 3dcontroller instance: the property interface we
// read from and write to.
type Controller struct {
	session *wamp.Session
	topic   string

	ID     string
	Info   ClientInfo
	Quirks Quirks

	mu           sync.RWMutex
	focus        bool
	clientFrames bool

	// frameTimes carries client-driven animation timestamps to whoever is
	// driving navigation. Buffered and lossy on purpose: the producer is the
	// WAMP read loop, which must never block — if it did, the RPC replies the
	// consumer is waiting for could not arrive, and the two would deadlock.
	frameTimes chan float64
}

// NewController binds a controller to the topic the client subscribed to.
func NewController(s *wamp.Session, topic, id string, info ClientInfo) *Controller {
	return &Controller{
		session:    s,
		topic:      topic,
		ID:         id,
		Info:       info,
		Quirks:     QuirksFor(info),
		frameTimes: make(chan float64, 1),
	}
}

// Focus reports whether the page's canvas currently has focus. Suppress output
// when it does not.
func (c *Controller) Focus() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.focus
}

func (c *Controller) setFocus(v bool) {
	c.mu.Lock()
	c.focus = v
	c.mu.Unlock()
}

// ClientDrivesFrames reports whether the client took over frame timing by
// setting frame.timingSource. When it has, navigation should update in
// response to FrameTimes rather than on its own ticker.
func (c *Controller) ClientDrivesFrames() bool {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.clientFrames
}

func (c *Controller) setClientDrivesFrames(v bool) {
	c.mu.Lock()
	c.clientFrames = v
	c.mu.Unlock()
}

// FrameTimes yields client animation timestamps, in milliseconds, as reported
// by the page's requestAnimationFrame. Only the most recent is kept.
func (c *Controller) FrameTimes() <-chan float64 { return c.frameTimes }

// notifyFrameTime publishes a timestamp without ever blocking. It runs on the
// WAMP read loop; see the frameTimes field comment.
func (c *Controller) notifyFrameTime(t float64) {
	select {
	case c.frameTimes <- t:
		return
	default:
	}
	// Replace the pending value: the newest timestamp is the useful one.
	select {
	case <-c.frameTimes:
	default:
	}
	select {
	case c.frameTimes <- t:
	default:
	}
}

// Read fetches a property from the page.
func (c *Controller) Read(ctx context.Context, prop string) (json.RawMessage, error) {
	return c.session.CallClient(ctx, c.topic, procRead, prop)
}

// Update writes a property to the page.
func (c *Controller) Update(ctx context.Context, prop string, value any) error {
	_, err := c.session.CallClient(ctx, c.topic, procUpdate, prop, value)
	return err
}

func readAs[T any](ctx context.Context, c *Controller, prop string) (T, error) {
	var out T
	raw, err := c.Read(ctx, prop)
	if err != nil {
		return out, err
	}
	if len(raw) == 0 || string(raw) == "null" {
		return out, fmt.Errorf("navlib: %s returned null", prop)
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return out, fmt.Errorf("navlib: %s: %w", prop, err)
	}
	return out, nil
}

// ReadBool reads a boolean property.
func (c *Controller) ReadBool(ctx context.Context, prop string) (bool, error) {
	return readAs[bool](ctx, c, prop)
}

// ReadFloat reads a numeric property.
func (c *Controller) ReadFloat(ctx context.Context, prop string) (float64, error) {
	return readAs[float64](ctx, c, prop)
}

// ReadVec3 reads a 3-element point or vector.
func (c *Controller) ReadVec3(ctx context.Context, prop string) ([3]float64, error) {
	v, err := readAs[[]float64](ctx, c, prop)
	if err != nil {
		return [3]float64{}, err
	}
	if len(v) != 3 {
		return [3]float64{}, fmt.Errorf("navlib: %s: want 3 elements, got %d", prop, len(v))
	}
	return [3]float64{v[0], v[1], v[2]}, nil
}

// ReadBox reads a bounding box property such as model.extents.
func (c *Controller) ReadBox(ctx context.Context, prop string) (Box, error) {
	v, err := readAs[[]float64](ctx, c, prop)
	if err != nil {
		return Box{}, err
	}
	if len(v) != 6 {
		return Box{}, fmt.Errorf("navlib: %s: want 6 elements, got %d", prop, len(v))
	}
	return Box{v[0], v[1], v[2], v[3], v[4], v[5]}, nil
}

// ReadMatrix4 reads an affine property, tagging it with this client's layout.
func (c *Controller) ReadMatrix4(ctx context.Context, prop string) (Matrix4, error) {
	v, err := readAs[[]float64](ctx, c, prop)
	if err != nil {
		return Matrix4{}, err
	}
	return NewMatrix4(v, c.Quirks.Layout)
}

// WriteMatrix4 writes an affine property.
func (c *Controller) WriteMatrix4(ctx context.Context, prop string, m Matrix4) error {
	if m.Layout != c.Quirks.Layout {
		return fmt.Errorf("navlib: refusing to write %s in %s to a %s client",
			prop, m.Layout, c.Quirks.Layout)
	}
	return c.Update(ctx, prop, m.Slice())
}

// WriteBox writes a bounding box property such as view.extents.
func (c *Controller) WriteBox(ctx context.Context, prop string, b Box) error {
	return c.Update(ctx, prop, b[:])
}

// SetMotion brackets a navigation burst.
func (c *Controller) SetMotion(ctx context.Context, moving bool) error {
	return c.Update(ctx, PropMotion, moving)
}

// BeginTransaction opens a navigation frame. Pass a monotonically increasing
// counter; EndTransaction closes it and cues the page to redraw.
func (c *Controller) BeginTransaction(ctx context.Context, n int64) error {
	return c.Update(ctx, PropTransaction, n)
}

// EndTransaction closes a navigation frame.
func (c *Controller) EndTransaction(ctx context.Context) error {
	return c.Update(ctx, PropTransaction, 0)
}
