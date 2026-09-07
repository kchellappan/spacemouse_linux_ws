package spacenav

import (
	"context"
	"errors"
	"fmt"
	"time"
)

// Axis identifies one of the six degrees of freedom as they arrive on the wire.
//
// Which physical motion drives which axis, and with what sign, varies by device
// model and by spacenavd configuration (invert-rot, invert-trans, swap-yz,
// axismapN). Determine it empirically with Calibrate rather than assuming.
type Axis int

const (
	AxisX Axis = iota
	AxisY
	AxisZ
	AxisRX
	AxisRY
	AxisRZ
)

// Axes lists every axis in wire order.
var Axes = []Axis{AxisX, AxisY, AxisZ, AxisRX, AxisRY, AxisRZ}

func (a Axis) String() string {
	switch a {
	case AxisX:
		return "x"
	case AxisY:
		return "y"
	case AxisZ:
		return "z"
	case AxisRX:
		return "rx"
	case AxisRY:
		return "ry"
	case AxisRZ:
		return "rz"
	}
	return fmt.Sprintf("axis(%d)", int(a))
}

// MaxMagnitude returns the largest absolute axis value.
func (m Motion) MaxMagnitude() int32 {
	var max int32
	for _, a := range Axes {
		if v := abs32(m.Get(a)); v > max {
			max = v
		}
	}
	return max
}

// AtRest reports whether every axis sits within threshold of zero.
//
// Prefer this to Zero for "has the user let go": a real puck settles with a
// small residual offset rather than at exact zero, so an exact test can wait
// forever. A SpaceMouse Compact was measured resting at up to 11 units against
// a full deflection of 279.
func (m Motion) AtRest(threshold int32) bool {
	return m.MaxMagnitude() <= threshold
}

// Get returns the value of one axis.
func (m Motion) Get(a Axis) int32 {
	switch a {
	case AxisX:
		return m.X
	case AxisY:
		return m.Y
	case AxisZ:
		return m.Z
	case AxisRX:
		return m.RX
	case AxisRY:
		return m.RY
	case AxisRZ:
		return m.RZ
	}
	return 0
}

func abs32(v int32) int32 {
	if v < 0 {
		return -v
	}
	return v
}

// Dominant ranks the axes by magnitude and reports the winner, the runner-up,
// and the sign of the winner. A large gap means the gesture was clean; a small
// gap means axes were mixed, and knowing *which* axis came second is what
// makes a mixed reading interpretable — cross-talk from a physically adjacent
// degree of freedom is expected, cross-talk from an unrelated one is not.
func (m Motion) Dominant() Deflection {
	var d Deflection
	for _, a := range Axes {
		v := abs32(m.Get(a))
		switch {
		case v > d.Peak:
			d.RunnerUpAxis, d.RunnerUp = d.Axis, d.Peak
			d.Axis, d.Peak = a, v
		case v > d.RunnerUp:
			d.RunnerUpAxis, d.RunnerUp = a, v
		}
	}
	d.Sign = 1
	if m.Get(d.Axis) < 0 {
		d.Sign = -1
	}
	return d
}

// Deflection describes one observed gesture.
type Deflection struct {
	Axis         Axis
	Sign         int   // +1 or -1
	Peak         int32 // magnitude of the dominant axis
	RunnerUpAxis Axis  // the next largest axis
	RunnerUp     int32 // its magnitude
}

// Clean reports whether the dominant axis clearly won, so the reading can be
// trusted as identifying a single degree of freedom.
func (d Deflection) Clean() bool {
	return d.RunnerUp*2 < d.Peak
}

func (d Deflection) String() string {
	s := "+"
	if d.Sign < 0 {
		s = "-"
	}
	if d.RunnerUp == 0 {
		return fmt.Sprintf("%s%s (peak %d, nothing else)", s, d.Axis, d.Peak)
	}
	return fmt.Sprintf("%s%s (peak %d, next %s at %d)",
		s, d.Axis, d.Peak, d.RunnerUpAxis, d.RunnerUp)
}

// DetectOptions tunes gesture detection.
type DetectOptions struct {
	// Threshold is the magnitude that counts as a deliberate push.
	Threshold int32
	// Settle is how long to keep sampling after the threshold is crossed, to
	// find the true peak rather than the first qualifying sample.
	Settle time.Duration
	// CentreTimeout bounds the wait for the puck to return to rest.
	CentreTimeout time.Duration
	// RestThreshold is the magnitude below which the puck counts as released.
	// Set it above the device's resting noise floor; see MeasureNoiseFloor.
	// Note the floor measured at true rest underestimates how far the puck
	// settles just after a deflection, so leave headroom.
	RestThreshold int32
	// DetectTimeout bounds the wait for a deflection. Zero means wait forever.
	DetectTimeout time.Duration
}

// DefaultDetectOptions are tuned for a SpaceMouse Compact with spacenavd's
// default deadzone.
var DefaultDetectOptions = DetectOptions{
	Threshold:     40,
	Settle:        300 * time.Millisecond,
	CentreTimeout: 15 * time.Second,
	RestThreshold: 15,
	DetectTimeout: 20 * time.Second,
}

// ErrNoDeflection is returned when no deliberate push arrived before
// DetectTimeout elapsed.
var ErrNoDeflection = errors.New("no deflection detected")

// pollInterval samples the latched state. The device streams at ~125 Hz
// (8 ms), so this is comfortably faster than new data arrives.
const pollInterval = 4 * time.Millisecond

// WaitCentred blocks until the puck rests at zero on every axis.
//
// It polls the latched state rather than reading the event channel: the latch
// is always current, whereas a channel can hold a backlog of stale events
// under load.
func WaitCentred(ctx context.Context, c *Client, timeout time.Duration, threshold int32) error {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	deadline := time.After(timeout)

	for {
		if c.State().AtRest(threshold) {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-deadline:
			return fmt.Errorf("puck did not settle below %d within %s (still reading %+v); "+
				"if it never settles, raise dead-zone in /etc/spnavrc",
				threshold, timeout, c.State())
		case <-tick.C:
		}
	}
}

// DetectDeflection waits for a deliberate push and reports which axis moved.
//
// Like WaitCentred it samples the latch, so it neither consumes nor is
// confused by the event channel. onSample, if non-nil, is called with the
// running peak magnitude so a caller can show live feedback.
func DetectDeflection(ctx context.Context, c *Client, o DetectOptions, onSample func(peak int32)) (Deflection, error) {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()

	var best Motion
	var bestPeak int32
	var settleUntil time.Time

	var giveUp <-chan time.Time
	if o.DetectTimeout > 0 {
		t := time.NewTimer(o.DetectTimeout)
		defer t.Stop()
		giveUp = t.C
	}

	for {
		m := c.State()
		if peak := m.Dominant().Peak; peak > bestPeak {
			best, bestPeak = m, peak
			if onSample != nil {
				onSample(bestPeak)
			}
		}

		if bestPeak >= o.Threshold {
			if settleUntil.IsZero() {
				settleUntil = time.Now().Add(o.Settle)
			} else if time.Now().After(settleUntil) {
				return best.Dominant(), nil
			}
		}

		select {
		case <-ctx.Done():
			return Deflection{}, ctx.Err()
		case <-giveUp:
			return Deflection{}, ErrNoDeflection
		case <-tick.C:
		}
	}
}

// MeasureNoiseFloor samples the puck at rest and returns the largest magnitude
// observed. Call it with the user's hand off the device.
//
// The result is the floor below which motion must be ignored: a SpaceMouse
// that rests at 11 units would otherwise drift the camera continuously.
func MeasureNoiseFloor(ctx context.Context, c *Client, d time.Duration) (int32, error) {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	deadline := time.After(d)

	var floor int32
	for {
		select {
		case <-ctx.Done():
			return floor, ctx.Err()
		case <-deadline:
			return floor, nil
		case <-tick.C:
			if v := c.State().MaxMagnitude(); v > floor {
				floor = v
			}
		}
	}
}
