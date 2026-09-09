// Package spacenav reads 6-DoF input from the spacenavd daemon over its
// native libspnav AF_UNIX protocol.
//
// We build on spacenavd rather than reading evdev directly because it handles
// device grab (without which Xorg may also treat the puck as a pointer),
// hotplug, multi-client arbitration, and per-axis deadzone/sensitivity that we
// would otherwise reimplement. See docs/05-spacenavd.md.
package spacenav

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"
)

// SocketPaths are tried in order. /var/run is a symlink to /run on modern
// systems, but both appear in the wild.
var SocketPaths = []string{"/var/run/spnav.sock", "/run/spnav.sock"}

// Event kinds on the wire.
const (
	evMotion        = 0
	evButtonPress   = 1
	evButtonRelease = 2
)

// eventSize is the fixed frame size: 8 x int32.
const eventSize = 32

// Motion is a 6-DoF deflection. Values are raw device units as filtered by
// spacenavd (deadzone and sensitivity already applied per /etc/spnavrc).
//
// Axes are a Z-up right-handed frame, verified by -calibrate on a SpaceMouse
// Compact (see docs/05-spacenavd.md):
//
//	+X  cap slides RIGHT
//	+Y  cap slides AWAY from the user, toward the screen
//	+Z  cap lifts UP
//	+RX right-handed about X: far edge LIFTS (so tipping forward is negative)
//	+RY right-handed about Y: right edge goes DOWN
//	+RZ right-handed about Z: COUNTER-clockwise seen from above
//
// The wire slot order is (x, z, y) — the second and third of each triple are
// swapped relative to the axis names, for rotation as well as translation.
type Motion struct {
	X, Y, Z    int32 // translation
	RX, RY, RZ int32 // rotation, right-handed about the matching axis
	Period     int32 // milliseconds since the previous motion event
}

// Zero reports whether the puck is centred on every axis.
func (m Motion) Zero() bool {
	return m.X == 0 && m.Y == 0 && m.Z == 0 &&
		m.RX == 0 && m.RY == 0 && m.RZ == 0
}

// Button is a button press or release.
type Button struct {
	ID      int32
	Pressed bool
}

// Event is either a Motion or a Button; exactly one field is non-nil.
type Event struct {
	Motion *Motion
	Button *Button
}

// Client is a connection to spacenavd.
type Client struct {
	conn net.Conn
	// reader buffers conn so protocol negotiation can inspect the first
	// bytes without consuming them when they turn out to be a device event.
	reader *bufio.Reader
	log    *slog.Logger
	// socket records which of the candidate paths answered, for diagnostics.
	socket string

	events chan Event

	mu     sync.RWMutex
	latest Motion

	// proto is the version the daemon agreed to at connect. Configuration
	// requests need 1 or above; 0 means an older daemon that speaks only the
	// event stream.
	proto int

	// reqMu serialises requests. The protocol correlates a reply to a request
	// only by echoing its type, so two callers in flight at once could take
	// each other's answers.
	reqMu sync.Mutex
	// responses carries frames the read loop identified as replies rather
	// than device events. Buffered by one so a late reply to a timed-out
	// request cannot block the read loop.
	responses chan response

	closeOnce sync.Once
	done      chan struct{}

	// dead is closed when the read loop stops for any reason; readErr says
	// why, and is nil when Close caused it. A caller that navigates from the
	// latch would otherwise never notice the daemon going away — the latch
	// keeps returning its last value forever.
	deadOnce sync.Once
	dead     chan struct{}
	errMu    sync.Mutex
	readErr  error

	dropped uint64
}

// response is one reply frame, kept whole so the caller can check the echoed
// type before trusting the payload.
type response struct {
	op   int32
	data [7]int32
}

// Dial connects to spacenavd. If path is empty the well-known locations are
// tried in order.
func Dial(path string, log *slog.Logger) (*Client, error) {
	if log == nil {
		log = slog.Default()
	}

	candidates := SocketPaths
	if path != "" {
		candidates = []string{path}
	}

	var lastErr error
	for _, p := range candidates {
		conn, err := net.Dial("unix", p)
		if err != nil {
			lastErr = err
			continue
		}
		log.Info("connected to spacenavd", "socket", p)
		c := &Client{
			conn:      conn,
			reader:    bufio.NewReaderSize(conn, 4096),
			log:       log,
			socket:    p,
			events:    make(chan Event, 128),
			responses: make(chan response, 1),
			done:      make(chan struct{}),
			dead:      make(chan struct{}),
		}
		// Negotiate before the read loop starts. The reply is a bare int32
		// rather than a frame, so it must not reach the frame reader — and
		// an older daemon does not reply at all, so this peeks rather than
		// reads and leaves anything it does not recognise alone.
		c.proto = negotiate(conn, c.reader, log)
		go c.readLoop()
		return c, nil
	}

	if errors.Is(lastErr, os.ErrNotExist) {
		return nil, fmt.Errorf("spacenavd socket not found (tried %v); is spacenavd installed and running? %w",
			candidates, lastErr)
	}
	return nil, fmt.Errorf("connecting to spacenavd: %w", lastErr)
}

// negotiate agrees a protocol version with the daemon.
//
// The request and the reply are bare int32 values rather than frames. A
// daemon too old to understand it simply says nothing, so a short timeout is
// the detection mechanism, and the result is protocol 0: events work,
// configuration does not.
func negotiate(conn net.Conn, r *bufio.Reader, log *slog.Logger) int {
	req := uint32(reqTag | reqChangeProto | maxProtoVer)
	var buf [4]byte
	binary.LittleEndian.PutUint32(buf[:], req)

	if err := conn.SetWriteDeadline(time.Now().Add(negotiateTimeout)); err != nil {
		return 0
	}
	if _, err := conn.Write(buf[:]); err != nil {
		log.Debug("protocol negotiation could not be sent", "err", err)
		return 0
	}
	_ = conn.SetWriteDeadline(time.Time{})

	if err := conn.SetReadDeadline(time.Now().Add(negotiateTimeout)); err != nil {
		return 0
	}
	defer conn.SetReadDeadline(time.Time{})

	// Peek, do not read. A daemon that predates negotiation says nothing and
	// may already be streaming events, and consuming four bytes of an event
	// frame would misalign every frame after it — silently, and forever.
	head, err := r.Peek(4)
	if err != nil {
		log.Debug("no protocol reply; assuming an older spacenavd", "err", err)
		return 0
	}
	v := binary.LittleEndian.Uint32(head)
	if v&0xffff0000 != uint32(reqTag) {
		log.Debug("first bytes are not a protocol reply; assuming an older spacenavd")
		return 0
	}
	_, _ = r.Discard(4)

	ver := int(v & 0xff)
	log.Debug("negotiated spacenavd protocol", "version", ver)
	return ver
}

// negotiateTimeout matches libspnav's: long enough for a local socket, short
// enough that an old daemon does not delay startup.
const negotiateTimeout = 300 * time.Millisecond

// Socket reports the path this client connected on.
func (c *Client) Socket() string { return c.socket }

// Dead is closed when the connection to spacenavd ends, whether because Close
// was called or because the daemon went away. Err distinguishes the two.
func (c *Client) Dead() <-chan struct{} { return c.dead }

// Err reports why the connection ended, or nil if it is still up or was closed
// deliberately.
func (c *Client) Err() error {
	c.errMu.Lock()
	defer c.errMu.Unlock()
	return c.readErr
}

func (c *Client) markDead(err error) {
	c.deadOnce.Do(func() {
		c.errMu.Lock()
		c.readErr = err
		c.errMu.Unlock()
		close(c.dead)
	})
}

// Events yields device events. Motion events may be dropped if the consumer
// falls behind — read State instead if you drive your own frame timer, which
// is the recommended design. Button events are best-effort but not
// deliberately dropped ahead of motion.
func (c *Client) Events() <-chan Event { return c.events }

// State returns the most recent deflection.
//
// spacenavd is event-driven: hold the puck steady off-centre and axis values
// stop changing, so events stop arriving. The 3Dconnexion driver instead runs
// a continuous frame loop. Drive navigation from this latched state on your
// own timer rather than one frame per event. See docs/05-spacenavd.md.
func (c *Client) State() Motion {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.latest
}

// Dropped reports how many events were discarded because the consumer was slow.
func (c *Client) Dropped() uint64 {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.dropped
}

func (c *Client) readLoop() {
	defer close(c.events)
	buf := make([]byte, eventSize)

	for {
		select {
		case <-c.done:
			c.markDead(nil)
			return
		default:
		}

		if _, err := io.ReadFull(c.reader, buf); err != nil {
			select {
			case <-c.done: // expected: Close was called
				c.markDead(nil)
			default:
				if errors.Is(err, net.ErrClosed) {
					c.markDead(nil)
				} else {
					c.log.Warn("spacenavd read failed", "err", err)
					c.markDead(err)
				}
			}
			return
		}

		var f [8]int32
		for i := range f {
			// spacenavd writes native-endian ints over a local socket.
			f[i] = int32(binary.LittleEndian.Uint32(buf[i*4 : i*4+4]))
		}

		// Replies share the socket and the frame size with device events,
		// and are told apart by the tag in the type field. Decoding one as
		// motion would inject a garbage deflection.
		if f[0]&reqTag == reqTag {
			select {
			case c.responses <- response{op: f[0], data: [7]int32(f[1:])}:
			default: // nobody waiting, or a stale reply: drop it
			}
			continue
		}
		c.emit(decode(f))
	}
}

// decode maps a raw frame to an Event. Slot order comes from spacenavd's
// protocol: motion is x, z, y, pitch, yaw, roll, period.
func decode(f [8]int32) Event {
	switch f[0] {
	case evMotion:
		// Slot order is (x, z, y) for translation AND rotation. Applying the
		// swap to only one of them silently exchanges yaw with roll, which
		// -calibrate on real hardware caught: "twist clockwise" landed on the
		// axis labelled RY and "tip right" on RZ.
		return Event{Motion: &Motion{
			X: f[1], Z: f[2], Y: f[3],
			RX: f[4], RZ: f[5], RY: f[6],
			Period: f[7],
		}}
	case evButtonPress:
		return Event{Button: &Button{ID: f[1], Pressed: true}}
	case evButtonRelease:
		return Event{Button: &Button{ID: f[1], Pressed: false}}
	}
	return Event{}
}

func (c *Client) emit(ev Event) {
	if ev.Motion == nil && ev.Button == nil {
		return
	}
	if ev.Motion != nil {
		c.mu.Lock()
		c.latest = *ev.Motion
		c.mu.Unlock()
	}

	select {
	case c.events <- ev:
		return
	default:
	}

	// The buffer is full. Discard the OLDEST event to make room rather than
	// dropping this one: a stale queue is worse than a short one, because a
	// consumer watching for a state change (the puck re-centring, say) would
	// otherwise be served a backlog while the event it wants is thrown away.
	select {
	case <-c.events:
	default:
	}
	select {
	case c.events <- ev:
	default:
	}

	c.mu.Lock()
	c.dropped++
	n := c.dropped
	c.mu.Unlock()
	// Not necessarily a problem: a consumer may deliberately read only the
	// latch via State() and never drain the channel.
	if n == 1 || n%10000 == 0 {
		c.log.Debug("spacenav event channel full; discarding oldest", "dropped", n)
	}
}

// Close stops reading and releases the socket.
func (c *Client) Close() error {
	var err error
	c.closeOnce.Do(func() {
		close(c.done)
		err = c.conn.Close()
	})
	return err
}

// WaitForDevice polls for the spacenavd socket, returning a connected client
// once available or an error when ctx-equivalent deadline elapses. Useful at
// startup, when the user service may race the daemon.
func WaitForDevice(path string, timeout time.Duration, log *slog.Logger) (*Client, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		c, err := Dial(path, log)
		if err == nil {
			return c, nil
		}
		lastErr = err
		time.Sleep(500 * time.Millisecond)
	}
	return nil, lastErr
}
