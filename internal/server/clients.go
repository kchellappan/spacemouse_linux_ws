package server

import (
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/kchellappan/spacemouse_linux_ws/internal/navlib"
)

// ClientInfo is what the status UI shows about one connected page.
//
// It is deliberately a snapshot rather than a live pointer: the UI reads it
// from an HTTP handler while the WAMP loop is writing, and handing out a
// pointer into session state would be a race waiting to happen.
type ClientInfo struct {
	ID          string    `json:"id"`
	Origin      string    `json:"origin"`
	ConnectedAt time.Time `json:"connectedAt"`

	// Name and LibVersion come from the create 3dcontroller handshake and are
	// empty until it arrives — a page can hold a socket open without ever
	// creating a controller.
	Name         string  `json:"name,omitempty"`
	LibVersion   float64 `json:"libVersion,omitempty"`
	MatrixLayout string  `json:"matrixLayout,omitempty"`
	FrameTiming  bool    `json:"clientFrameTiming"`
}

// Clients tracks connected pages for the status UI.
type Clients struct {
	mu   sync.RWMutex
	list map[string]*ClientInfo
	seq  atomic.Uint64
}

// NewClients returns an empty registry.
func NewClients() *Clients {
	return &Clients{list: map[string]*ClientInfo{}}
}

// Add registers a connection and returns its id.
func (c *Clients) Add(origin string) string {
	id := "conn-" + strconv.FormatUint(c.seq.Add(1), 10)

	c.mu.Lock()
	defer c.mu.Unlock()
	c.list[id] = &ClientInfo{ID: id, Origin: origin, ConnectedAt: time.Now()}
	return id
}

// Describe fills in what the controller handshake reported.
func (c *Clients) Describe(id string, info navlib.ClientInfo, layout navlib.Layout, frameTiming bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.list[id]
	if !ok {
		return
	}
	e.Name = info.Name
	e.LibVersion = info.Version
	e.MatrixLayout = layout.String()
	e.FrameTiming = frameTiming
}

// Remove deregisters a connection.
func (c *Clients) Remove(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	delete(c.list, id)
}

// Snapshot returns a copy, oldest connection first.
func (c *Clients) Snapshot() []ClientInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()

	out := make([]ClientInfo, 0, len(c.list))
	for _, e := range c.list {
		out = append(out, *e)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].ConnectedAt.Before(out[j].ConnectedAt)
	})
	return out
}
