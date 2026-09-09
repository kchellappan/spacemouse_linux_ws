package spacenav

// Request/response protocol constants, transcribed from spacenavd's
// src/proto.h. These are enum positions, so the values below are derived by
// counting from each block's base — get one wrong and a request lands on a
// different setting, which is why they are written out explicitly rather than
// generated with iota.
//
// The protocol shares the 32-byte frame the event stream uses:
//
//	struct reqresp { int32_t type; int32_t data[7]; }
//
// A request sets type to the opcode OR'd with reqTag. The daemon echoes the
// same type back, and data[6] carries a status that is negative on failure.
// Floats cross the wire bit-cast into an int32 slot, as libspnav does.
const (
	// reqTag marks a frame as a request rather than a device event, and is
	// how the read loop tells a response from motion.
	reqTag = 0x7faa0000

	// Protocol negotiation. Sent as a bare int32, not a full frame, before
	// anything else; the daemon replies with a bare int32 whose low byte is
	// the agreed version. Configuration requests need version 1 or above.
	reqChangeProto = 0x5500
	maxProtoVer    = 1

	// Per-client settings, REQ_BASE = 0x1000.
	reqSetSens = 0x1001 // this connection only, not the machine
	reqGetSens = 0x1002

	// Device queries, 0x2000.
	reqDevName     = 0x2000
	reqDevNAxes    = 0x2002
	reqDevNButtons = 0x2003

	// Global configuration, 0x3000. These change every application on the
	// machine, not just this one.
	reqSCfgSens     = 0x3000
	reqGCfgSens     = 0x3001
	reqSCfgSensAxis = 0x3002
	reqGCfgSensAxis = 0x3003
	reqSCfgDeadzone = 0x3004
	reqGCfgDeadzone = 0x3005
	reqSCfgInvert   = 0x3006
	reqGCfgInvert   = 0x3007
	reqSCfgSwapYZ   = 0x3010
	reqGCfgSwapYZ   = 0x3011

	// Persistence. Save writes /etc/spnavrc, which the daemon does on the
	// client's behalf because the daemon runs as root. Reset discards a
	// user's tuning entirely, so it is deliberately not exposed by the UI.
	reqCfgSave    = 0x3ffe
	reqCfgRestore = 0x3fff
	reqCfgReset   = 0x4000
)

// statusSlot is where a response reports success: negative means failure.
const statusSlot = 6
