package server

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"strconv"
	"strings"
	"time"

	"spacemouse-bridge/internal/nav"
	"spacemouse-bridge/internal/navlib"
	"spacemouse-bridge/internal/spacenav"
	"spacemouse-bridge/internal/wamp"
)

// ParseButtons reads a mapping such as "0=fit,1=menu".
func ParseButtons(spec string) (map[int]ButtonAction, error) {
	out := map[int]ButtonAction{}
	if strings.TrimSpace(spec) == "" {
		return out, nil
	}
	valid := map[ButtonAction]bool{}
	for _, a := range ButtonActions {
		valid[a] = true
	}
	for _, part := range strings.Split(spec, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, name, ok := strings.Cut(part, "=")
		if !ok {
			return nil, fmt.Errorf("button mapping %q: want id=action", part)
		}
		n, err := strconv.Atoi(strings.TrimSpace(id))
		if err != nil {
			return nil, fmt.Errorf("button mapping %q: %w", part, err)
		}
		act := ButtonAction(strings.TrimSpace(name))
		if !valid[act] {
			return nil, fmt.Errorf("button mapping %q: unknown action %q", part, act)
		}
		out[n] = act
	}
	return out, nil
}

// extentsTTL bounds how stale the cached model bounding box may get. Re-reading
// it every frame would double the round trips for a value that rarely changes.
const extentsTTL = 2 * time.Second

// DriveOptions configures the live navigation loop.
type DriveOptions struct {
	Device *spacenav.Client
	Config nav.Config
	// FrameRate is how often the camera is updated when we are driving the
	// clock. The device streams at ~125 Hz, far above display rate, so we
	// sample its latched state on our own timer instead of reacting per
	// event. See docs/05-spacenavd.md.
	//
	// A client that sets frame.timingSource drives the clock instead, and
	// this becomes a fallback heartbeat for noticing new input.
	FrameRate int

	// Buttons maps device button ids to actions. Ids vary by model, so
	// unmapped presses are logged rather than ignored silently.
	// On a SpaceMouse Compact: 0 is the left button, 1 the right.
	Buttons map[int]ButtonAction
}

// ButtonAction is what a device button does.
type ButtonAction string

const (
	// ActionNone logs the press and nothing else.
	ActionNone ButtonAction = "none"
	// ActionFit frames the model, like a driver's Fit command.
	ActionFit ButtonAction = "fit"
	// ActionMenu sends the V3DK_MENU key to the application, which is what
	// the real driver does. Applications that do not implement
	// events.keyPress simply answer CALLERROR and nothing happens.
	ActionMenu ButtonAction = "menu"
	// ActionToggleDominant turns dominant-axis filtering on and off, so a
	// user can suppress cross-talk for one deliberate gesture.
	ActionToggleDominant ButtonAction = "dominant-axis"
	// ActionToggleRotation locks and unlocks rotation, leaving pan and zoom.
	ActionToggleRotation ButtonAction = "rotation-lock"
)

// ButtonActions lists every valid action, for flag parsing and help text.
var ButtonActions = []ButtonAction{
	ActionNone, ActionFit, ActionMenu, ActionToggleDominant, ActionToggleRotation,
}

// Drive connects the SpaceMouse to a client's camera.
//
// Safe to use as Options.OnReady: it starts its own goroutine, which matters
// because OnReady runs on the session read loop and that loop delivers the RPC
// replies this needs.
func Drive(log *slog.Logger, o DriveOptions) func(context.Context, *navlib.Controller) {
	if o.FrameRate <= 0 {
		o.FrameRate = 60
	}
	return func(ctx context.Context, c *navlib.Controller) {
		go runDrive(ctx, log, o, c)
	}
}

func runDrive(ctx context.Context, log *slog.Logger, o DriveOptions, c *navlib.Controller) {
	interval := time.Second / time.Duration(o.FrameRate)
	tick := time.NewTicker(interval)
	defer tick.Stop()

	// A local copy: buttons can toggle settings at runtime.
	cfg := o.Config

	log.Info("navigation active",
		"client", c.Info.Name,
		"frameRate", o.FrameRate,
		"mode", modeName(cfg.Mode),
		"dominantAxis", cfg.DominantAxis)

	var (
		moving      bool
		frame       int64
		extents     navlib.Box
		haveExtents bool
		extentsAt   time.Time
		last        = time.Now()
		lastClient  float64
	)

	stopMoving := func() {
		if !moving {
			return
		}
		moving = false
		sc, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		if err := c.SetMotion(sc, false); err != nil && !wamp.IsUnsupported(err) {
			log.Debug("could not clear motion flag", "err", err)
		}
	}
	defer stopMoving()

	for {
		var dt time.Duration

		select {
		case <-ctx.Done():
			return

		case t := <-c.FrameTimes():
			// The client is driving its own animation loop and has just told
			// us when this frame is. Use its clock so we stay in step.
			if lastClient > 0 && t > lastClient {
				dt = time.Duration((t - lastClient) * float64(time.Millisecond))
			} else {
				dt = interval
			}
			lastClient = t
			// A backgrounded tab can produce an enormous gap; do not let one
			// frame teleport the camera.
			if dt > 100*time.Millisecond {
				dt = 100 * time.Millisecond
			}

		case <-tick.C:
			if c.ClientDrivesFrames() && moving {
				// The client is supplying frames; our ticker would double-step.
				// It still runs while stopped, to notice fresh input.
				continue
			}
			dt = time.Since(last)
		}

		now := time.Now()
		last = now

		handleButtons(ctx, log, o, c, &cfg)

		// The page tells us when its canvas has focus; respect it or we fight
		// whatever else the user is doing.
		if !c.Focus() {
			stopMoving()
			continue
		}

		in := o.Device.State()
		if cfg.ShapeIsZero(in) {
			stopMoving()
			continue
		}

		if !haveExtents || now.Sub(extentsAt) > extentsTTL {
			if b, err := c.ReadBox(ctx, navlib.PropModelExtents); err == nil {
				extents, haveExtents = b, true
			} else if !haveExtents && !wamp.IsUnsupported(err) {
				if ctx.Err() != nil {
					return
				}
				log.Debug("model.extents unavailable", "err", err)
			}
			extentsAt = now
		}

		cur, err := c.ReadMatrix4(ctx, navlib.PropViewAffine)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("cannot read view.affine; navigation stopping", "err", err)
			return
		}

		scene := nav.Scene{
			Camera:       cur.Canonical(),
			Pivot:        extents.Center(),
			ModelExtents: extents,
			// Default to the common case when the client does not say.
			Perspective: true,
			Rotatable:   true,
		}
		if haveExtents {
			scene.ModelDiagonal = extents.Diagonal()
		}
		if p, err := c.ReadVec3(ctx, navlib.PropPivotPosition); err == nil {
			scene.Pivot = p
		}
		if v, err := c.ReadBool(ctx, navlib.PropViewRotatable); err == nil {
			scene.Rotatable = v
		}
		if v, err := c.ReadBool(ctx, navlib.PropViewPerspective); err == nil {
			scene.Perspective = v
		}
		if !scene.Perspective {
			// Orthographic zoom scales the view box; we need its current value.
			if b, err := c.ReadBox(ctx, navlib.PropViewExtents); err == nil {
				scene.ViewExtents = b
			}
		}

		res := cfg.Step(in, dt, scene)
		if !res.Moved {
			stopMoving()
			continue
		}

		if !moving {
			moving = true
			if err := c.SetMotion(ctx, true); err != nil && !wamp.IsUnsupported(err) {
				log.Debug("could not set motion flag", "err", err)
			}
		}

		frame++
		if err := c.BeginTransaction(ctx, frame); err != nil && !wamp.IsUnsupported(err) {
			if ctx.Err() != nil {
				return
			}
		}
		if res.ExtentsChanged {
			if err := c.WriteBox(ctx, navlib.PropViewExtents, res.Extents); err != nil && !wamp.IsUnsupported(err) {
				if ctx.Err() != nil {
					return
				}
				log.Debug("cannot write view.extents", "err", err)
			}
		}
		if err := c.WriteMatrix4(ctx, navlib.PropViewAffine, navlib.FromCanonical(res.Camera, c.Quirks.Layout)); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("cannot write view.affine; navigation stopping", "err", err)
			return
		}
		if err := c.EndTransaction(ctx); err != nil && !wamp.IsUnsupported(err) {
			if ctx.Err() != nil {
				return
			}
		}
	}
}

// handleButtons drains any pending device buttons. Buttons are rare and the
// loop drains every frame, so the channel cannot back up behind them.
func handleButtons(ctx context.Context, log *slog.Logger, o DriveOptions, c *navlib.Controller, cfg *nav.Config) {
	for {
		select {
		case ev, ok := <-o.Device.Events():
			if !ok {
				return
			}
			if ev.Button == nil {
				continue
			}
			applyButton(ctx, log, c, cfg, o.Buttons[int(ev.Button.ID)], *ev.Button)
		default:
			return
		}
	}
}

func applyButton(ctx context.Context, log *slog.Logger, c *navlib.Controller, cfg *nav.Config, act ButtonAction, b spacenav.Button) {
	switch act {
	case ActionFit:
		if b.Pressed {
			doFit(ctx, log, c)
		}

	case ActionMenu:
		// Mirror press and release so an application can open a menu on the
		// way down and act on the way up.
		prop := navlib.PropEventsKeyPress
		if !b.Pressed {
			prop = navlib.PropEventsKeyRelease
		}
		if err := c.Update(ctx, prop, navlib.V3DKMenu); err != nil {
			if wamp.IsUnsupported(err) {
				log.Debug("client does not handle V3DK keys", "prop", prop)
			} else if ctx.Err() == nil {
				log.Debug("could not send V3DK key", "prop", prop, "err", err)
			}
		}

	case ActionToggleDominant:
		if b.Pressed {
			cfg.DominantAxis = !cfg.DominantAxis
			log.Info("dominant-axis filtering", "on", cfg.DominantAxis)
		}

	case ActionToggleRotation:
		if b.Pressed {
			cfg.EnableRotation = !cfg.EnableRotation
			log.Info("rotation", "enabled", cfg.EnableRotation)
		}

	case ActionNone:
		// Explicitly mapped to nothing; stay quiet.

	default:
		if b.Pressed {
			log.Info("unmapped button", "id", b.ID,
				"hint", "map it with -buttons, e.g. -buttons 0=fit,1=menu")
		}
	}
}

// doFit frames the model, the way a driver's Fit command does. We compute it
// rather than asking the client to, so it works with any application.
func doFit(ctx context.Context, log *slog.Logger, c *navlib.Controller) {
	cur, err := c.ReadMatrix4(ctx, navlib.PropViewAffine)
	if err != nil {
		log.Debug("fit: cannot read view.affine", "err", err)
		return
	}
	box, err := c.ReadBox(ctx, navlib.PropModelExtents)
	if err != nil {
		log.Info("fit: the client does not report model.extents; nothing to frame")
		return
	}

	s := nav.Scene{Camera: cur.Canonical(), ModelExtents: box, Perspective: true}
	if v, err := c.ReadBool(ctx, navlib.PropViewPerspective); err == nil {
		s.Perspective = v
	}
	if !s.Perspective {
		if b, err := c.ReadBox(ctx, navlib.PropViewExtents); err == nil {
			s.ViewExtents = b
		}
	}
	fov := 45 * math.Pi / 180
	if v, err := c.ReadFloat(ctx, navlib.PropViewFOV); err == nil && v > 0 {
		fov = v
	}

	res := nav.Fit(s, fov)
	if !res.Moved {
		return
	}

	log.Info("fit", "modelCentre", box.Center(), "perspective", s.Perspective)
	if res.ExtentsChanged {
		if err := c.WriteBox(ctx, navlib.PropViewExtents, res.Extents); err != nil && !wamp.IsUnsupported(err) {
			log.Debug("fit: cannot write view.extents", "err", err)
		}
	}
	if err := c.WriteMatrix4(ctx, navlib.PropViewAffine, navlib.FromCanonical(res.Camera, c.Quirks.Layout)); err != nil {
		log.Debug("fit: cannot write view.affine", "err", err)
	}
}

func modeName(m nav.Mode) string {
	if m == nav.ModeCamera {
		return "camera"
	}
	return "object"
}
