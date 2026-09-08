package navlib

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/kchellappan/spacemouse_linux_ws/internal/wamp"
)

// Resource names the client uses in 3dx_rpc:create.
const (
	resMouse      = "3dconnexion:3dmouse"
	resController = "3dconnexion:3dcontroller"
)

// Bridge implements wamp.Handler for one page connection: it performs the
// 3dmouse/3dcontroller handshake and surfaces a ready Controller.
//
// See docs/02-wire-protocol.md for the message sequence.
type Bridge struct {
	log *slog.Logger

	// OnReady runs once the client has subscribed to its controller topic.
	// It runs on the session read loop's goroutine, so it must not block on
	// further client RPCs — start a goroutine for those.
	OnReady func(ctx context.Context, c *Controller)

	// OnFrameTime is invoked when a client driving its own animation reports
	// a frame time. Must return quickly: the 0.8.1 sample abandons its
	// animation loop if we take longer than 60ms.
	OnFrameTime func(ctx context.Context, c *Controller, t float64)

	// OnClientInfo reports what the create 3dcontroller handshake said, for
	// anything that wants to display it. Runs on the read loop, so it must
	// not block.
	OnClientInfo func(info ClientInfo, q Quirks)

	mu          sync.Mutex
	connexionID string
	instanceID  string
	info        ClientInfo
	controller  *Controller
}

// NewBridge creates a per-connection handler.
func NewBridge(log *slog.Logger) *Bridge { return &Bridge{log: log} }

// Controller returns the live controller, or nil before the handshake finishes.
func (b *Bridge) Controller() *Controller {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.controller
}

// OnCall implements wamp.Handler.
func (b *Bridge) OnCall(ctx context.Context, s *wamp.Session, procURI string, args []json.RawMessage) (any, error) {
	// procURI arrives expanded, e.g. "wss://127.51.68.120/3dconnexion#create".
	op := procURI
	if i := strings.LastIndex(procURI, "#"); i >= 0 {
		op = procURI[i+1:]
	}

	switch op {
	case "create":
		return b.handleCreate(args)
	case "update":
		return b.handleUpdate(ctx, args)
	case "delete":
		return b.handleDelete(args)
	default:
		return nil, fmt.Errorf("unknown procedure %q", procURI)
	}
}

func (b *Bridge) handleCreate(args []json.RawMessage) (any, error) {
	if len(args) < 1 {
		return nil, fmt.Errorf("create: missing resource")
	}
	var resource string
	if err := json.Unmarshal(args[0], &resource); err != nil {
		return nil, fmt.Errorf("create: bad resource: %w", err)
	}

	switch resource {
	case resMouse:
		var libVersion string
		if len(args) > 1 {
			_ = json.Unmarshal(args[1], &libVersion)
		}
		b.mu.Lock()
		b.connexionID = "mouse-" + shortID()
		id := b.connexionID
		b.mu.Unlock()

		b.log.Info("3dmouse created", "connexion", id, "clientLibVersion", libVersion)
		return map[string]string{"connexion": id}, nil

	case resController:
		if len(args) < 3 {
			return nil, fmt.Errorf("create 3dcontroller: want connexion and info")
		}
		var connexion string
		_ = json.Unmarshal(args[1], &connexion)

		b.mu.Lock()
		expected := b.connexionID
		b.mu.Unlock()
		if connexion != expected {
			return nil, fmt.Errorf("create 3dcontroller: unknown connexion %q", connexion)
		}

		var info ClientInfo
		if err := json.Unmarshal(args[2], &info); err != nil {
			return nil, fmt.Errorf("create 3dcontroller: bad info: %w", err)
		}

		b.mu.Lock()
		b.instanceID = "ctl-" + shortID()
		b.info = info
		id := b.instanceID
		b.mu.Unlock()

		q := QuirksFor(info)
		b.log.Info("3dcontroller created",
			"instance", id,
			"client", info.Name,
			"clientVersion", info.Version,
			"matrixLayout", q.Layout.String(),
			"frameTiming", q.FrameTiming)
		if info.RowMajorOrder == nil && info.Version < 0.5 {
			b.log.Warn("client predates 3DconnexionJS 0.5; assuming row-major matrices",
				"client", info.Name)
		}
		if b.OnClientInfo != nil {
			b.OnClientInfo(info, q)
		}
		return map[string]string{"instance": id}, nil
	}
	return nil, fmt.Errorf("create: unknown resource %q", resource)
}

// clientUpdate is the payload of a client-initiated 3dx_rpc:update.
type clientUpdate struct {
	Focus *bool `json:"focus"`
	Frame *struct {
		TimingSource *int     `json:"timingSource"`
		Time         *float64 `json:"time"`
	} `json:"frame"`
	Commands json.RawMessage `json:"commands"`
	Images   json.RawMessage `json:"images"`
}

func (b *Bridge) handleUpdate(ctx context.Context, args []json.RawMessage) (any, error) {
	if len(args) < 2 {
		return map[string]any{}, nil
	}
	var upd clientUpdate
	if err := json.Unmarshal(args[1], &upd); err != nil {
		// Unknown shapes are not fatal; the client tolerates a bare ack.
		b.log.Debug("update: undecodable payload", "raw", string(args[1]))
		return map[string]any{}, nil
	}

	c := b.Controller()

	if upd.Focus != nil {
		if c != nil {
			c.setFocus(*upd.Focus)
		}
		b.log.Debug("client focus", "focus", *upd.Focus)
	}
	if upd.Frame != nil {
		if upd.Frame.TimingSource != nil && c != nil {
			on := *upd.Frame.TimingSource != 0
			c.setClientDrivesFrames(on)
			b.log.Info("client frame timing", "clientDriven", on)
		}
		if upd.Frame.Time != nil && c != nil {
			// Must return immediately: we are on the read loop, and the page
			// is waiting on this reply with a 60ms budget before it abandons
			// its animation loop.
			c.notifyFrameTime(*upd.Frame.Time)
			if b.OnFrameTime != nil {
				b.OnFrameTime(ctx, c, *upd.Frame.Time)
			}
		}
	}
	if len(upd.Commands) > 0 {
		b.log.Debug("client exported commands", "bytes", len(upd.Commands))
	}
	if len(upd.Images) > 0 {
		b.log.Debug("client exported images", "bytes", len(upd.Images))
	}
	return map[string]any{}, nil
}

func (b *Bridge) handleDelete(args []json.RawMessage) (any, error) {
	b.log.Info("client released the 3dmouse")
	b.mu.Lock()
	b.controller = nil
	b.mu.Unlock()
	return map[string]any{}, nil
}

// OnSubscribe implements wamp.Handler. The client subscribing to its
// controller topic is the signal that the handshake is complete.
func (b *Bridge) OnSubscribe(ctx context.Context, s *wamp.Session, topic string) {
	b.mu.Lock()
	if b.instanceID == "" {
		b.mu.Unlock()
		b.log.Warn("subscribe before 3dcontroller was created", "topic", topic)
		return
	}
	if b.controller != nil {
		b.mu.Unlock()
		return
	}
	c := NewController(s, topic, b.instanceID, b.info)
	b.controller = c
	b.mu.Unlock()

	b.log.Info("controller subscribed", "topic", topic)
	if b.OnReady != nil {
		b.OnReady(ctx, c)
	}
}
