package spacenav

import (
	"encoding/binary"
	"fmt"
	"math"
	"time"
)

// DeviceConfig is spacenavd's own configuration — the settings shared by every
// application on the machine, not the bridge's per-application tuning.
type DeviceConfig struct {
	// Sensitivity multiplies every motion value. Full deflection reports
	// 350 x this, with no clamp, so raising it moves where the device
	// saturates. See docs/05-spacenavd.md.
	Sensitivity float64 `json:"sensitivity"`

	// AxisSensitivity is per axis, in device order: three translation then
	// three rotation.
	AxisSensitivity [6]float64 `json:"axisSensitivity"`

	// Deadzone is per axis, in raw device units. Always ConfigAxes long.
	Deadzone []int32 `json:"deadzone"`

	// Invert reverses an axis, in the same order as AxisSensitivity.
	Invert [6]bool `json:"invert"`

	// SwapYZ exchanges the Y and Z axes, for devices mounted differently.
	SwapYZ bool `json:"swapYZ"`
}

// ConfigSupported reports whether the daemon negotiated a protocol new enough
// to answer configuration requests. Older daemons speak only the event
// stream, and every config call returns ErrConfigUnsupported.
func (c *Client) ConfigSupported() bool { return c.proto >= 1 }

// ErrConfigUnsupported is returned when the daemon predates the configuration
// protocol.
var ErrConfigUnsupported = fmt.Errorf("this spacenavd does not support configuration over its socket (needs protocol 1 or later)")

// requestTimeout bounds a single exchange. The daemon answers a local socket
// immediately or not at all.
const requestTimeout = 2 * time.Second

// request sends one request frame and waits for the matching response.
//
// The read loop routes responses here rather than decoding them as motion,
// and requests are serialised by reqMu so two callers cannot interleave and
// take each other's replies — the protocol has no sequence numbers, only the
// echoed type.
func (c *Client) request(op int32, data ...int32) ([7]int32, error) {
	var out [7]int32
	if !c.ConfigSupported() {
		return out, ErrConfigUnsupported
	}

	c.reqMu.Lock()
	defer c.reqMu.Unlock()

	var frame [eventSize]byte
	binary.LittleEndian.PutUint32(frame[0:4], uint32(op|reqTag))
	for i, v := range data {
		if i >= 7 {
			break
		}
		binary.LittleEndian.PutUint32(frame[4+i*4:8+i*4], uint32(v))
	}

	// Drain any straggler before sending, so a reply left over from a
	// timed-out earlier request cannot be mistaken for this one's.
	select {
	case <-c.responses:
	default:
	}

	if _, err := c.conn.Write(frame[:]); err != nil {
		return out, fmt.Errorf("sending request %#x: %w", op, err)
	}

	select {
	case resp := <-c.responses:
		if resp.op != op|reqTag {
			return out, fmt.Errorf("request %#x got a reply for %#x", op, resp.op&^reqTag)
		}
		if resp.data[statusSlot] < 0 {
			return out, fmt.Errorf("spacenavd refused request %#x (status %d)", op, resp.data[statusSlot])
		}
		return resp.data, nil
	case <-time.After(requestTimeout):
		return out, fmt.Errorf("spacenavd did not answer request %#x within %s", op, requestTimeout)
	case <-c.dead:
		return out, fmt.Errorf("connection to spacenavd ended while waiting for %#x", op)
	}
}

// floatBits and bitsFloat move a float through an int32 slot the way libspnav
// does: a bit cast, not a scaled integer.
func floatBits(f float64) int32 { return int32(math.Float32bits(float32(f))) }
func bitsFloat(b int32) float64 { return float64(math.Float32frombits(uint32(b))) }
func boolInt(b bool) int32 {
	if b {
		return 1
	}
	return 0
}
func intBool(v int32) bool { return v != 0 }

// NumAxes reports how many axes the device exposes.
//
// spacenavd 1.2 does not implement this request — the REQ_DEV_* block exists
// only in later sources — so callers must tolerate an error rather than treat
// it as fatal. ReadConfig discovers the count by probing instead.
func (c *Client) NumAxes() (int, error) {
	resp, err := c.request(reqDevNAxes)
	if err != nil {
		return 0, err
	}
	return int(resp[0]), nil
}

// ConfigAxes is how many axes the configuration covers: three translation then
// three rotation, matching the fixed six slots that sensitivity and inversion
// already use.
//
// It is a constant rather than a discovered value because neither mechanism
// for discovering it works. REQ_DEV_NAXES is unimplemented on spacenavd 1.2,
// and probing dead zones cannot find the end either: measured against a real
// daemon, axes past the device's actual count answer 0 rather than refusing,
// so a probe returns however many it is willing to ask for. Six is what every
// 3Dconnexion device has and what the rest of the protocol assumes.
const ConfigAxes = 6

// readDeadzones reads the per-axis dead zones.
func (c *Client) readDeadzones() ([]int32, error) {
	out := make([]int32, ConfigAxes)
	for axis := 0; axis < ConfigAxes; axis++ {
		resp, err := c.request(reqGCfgDeadzone, int32(axis))
		if err != nil {
			return nil, err
		}
		out[axis] = resp[1]
	}
	return out, nil
}

// ReadConfig fetches spacenavd's current global configuration.
func (c *Client) ReadConfig() (DeviceConfig, error) {
	var cfg DeviceConfig

	resp, err := c.request(reqGCfgSens)
	if err != nil {
		return cfg, err
	}
	cfg.Sensitivity = bitsFloat(resp[0])

	if resp, err = c.request(reqGCfgSensAxis); err != nil {
		return cfg, err
	}
	for i := range cfg.AxisSensitivity {
		cfg.AxisSensitivity[i] = bitsFloat(resp[i])
	}

	if resp, err = c.request(reqGCfgInvert); err != nil {
		return cfg, err
	}
	for i := range cfg.Invert {
		cfg.Invert[i] = intBool(resp[i])
	}

	if resp, err = c.request(reqGCfgSwapYZ); err != nil {
		return cfg, err
	}
	cfg.SwapYZ = intBool(resp[0])

	// Dead zones are addressed per device axis and read one at a time.
	if cfg.Deadzone, err = c.readDeadzones(); err != nil {
		return cfg, err
	}
	return cfg, nil
}

// WriteConfig applies a configuration to the daemon.
//
// It does not persist: spacenavd keeps it in memory until SaveConfig writes
// /etc/spnavrc. That split is deliberate, so a slider can be tried and
// abandoned without leaving the machine changed.
func (c *Client) WriteConfig(cfg DeviceConfig) error {
	if _, err := c.request(reqSCfgSens, floatBits(cfg.Sensitivity)); err != nil {
		return err
	}

	axis := make([]int32, 6)
	for i, v := range cfg.AxisSensitivity {
		axis[i] = floatBits(v)
	}
	if _, err := c.request(reqSCfgSensAxis, axis...); err != nil {
		return err
	}

	inv := make([]int32, 6)
	for i, v := range cfg.Invert {
		inv[i] = boolInt(v)
	}
	if _, err := c.request(reqSCfgInvert, inv...); err != nil {
		return err
	}

	if _, err := c.request(reqSCfgSwapYZ, boolInt(cfg.SwapYZ)); err != nil {
		return err
	}

	for i, dz := range cfg.Deadzone {
		if _, err := c.request(reqSCfgDeadzone, int32(i), dz); err != nil {
			return err
		}
	}
	return nil
}

// SaveConfig writes the daemon's current settings to /etc/spnavrc.
//
// The daemon performs the write, because it runs as root and the caller does
// not. This is how spnavcfg persists changes, so it is sanctioned rather than
// a privilege trick — but it does mean a web page can cause a root-owned
// system file to be rewritten, which is why it is a separate, explicit call.
func (c *Client) SaveConfig() error {
	_, err := c.request(reqCfgSave)
	return err
}

// RestoreConfig discards unsaved changes by reloading /etc/spnavrc.
func (c *Client) RestoreConfig() error {
	_, err := c.request(reqCfgRestore)
	return err
}
