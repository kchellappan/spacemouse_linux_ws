package main

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/kchellappan/spacemouse_linux_ws/internal/config"
)

// flagValues carries the tuning flags so precedence lives in one readable
// place rather than being scattered through main.
type flagValues struct {
	navMode      string
	fullScale    float64
	deadzone     float64
	curve        float64
	panSpeed     float64
	rotateSpeed  float64
	zoomSpeed    float64
	dominantAxis bool
	noRotate     bool
	noTranslate  bool
	frameRate    int
	buttons      string
}

// applyFlagOverrides layers explicitly-passed flags over the settings file.
//
// Only flags the user actually typed count: a flag left at its default must
// not overwrite a calibrated value from the file, which is the whole reason
// set is threaded through rather than comparing against defaults.
func applyFlagOverrides(c *config.Config, set map[string]bool, v flagValues) {
	if set["nav-mode"] {
		c.NavMode = v.navMode
	}
	if set["full-scale"] {
		c.FullScale = v.fullScale
	}
	if set["deadzone"] {
		c.Deadzone = v.deadzone
	}
	if set["curve"] {
		c.Curve = v.curve
	}
	if set["pan-speed"] {
		c.PanSpeed = v.panSpeed
	}
	if set["rotate-speed"] {
		c.RotateSpeed = v.rotateSpeed
	}
	if set["zoom-speed"] {
		c.ZoomSpeed = v.zoomSpeed
	}
	if set["dominant-axis"] {
		c.DominantAxis = v.dominantAxis
	}
	if set["no-rotate"] {
		c.EnableRotation = !v.noRotate
	}
	if set["no-translate"] {
		c.EnableTranslation = !v.noTranslate
	}
	if set["frame-rate"] {
		c.FrameRate = v.frameRate
	}
	if set["buttons"] {
		c.Buttons = parseButtonSpec(v.buttons)
	}
}

// parseButtonSpec turns "0=fit,1=menu" into the persisted map. Validation
// happens later, in server.ParseButtons, so there is one authority on what an
// action name means.
func parseButtonSpec(spec string) map[string]string {
	out := map[string]string{}
	for _, part := range strings.Split(spec, ",") {
		id, action, ok := strings.Cut(part, "=")
		if !ok {
			continue
		}
		out[strings.TrimSpace(id)] = strings.TrimSpace(action)
	}
	return out
}

// printConfig writes the effective settings, so a support conversation can
// start from what the bridge actually believes rather than what the file says.
func printConfig(c config.Config, path string) error {
	data, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return err
	}
	fmt.Printf("// %s\n%s\n", path, data)
	return nil
}

// saveSettings writes the file and says where it went. Calibration is the one
// command that changes settings without being asked to, so it should be loud
// about it.
func saveSettings(path string, c config.Config) error {
	if err := config.Save(path, c); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "saved to %s\n", path)
	return nil
}
