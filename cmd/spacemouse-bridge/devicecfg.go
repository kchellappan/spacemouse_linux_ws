package main

import (
	"fmt"

	"github.com/kchellappan/spacemouse_linux_ws/internal/spacenav"
	"github.com/kchellappan/spacemouse_linux_ws/internal/webui"
)

// deviceStore adapts the spacenavd configuration client to what the web UI
// needs.
//
// Every operation here changes the machine, not just this process: spacenavd
// applies these to every client it serves, and Save rewrites /etc/spnavrc.
type deviceStore struct{ dev *spacenav.Client }

func (d *deviceStore) Read() (webui.DeviceConfig, error) {
	if d.dev == nil {
		return webui.DeviceConfig{}, fmt.Errorf("the bridge is not reading a device")
	}
	if !d.dev.ConfigSupported() {
		return webui.DeviceConfig{}, spacenav.ErrConfigUnsupported
	}
	cfg, err := d.dev.ReadConfig()
	if err != nil {
		return webui.DeviceConfig{}, err
	}
	return webui.DeviceConfig{
		Supported:       true,
		Sensitivity:     cfg.Sensitivity,
		AxisSensitivity: cfg.AxisSensitivity,
		Deadzone:        cfg.Deadzone,
		Invert:          cfg.Invert,
		SwapYZ:          cfg.SwapYZ,
	}, nil
}

func (d *deviceStore) Write(in webui.DeviceConfig) error {
	if d.dev == nil || !d.dev.ConfigSupported() {
		return spacenav.ErrConfigUnsupported
	}
	if err := validateDeviceConfig(in); err != nil {
		return err
	}
	return d.dev.WriteConfig(spacenav.DeviceConfig{
		Sensitivity:     in.Sensitivity,
		AxisSensitivity: in.AxisSensitivity,
		Deadzone:        in.Deadzone,
		Invert:          in.Invert,
		SwapYZ:          in.SwapYZ,
	})
}

func (d *deviceStore) Save() error {
	if d.dev == nil || !d.dev.ConfigSupported() {
		return spacenav.ErrConfigUnsupported
	}
	return d.dev.SaveConfig()
}

func (d *deviceStore) Restore() error {
	if d.dev == nil || !d.dev.ConfigSupported() {
		return spacenav.ErrConfigUnsupported
	}
	return d.dev.RestoreConfig()
}

// validateDeviceConfig rejects values that would leave the device unusable
// for every application, not merely values that are a strange choice.
//
// A sensitivity of zero is the one that matters: spacenavd would then report
// nothing at all, in every application, and the cause would be invisible from
// inside any of them.
func validateDeviceConfig(c webui.DeviceConfig) error {
	if c.Sensitivity <= 0 {
		return fmt.Errorf("sensitivity must be greater than zero, not %v", c.Sensitivity)
	}
	if c.Sensitivity > 20 {
		return fmt.Errorf("sensitivity above 20 is not usable, got %v", c.Sensitivity)
	}
	for i, s := range c.AxisSensitivity {
		if s < 0 || s > 20 {
			return fmt.Errorf("axis %d sensitivity must be between 0 and 20, not %v", i, s)
		}
	}
	if len(c.Deadzone) != spacenav.ConfigAxes {
		return fmt.Errorf("expected %d dead zones, got %d", spacenav.ConfigAxes, len(c.Deadzone))
	}
	for i, dz := range c.Deadzone {
		// Full deflection is 350 before sensitivity, so a dead zone near it
		// silently disables the axis everywhere.
		if dz < 0 || dz > 300 {
			return fmt.Errorf("axis %d dead zone must be between 0 and 300, not %d", i, dz)
		}
	}
	return nil
}
