// Package config persists the settings a user tunes, so they survive a
// restart and reach the packaged service.
//
// Until this existed every knob was a command-line flag and the systemd unit
// passed none of them, so calibration measured a device's full scale, printed
// it, and threw it away — a SpaceMouse reading 216 at full deflection ran
// against the built-in 350 forever. Settings that cannot outlive a process are
// not settings.
//
// The file is JSON because the web UI will read and write it as JSON anyway,
// and because staying on the standard library keeps the single static binary.
// The cost is no comments, so Save always writes every field: the file
// documents itself by example.
package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/kchellappan/spacemouse_linux_ws/internal/nav"
)

// Version is the schema version, so a later change can migrate rather than
// guess at what an old file meant.
//
// 2 split rotationFullScale out of fullScale. A version 1 file is migrated on
// load by copying fullScale into it, which is what it effectively meant.
const Version = 2

// Config is the persisted form. It deliberately mirrors what a user can tune
// rather than nav.Config exactly: navigation internals should be free to move
// without breaking everyone's file.
type Config struct {
	Version int `json:"version"`

	// NavMode is "object" (the model follows the cap) or "camera".
	NavMode string `json:"navMode"`

	// FullScale and RotationFullScale are the device magnitudes at full
	// deflection, measured by -calibrate. They are separate because sliding
	// the cap and tipping it do not produce the same range. NoiseFloor is
	// recorded alongside for diagnosis; nothing reads it yet.
	FullScale         float64 `json:"fullScale"`
	RotationFullScale float64 `json:"rotationFullScale"`
	NoiseFloor        float64 `json:"noiseFloor"`

	Deadzone float64 `json:"deadzone"`
	Curve    float64 `json:"curve"`

	PanSpeed    float64 `json:"panSpeed"`
	RotateSpeed float64 `json:"rotateSpeed"`
	ZoomSpeed   float64 `json:"zoomSpeed"`

	DominantAxis      bool `json:"dominantAxis"`
	EnableTranslation bool `json:"enableTranslation"`
	EnableRotation    bool `json:"enableRotation"`

	FrameRate int `json:"frameRate"`

	// Buttons maps a device button id to an action name, e.g. {"0": "fit"}.
	// JSON object keys are strings whatever the type says.
	Buttons map[string]string `json:"buttons"`
}

// Default returns the built-in settings. FullScale is a typical SpaceMouse
// Compact reading, not a measurement of the user's device — -calibrate
// replaces it.
func Default() Config {
	n := nav.DefaultConfig()
	return Config{
		Version:           Version,
		NavMode:           "object",
		FullScale:         n.FullScale,
		RotationFullScale: n.RotationFullScale,
		NoiseFloor:        0,
		Deadzone:          n.Deadzone,
		Curve:             n.Exponent,
		PanSpeed:          n.TranslationSpeed,
		RotateSpeed:       n.RotationSpeed,
		ZoomSpeed:         n.ZoomSpeed,
		DominantAxis:      n.DominantAxis,
		EnableTranslation: n.EnableTranslation,
		EnableRotation:    n.EnableRotation,
		FrameRate:         60,
		Buttons:           map[string]string{"0": "fit", "1": "menu"},
	}
}

// DefaultPath is $XDG_CONFIG_HOME/spacemouse-bridge/config.json, or
// ~/.config/spacemouse-bridge/config.json.
func DefaultPath() (string, error) {
	if d := os.Getenv("XDG_CONFIG_HOME"); d != "" {
		return filepath.Join(d, "spacemouse-bridge", "config.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating home directory: %w", err)
	}
	return filepath.Join(home, ".config", "spacemouse-bridge", "config.json"), nil
}

// Load reads path, filling anything absent from the defaults. A missing file
// is not an error: it is the ordinary state before the first calibration.
//
// Unmarshalling *over* the defaults rather than into a zero value is what
// makes a partial file behave: keys the file omits are left untouched, so an
// entry setting only fullScale does not silently turn rotation off.
func Load(path string) (Config, error) {
	c := Default()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return c, nil
		}
		return c, fmt.Errorf("reading %s: %w", path, err)
	}
	if err := json.Unmarshal(data, &c); err != nil {
		return Default(), fmt.Errorf("parsing %s: %w", path, err)
	}
	return migrate(c), nil
}

// migrate brings an older file forward. Version 1 had a single fullScale
// covering both halves, so that is what its rotation scale was.
func migrate(c Config) Config {
	if c.Version < 2 {
		c.RotationFullScale = c.FullScale
	}
	c.Version = Version
	return c
}

// Save writes the complete configuration, creating the directory as needed.
//
// The write goes to a temporary file and is renamed into place, so an
// interrupted save cannot leave a half-written file that the next start
// refuses to parse.
func Save(path string, c Config) error {
	c.Version = Version

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("creating %s: %w", dir, err)
	}

	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')

	tmp, err := os.CreateTemp(dir, ".config-*.json")
	if err != nil {
		return fmt.Errorf("creating a temporary file in %s: %w", dir, err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

// Nav converts to the navigation model's own configuration.
func (c Config) Nav() nav.Config {
	n := nav.DefaultConfig()
	n.FullScale = c.FullScale
	n.RotationFullScale = c.RotationFullScale
	n.Deadzone = c.Deadzone
	n.Exponent = c.Curve
	n.TranslationSpeed = c.PanSpeed
	n.RotationSpeed = c.RotateSpeed
	n.ZoomSpeed = c.ZoomSpeed
	n.DominantAxis = c.DominantAxis
	n.EnableTranslation = c.EnableTranslation
	n.EnableRotation = c.EnableRotation
	if c.NavMode == "camera" {
		n.Mode = nav.ModeCamera
	}
	return n
}

// ButtonSpec renders the button map in the form -buttons accepts, so there is
// one parser rather than two.
func (c Config) ButtonSpec() string {
	if len(c.Buttons) == 0 {
		return ""
	}
	// Sorted, so the string is stable across runs and diffs are readable.
	ids := make([]string, 0, len(c.Buttons))
	for id := range c.Buttons {
		ids = append(ids, id)
	}
	// Numerically where possible, so "10" follows "9" rather than "1".
	sort.Slice(ids, func(i, j int) bool {
		a, aerr := strconv.Atoi(ids[i])
		b, berr := strconv.Atoi(ids[j])
		if aerr == nil && berr == nil {
			return a < b
		}
		return ids[i] < ids[j]
	})

	spec := ""
	for _, id := range ids {
		if spec != "" {
			spec += ","
		}
		spec += id + "=" + c.Buttons[id]
	}
	return spec
}
