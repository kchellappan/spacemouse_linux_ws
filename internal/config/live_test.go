package config

import (
	"testing"
	"time"
)

func TestLiveGetReturnsWhatWasSet(t *testing.T) {
	l := NewLive(Default())
	c := Default()
	c.PanSpeed = 2.5

	if err := l.Set(c); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got := l.Get().PanSpeed; got != 2.5 {
		t.Errorf("panSpeed = %v, want 2.5", got)
	}
}

// Calibration is measured, not chosen. A tuning UI that round-trips the whole
// document must not be able to clear it by sending back what it rendered.
func TestSetPreservesCalibration(t *testing.T) {
	seed := Default()
	seed.CalibratedAt = time.Date(2026, 9, 7, 11, 25, 0, 0, time.UTC)
	seed.FullScale = 350
	l := NewLive(seed)

	incoming := Default() // no calibration stamp at all
	incoming.PanSpeed = 1.2
	if err := l.Set(incoming); err != nil {
		t.Fatalf("Set: %v", err)
	}

	got := l.Get()
	if !got.CalibratedAt.Equal(seed.CalibratedAt) {
		t.Errorf("calibratedAt = %v, want the preserved %v", got.CalibratedAt, seed.CalibratedAt)
	}
	if got.PanSpeed != 1.2 {
		t.Errorf("panSpeed = %v, want the update applied", got.PanSpeed)
	}
}

// An invalid update is rejected whole. Applying the valid half would leave the
// device in a state nobody asked for and the page could not show.
func TestSetRejectsInvalidSettingsWithoutApplyingAnything(t *testing.T) {
	l := NewLive(Default())

	bad := Default()
	bad.PanSpeed = 3.0 // fine
	bad.Deadzone = 1.5 // not
	if err := l.Set(bad); err == nil {
		t.Fatal("an out-of-range dead zone was accepted")
	}
	if got := l.Get().PanSpeed; got == 3.0 {
		t.Error("the valid half of a rejected update was applied")
	}
}

func TestValidateBounds(t *testing.T) {
	for _, tc := range []struct {
		name  string
		spoil func(*Config)
	}{
		{"unknown nav mode", func(c *Config) { c.NavMode = "sideways" }},
		{"zero full scale", func(c *Config) { c.FullScale = 0 }},
		{"negative full scale", func(c *Config) { c.FullScale = -1 }},
		// A dead zone at or above 1 discards every reading the device can
		// produce: a puck that does nothing, with no clue why.
		{"dead zone of one", func(c *Config) { c.Deadzone = 1 }},
		{"negative dead zone", func(c *Config) { c.Deadzone = -0.1 }},
		{"zero curve", func(c *Config) { c.Curve = 0 }},
		{"negative pan speed", func(c *Config) { c.PanSpeed = -1 }},
		{"frame rate of zero", func(c *Config) { c.FrameRate = 0 }},
		{"absurd frame rate", func(c *Config) { c.FrameRate = 10000 }},
	} {
		c := Default()
		tc.spoil(&c)
		if err := c.Validate(); err == nil {
			t.Errorf("%s was accepted", tc.name)
		}
	}

	if err := Default().Validate(); err != nil {
		t.Errorf("the defaults do not validate: %v", err)
	}
}

// A dead zone of exactly zero is legitimate — some users want no filtering —
// and the bound must not exclude it.
func TestValidateAllowsAZeroDeadzone(t *testing.T) {
	c := Default()
	c.Deadzone = 0
	if err := c.Validate(); err != nil {
		t.Errorf("a zero dead zone was rejected: %v", err)
	}
}

// Buttons toggle settings mid-session, and that path must not go through
// validation or a button could be refused for something it did not change.
func TestUpdateAppliesInProcessChanges(t *testing.T) {
	l := NewLive(Default())
	got := l.Update(func(c *Config) { c.DominantAxis = true })

	if !got.DominantAxis || !l.Get().DominantAxis {
		t.Error("Update did not apply the change")
	}
}

func TestConcurrentReadsAndWritesAreSafe(t *testing.T) {
	l := NewLive(Default())
	done := make(chan struct{})

	go func() {
		for i := 0; i < 500; i++ {
			c := Default()
			c.PanSpeed = float64(i%10) + 0.5
			_ = l.Set(c)
		}
		close(done)
	}()
	for i := 0; i < 500; i++ {
		_ = l.Get().Nav()
	}
	<-done
}
