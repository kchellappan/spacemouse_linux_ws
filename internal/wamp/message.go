// Package wamp implements the subset of WAMP v1 (https://wamp-proto.org, v1
// draft) spoken by 3Dconnexion's 3DconnexionJS client library.
//
// The protocol is quirky in two ways that matter:
//
//  1. The client never sends a HELLO. The server speaks first with WELCOME.
//  2. WAMP v1 has no server-initiated CALL, so the driver smuggles a CALL
//     message inside the payload of an EVENT published to the controller
//     topic. See Session.CallClient.
//
// See docs/02-wire-protocol.md.
package wamp

import (
	"encoding/json"
	"fmt"
)

// Type is the WAMP v1 message type, the first element of every message array.
type Type int

const (
	TypeWelcome     Type = 0
	TypePrefix      Type = 1
	TypeCall        Type = 2
	TypeCallResult  Type = 3
	TypeCallError   Type = 4
	TypeSubscribe   Type = 5
	TypeUnsubscribe Type = 6
	TypePublish     Type = 7
	TypeEvent       Type = 8
)

func (t Type) String() string {
	switch t {
	case TypeWelcome:
		return "WELCOME"
	case TypePrefix:
		return "PREFIX"
	case TypeCall:
		return "CALL"
	case TypeCallResult:
		return "CALLRESULT"
	case TypeCallError:
		return "CALLERROR"
	case TypeSubscribe:
		return "SUBSCRIBE"
	case TypeUnsubscribe:
		return "UNSUBSCRIBE"
	case TypePublish:
		return "PUBLISH"
	case TypeEvent:
		return "EVENT"
	}
	return fmt.Sprintf("UNKNOWN(%d)", int(t))
}

// Message is a decoded WAMP frame. Elements holds the raw arguments following
// the type tag, left undecoded so callers can pick their own target types.
type Message struct {
	Type     Type
	Elements []json.RawMessage
}

// Decode parses a raw WAMP frame.
func Decode(b []byte) (*Message, error) {
	var raw []json.RawMessage
	if err := json.Unmarshal(b, &raw); err != nil {
		return nil, fmt.Errorf("wamp: not a JSON array: %w", err)
	}
	if len(raw) == 0 {
		return nil, fmt.Errorf("wamp: empty message")
	}
	var t int
	if err := json.Unmarshal(raw[0], &t); err != nil {
		return nil, fmt.Errorf("wamp: bad type tag: %w", err)
	}
	return &Message{Type: Type(t), Elements: raw[1:]}, nil
}

// String decodes element i as a string.
func (m *Message) String(i int) (string, error) {
	if i >= len(m.Elements) {
		return "", fmt.Errorf("wamp: %s missing element %d", m.Type, i)
	}
	var s string
	if err := json.Unmarshal(m.Elements[i], &s); err != nil {
		return "", fmt.Errorf("wamp: %s element %d not a string: %w", m.Type, i, err)
	}
	return s, nil
}

// Raw returns element i undecoded, or nil if absent.
func (m *Message) Raw(i int) json.RawMessage {
	if i >= len(m.Elements) {
		return nil
	}
	return m.Elements[i]
}

// encode builds a frame from a type tag and arguments.
func encode(t Type, args ...any) ([]byte, error) {
	out := make([]any, 0, len(args)+1)
	out = append(out, int(t))
	out = append(out, args...)
	return json.Marshal(out)
}
