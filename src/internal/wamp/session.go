package wamp

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"

	"github.com/gorilla/websocket"
)

// Subprotocol is the WebSocket subprotocol the 3Dconnexion client requests.
const Subprotocol = "wamp"

// CallError is returned by CallClient when the page answers with a CALLERROR.
// The client library uses this for properties it does not implement, which is
// normal and expected — callers should treat it as "unsupported", not fatal.
type CallError struct {
	URI  string
	Desc string
}

func (e *CallError) Error() string { return fmt.Sprintf("%s: %s", e.URI, e.Desc) }

// IsUnsupported reports whether err is a client-side CALLERROR, i.e. the page
// does not implement the property.
func IsUnsupported(err error) bool {
	var ce *CallError
	return errors.As(err, &ce)
}

// Handler receives inbound client-initiated messages.
type Handler interface {
	// OnCall handles a client CALL. procURI is prefix-expanded. Returning a
	// non-nil error sends a CALLERROR; otherwise the result is sent as a
	// CALLRESULT.
	OnCall(ctx context.Context, s *Session, procURI string, args []json.RawMessage) (any, error)
	// OnSubscribe is called when the client subscribes. topic is the RAW
	// string the client sent (normally a CURIE such as
	// "3dconnexion:3dcontroller/7"). Publish events back using this exact
	// string: autobahn normalises both forms, but echoing what the client
	// sent cannot fail to match. Call Resolve if you need the expansion.
	OnSubscribe(ctx context.Context, s *Session, topic string)
}

type pending struct {
	result json.RawMessage
	err    error
	done   chan struct{}
}

// Session wraps one WebSocket connection speaking WAMP v1.
type Session struct {
	conn *websocket.Conn
	log  *slog.Logger

	writeMu sync.Mutex

	prefixMu sync.RWMutex
	prefixes map[string]string

	pendingMu  sync.Mutex
	pendingRPC map[string]*pending

	ID string
}

// NewSession wraps an upgraded WebSocket connection.
func NewSession(conn *websocket.Conn, log *slog.Logger) *Session {
	return &Session{
		conn:       conn,
		log:        log,
		prefixes:   make(map[string]string),
		pendingRPC: make(map[string]*pending),
		ID:         randID(16),
	}
}

// Welcome sends the opening WELCOME frame. The client waits for this before
// sending anything.
func (s *Session) Welcome(serverIdent string) error {
	return s.send(TypeWelcome, s.ID, 1, serverIdent)
}

func (s *Session) send(t Type, args ...any) error {
	b, err := encode(t, args...)
	if err != nil {
		return err
	}
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	s.log.Debug("wamp send", "type", t.String(), "raw", string(b))
	return s.conn.WriteMessage(websocket.TextMessage, b)
}

// Reply answers a client CALL with a CALLRESULT.
func (s *Session) Reply(callID string, result any) error {
	return s.send(TypeCallResult, callID, result)
}

// ReplyError answers a client CALL with a CALLERROR.
func (s *Session) ReplyError(callID, errURI, desc string) error {
	return s.send(TypeCallError, callID, errURI, desc)
}

// RegisterPrefix records a CURIE prefix. Normally driven by inbound PREFIX
// messages; exposed for tests.
func (s *Session) RegisterPrefix(prefix, uri string) {
	s.prefixMu.Lock()
	defer s.prefixMu.Unlock()
	s.prefixes[prefix] = uri
}

// Resolve expands a CURIE such as "3dx_rpc:create" using the prefixes the
// client registered. Strings whose scheme is not a registered prefix (a full
// wss:// URI, say) are returned unchanged.
//
// Note the expansion is a plain concatenation, so
// "3dconnexion:3dcontroller/7" against prefix
// "wss://127.51.68.120/3dconnexion" yields
// "wss://127.51.68.120/3dconnexion3dcontroller/7" — with no separating slash.
// That is what the real driver produces; do not "fix" it.
func (s *Session) Resolve(uri string) string {
	prefix, rest, found := strings.Cut(uri, ":")
	if !found {
		return uri
	}
	s.prefixMu.RLock()
	base, ok := s.prefixes[prefix]
	s.prefixMu.RUnlock()
	if !ok {
		return uri
	}
	return base + rest
}

// CallClient performs a server-to-client RPC. WAMP v1 has no such thing, so
// the CALL is wrapped in an EVENT published to the controller topic; the page
// unwraps it and answers with a bare CALLRESULT/CALLERROR carrying our call id.
func (s *Session) CallClient(ctx context.Context, topic, procURI string, args ...any) (json.RawMessage, error) {
	callID := randID(16)

	inner := make([]any, 0, len(args)+3)
	inner = append(inner, int(TypeCall), callID, procURI, "")
	inner = append(inner, args...)

	p := &pending{done: make(chan struct{})}
	s.pendingMu.Lock()
	s.pendingRPC[callID] = p
	s.pendingMu.Unlock()

	defer func() {
		s.pendingMu.Lock()
		delete(s.pendingRPC, callID)
		s.pendingMu.Unlock()
	}()

	if err := s.send(TypeEvent, topic, inner); err != nil {
		return nil, err
	}

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-p.done:
		return p.result, p.err
	}
}

func (s *Session) resolvePending(callID string, result json.RawMessage, err error) {
	s.pendingMu.Lock()
	p, ok := s.pendingRPC[callID]
	s.pendingMu.Unlock()
	if !ok {
		s.log.Warn("wamp: reply for unknown call", "callID", callID)
		return
	}
	p.result, p.err = result, err
	close(p.done)
}

// Serve runs the read loop until the connection closes or ctx is cancelled.
func (s *Session) Serve(ctx context.Context, h Handler) error {
	for {
		_, raw, err := s.conn.ReadMessage()
		if err != nil {
			if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
				return nil
			}
			return err
		}
		s.log.Debug("wamp recv", "raw", string(raw))

		msg, err := Decode(raw)
		if err != nil {
			s.log.Warn("wamp: undecodable frame", "err", err)
			continue
		}
		if err := s.dispatch(ctx, h, msg); err != nil {
			return err
		}
	}
}

func (s *Session) dispatch(ctx context.Context, h Handler, msg *Message) error {
	switch msg.Type {
	case TypePrefix:
		prefix, err := msg.String(0)
		if err != nil {
			return nil
		}
		uri, err := msg.String(1)
		if err != nil {
			return nil
		}
		s.RegisterPrefix(prefix, uri)
		s.log.Debug("wamp prefix", "prefix", prefix, "uri", uri)

	case TypeCall:
		callID, err := msg.String(0)
		if err != nil {
			return nil
		}
		procURI, err := msg.String(1)
		if err != nil {
			return s.ReplyError(callID, "wamp.error.invalid", "missing procedure uri")
		}
		// Handlers run inline: the client library issues these sequentially
		// during the handshake and ordering matters.
		result, err := h.OnCall(ctx, s, s.Resolve(procURI), msg.Elements[2:])
		if err != nil {
			return s.ReplyError(callID, procURI+"#generic", err.Error())
		}
		return s.Reply(callID, result)

	case TypeSubscribe:
		topic, err := msg.String(0)
		if err != nil {
			return nil
		}
		h.OnSubscribe(ctx, s, topic)

	case TypeCallResult:
		callID, err := msg.String(0)
		if err != nil {
			return nil
		}
		s.resolvePending(callID, msg.Raw(1), nil)

	case TypeCallError:
		callID, err := msg.String(0)
		if err != nil {
			return nil
		}
		uri, _ := msg.String(1)
		desc, _ := msg.String(2)
		s.resolvePending(callID, nil, &CallError{URI: uri, Desc: desc})

	default:
		s.log.Debug("wamp: unhandled message type", "type", msg.Type.String())
	}
	return nil
}

// Close shuts the connection down.
func (s *Session) Close() error { return s.conn.Close() }

const idAlphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789"

func randID(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err) // crypto/rand failure is unrecoverable
	}
	for i := range b {
		b[i] = idAlphabet[int(b[i])%len(idAlphabet)]
	}
	return string(b)
}
