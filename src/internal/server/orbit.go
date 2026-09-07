package server

import (
	"context"
	"log/slog"
	"math"
	"time"

	"spacemouse-bridge/internal/navlib"
	"spacemouse-bridge/internal/wamp"
)

// Orbit slowly rotates the client's camera about its model centre.
//
// It is a diagnostic, not navigation: it exercises the write path — transaction
// bracketing, motion flags and view.affine writes — against a real client with
// no hardware attached. If the model visibly turns, the output half of the
// bridge is working.
//
// Like the real driver it re-reads view.affine each frame rather than
// integrating internally, so a page reload resumes from the page's own camera.
// See docs/01-architecture-and-data-model.md.
func Orbit(log *slog.Logger, degreesPerSecond float64) func(context.Context, *navlib.Controller) {
	return func(ctx context.Context, c *navlib.Controller) {
		go runOrbit(ctx, log, c, degreesPerSecond)
	}
}

func runOrbit(ctx context.Context, log *slog.Logger, c *navlib.Controller, degPerSec float64) {
	const fps = 30
	tick := time.NewTicker(time.Second / fps)
	defer tick.Stop()

	pivot := pivotFor(ctx, log, c)
	log.Info("orbit demo starting", "pivot", pivot, "degreesPerSecond", degPerSec)

	if err := c.SetMotion(ctx, true); err != nil {
		log.Error("orbit: could not set motion", "err", err)
		return
	}
	defer func() {
		// Best effort: the context is usually already cancelled here.
		stopCtx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_ = c.SetMotion(stopCtx, false)
	}()

	step := degPerSec / fps * math.Pi / 180
	var frame int64

	for {
		select {
		case <-ctx.Done():
			log.Info("orbit demo stopped", "frames", frame)
			return
		case <-tick.C:
		}

		if !c.Focus() {
			continue
		}

		cur, err := c.ReadMatrix4(ctx, navlib.PropViewAffine)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("orbit: cannot read view.affine", "err", err)
			return
		}

		next := navlib.OrbitAbout(pivot, navlib.RotateAxis([3]float64{0, 1, 0}, step)).
			Mul(cur.Canonical())

		frame++
		if err := c.BeginTransaction(ctx, frame); err != nil && !wamp.IsUnsupported(err) {
			return
		}
		if err := c.WriteMatrix4(ctx, navlib.PropViewAffine, navlib.FromCanonical(next, c.Quirks.Layout)); err != nil {
			if ctx.Err() != nil {
				return
			}
			log.Warn("orbit: cannot write view.affine", "err", err)
			return
		}
		if err := c.EndTransaction(ctx); err != nil && !wamp.IsUnsupported(err) {
			return
		}

		if frame%(fps*2) == 0 {
			log.Info("orbit", "frames", frame, "cameraAt", next.Translation())
		}
	}
}

// pivotFor picks a rotation centre: the model bounding-box centre if the page
// exposes one, else its stated pivot, else the world origin.
func pivotFor(ctx context.Context, log *slog.Logger, c *navlib.Controller) [3]float64 {
	if b, err := c.ReadBox(ctx, navlib.PropModelExtents); err == nil {
		return b.Center()
	}
	if p, err := c.ReadVec3(ctx, navlib.PropPivotPosition); err == nil {
		return p
	}
	log.Warn("orbit: no model extents or pivot; orbiting the world origin")
	return [3]float64{0, 0, 0}
}
