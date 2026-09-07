package server

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/kchellappan/spacemouse_linux_ws/internal/navlib"
	"github.com/kchellappan/spacemouse_linux_ws/internal/wamp"
)

// Probe reads the client's navlib properties and reports what it implements.
// It exercises the full server-to-client RPC path, so a clean run proves the
// protocol layer end to end.
//
// Safe to use directly as Options.OnReady — it starts its own goroutine.
func Probe(log *slog.Logger) func(context.Context, *navlib.Controller) {
	return func(ctx context.Context, c *navlib.Controller) {
		go runProbe(ctx, log, c)
	}
}

func runProbe(ctx context.Context, log *slog.Logger, c *navlib.Controller) {
	ctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	log.Info("probing client properties",
		"client", c.Info.Name,
		"clientVersion", c.Info.Version,
		"layout", c.Quirks.Layout.String())

	report := func(prop string, v any, err error) {
		switch {
		case err == nil:
			log.Info("  ok", "prop", prop, "value", v)
		case wamp.IsUnsupported(err):
			log.Info("  unsupported", "prop", prop)
		default:
			log.Warn("  failed", "prop", prop, "err", err)
		}
	}

	if v, err := c.ReadBool(ctx, navlib.PropViewPerspective); err == nil {
		report(navlib.PropViewPerspective, v, nil)
	} else {
		report(navlib.PropViewPerspective, nil, err)
	}

	if m, err := c.ReadMatrix4(ctx, navlib.PropViewAffine); err == nil {
		report(navlib.PropViewAffine, fmt.Sprintf("translation=%v", m.Translation()), nil)
	} else {
		report(navlib.PropViewAffine, nil, err)
	}

	if b, err := c.ReadBox(ctx, navlib.PropModelExtents); err == nil {
		report(navlib.PropModelExtents,
			fmt.Sprintf("center=%v diagonal=%.3f", b.Center(), b.Diagonal()), nil)
	} else {
		report(navlib.PropModelExtents, nil, err)
	}

	for _, p := range []string{navlib.PropViewExtents, navlib.PropSelectionExtents} {
		v, err := c.ReadBox(ctx, p)
		report(p, v, err)
	}
	for _, p := range []string{navlib.PropViewRotatable, navlib.PropSelectionEmpty} {
		v, err := c.ReadBool(ctx, p)
		report(p, v, err)
	}
	if v, err := c.ReadFloat(ctx, navlib.PropViewFOV); err == nil {
		report(navlib.PropViewFOV, fmt.Sprintf("%.4f rad (%.1f deg)", v, v*180/3.14159265), nil)
	} else {
		report(navlib.PropViewFOV, nil, err)
	}
	for _, p := range []string{navlib.PropPivotPosition, navlib.PropViewTarget} {
		v, err := c.ReadVec3(ctx, p)
		report(p, v, err)
	}
	for _, p := range []string{navlib.PropCoordinateSystem, navlib.PropViewsFront} {
		m, err := c.ReadMatrix4(ctx, p)
		if err == nil {
			report(p, m.M, nil)
		} else {
			report(p, nil, err)
		}
	}

	log.Info("probe complete — protocol layer verified")
}
