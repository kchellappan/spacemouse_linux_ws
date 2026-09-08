package main

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/kchellappan/spacemouse_linux_ws/internal/config"
)

func newUIState(t *testing.T) *uiState {
	t.Helper()
	return &uiState{
		live:    config.NewLive(config.Default()),
		cfgPath: filepath.Join(t.TempDir(), "config.json"),
		log:     slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})),
	}
}

// A page sends only what changed. Parsing into a zero value would clear
// everything it left out.
func TestApplySettingsMergesOverWhatIsInForce(t *testing.T) {
	u := newUIState(t)
	before := u.live.Get()

	if err := u.applySettings([]byte(`{"panSpeed": 2.5}`), false); err != nil {
		t.Fatalf("applySettings: %v", err)
	}

	got := u.live.Get()
	if got.PanSpeed != 2.5 {
		t.Errorf("panSpeed = %v, want 2.5", got.PanSpeed)
	}
	if got.RotateSpeed != before.RotateSpeed || got.FullScale != before.FullScale {
		t.Errorf("untouched fields changed: %+v", got)
	}
	if !got.EnableRotation {
		t.Error("rotation was disabled by an update that never mentioned it")
	}
}

// Regression: assigning the struct shares the button map, and json.Unmarshal
// merges into an existing map rather than replacing it. Decoding a rejected
// update therefore mutated the settings in force — permanently, and
// invisibly, so every later change failed for a reason the user could not
// see.
func TestRejectedUpdateLeavesButtonsUntouched(t *testing.T) {
	u := newUIState(t)
	before := u.live.Get().ButtonSpec()

	if err := u.applySettings([]byte(`{"buttons": {"0": "explode"}}`), false); err == nil {
		t.Fatal("an unknown button action was accepted")
	}
	if after := u.live.Get().ButtonSpec(); after != before {
		t.Errorf("buttons became %q after a rejected update, want %q", after, before)
	}

	// And the next legitimate change must still work.
	if err := u.applySettings([]byte(`{"panSpeed": 1.4}`), false); err != nil {
		t.Fatalf("a later valid update failed: %v", err)
	}
}

func TestApplySettingsRejectsInvalidValues(t *testing.T) {
	for _, body := range []string{
		`{"deadzone": 1.5}`,
		`{"navMode": "sideways"}`,
		`{"frameRate": 9999}`,
		`{"fullScale": 0}`,
		`{"buttons": {"0": "explode"}}`,
		`not json at all`,
	} {
		u := newUIState(t)
		if err := u.applySettings([]byte(body), false); err == nil {
			t.Errorf("%s was accepted", body)
		}
	}
}

// Live apply and saving are separate so a user can feel a slider and still
// walk away without having changed anything on disk.
func TestApplyWithoutPersistDoesNotWriteTheFile(t *testing.T) {
	u := newUIState(t)

	if err := u.applySettings([]byte(`{"panSpeed": 2.5}`), false); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(u.cfgPath); !os.IsNotExist(err) {
		t.Error("a live-only change wrote the settings file")
	}

	if err := u.applySettings([]byte(`{"panSpeed": 1.4}`), true); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(u.cfgPath)
	if err != nil {
		t.Fatalf("persisting did not write the file: %v", err)
	}
	var saved config.Config
	if err := json.Unmarshal(data, &saved); err != nil {
		t.Fatal(err)
	}
	if saved.PanSpeed != 1.4 {
		t.Errorf("saved panSpeed = %v, want 1.4", saved.PanSpeed)
	}
}

// The tuning UI round-trips the whole document, so it will send back whatever
// it rendered. It must not be able to erase a measurement that way.
func TestApplySettingsCannotClearCalibration(t *testing.T) {
	u := newUIState(t)
	u.live.Update(func(c *config.Config) {
		c.CalibratedAt = config.Default().CalibratedAt
	})
	stamped := u.live.Get()
	if stamped.Calibrated() {
		t.Skip("defaults unexpectedly carry a calibration stamp")
	}

	// Give it one, then try to clear it through the API.
	u.live.Update(func(c *config.Config) { c.FullScale = 216 })
	u.live.Update(func(c *config.Config) { c.CalibratedAt = time.Now() })

	if err := u.applySettings([]byte(`{"calibratedAt": "0001-01-01T00:00:00Z"}`), false); err != nil {
		t.Fatalf("applySettings: %v", err)
	}
	if !u.live.Get().Calibrated() {
		t.Error("the API cleared the calibration stamp")
	}
}
