package config

import (
	"fmt"
	"sync"
)

// Live holds the settings in use, so the drive loop and the web UI share one
// source of truth rather than each keeping a copy that drifts.
//
// The drive loop reads this every frame. That is what makes a slider in the
// UI change the feel with the puck still in your hand, which is the whole
// reason for tuning in a browser rather than editing a file.
type Live struct {
	mu  sync.RWMutex
	cfg Config
}

// NewLive returns a holder seeded with c.
func NewLive(c Config) *Live { return &Live{cfg: c} }

// Get returns the current settings.
func (l *Live) Get() Config {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.cfg
}

// Set replaces the settings after validating them. An invalid update is
// rejected whole: applying the good half would leave the device in a state
// the user never asked for and cannot see.
func (l *Live) Set(c Config) error {
	if err := c.Validate(); err != nil {
		return err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	// Preserve calibration: it is measured, not chosen, and the tuning UI
	// has no business overwriting it with whatever the page last rendered.
	c.CalibratedAt = l.cfg.CalibratedAt
	c.Version = Version
	l.cfg = c
	return nil
}

// Update applies a change made in-process, such as a button toggling
// dominant-axis filtering, without the validation a request needs.
func (l *Live) Update(f func(*Config)) Config {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(&l.cfg)
	return l.cfg
}

// Validate reports whether the settings are usable.
//
// The bounds are deliberately wide: this rejects values that would break
// navigation outright, not values that are merely a strange choice. Someone
// who wants an absurdly fast pan is entitled to it.
func (c Config) Validate() error {
	if c.NavMode != "object" && c.NavMode != "camera" {
		return fmt.Errorf("navMode must be object or camera, not %q", c.NavMode)
	}
	if c.FullScale <= 0 {
		return fmt.Errorf("fullScale must be positive, not %v", c.FullScale)
	}
	if c.RotationFullScale < 0 {
		return fmt.Errorf("rotationFullScale cannot be negative, not %v", c.RotationFullScale)
	}
	// A dead zone at or above 1 discards every reading the device can
	// produce, leaving a puck that does nothing and no clue why.
	if c.Deadzone < 0 || c.Deadzone >= 1 {
		return fmt.Errorf("deadzone must be at least 0 and below 1, not %v", c.Deadzone)
	}
	if c.Curve <= 0 {
		return fmt.Errorf("curve must be positive, not %v", c.Curve)
	}
	for name, v := range map[string]float64{
		"panSpeed": c.PanSpeed, "rotateSpeed": c.RotateSpeed, "zoomSpeed": c.ZoomSpeed,
	} {
		if v < 0 {
			return fmt.Errorf("%s cannot be negative, not %v", name, v)
		}
	}
	if c.FrameRate < 1 || c.FrameRate > 240 {
		return fmt.Errorf("frameRate must be between 1 and 240, not %d", c.FrameRate)
	}
	return nil
}
