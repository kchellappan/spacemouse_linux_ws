package server

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"time"
)

// PlaintextRedirect wraps a listener so plain HTTP requests are answered with
// a redirect to HTTPS instead of Go's "Client sent an HTTP request to an
// HTTPS server."
//
// Browsers default a bare host:port to http://, and the bridge's address is
// something users type rather than click, so that error is what most people
// see the first time they open the status page. It reads like a crash.
//
// Connections are classified by their first byte: 0x16 is a TLS record of
// type handshake, which no HTTP method can begin with. Classification happens
// per connection in its own goroutine — doing it inline in Accept means one
// client that connects and says nothing stalls every connection behind it,
// and browsers open speculative connections routinely.
func PlaintextRedirect(inner net.Listener, log *slog.Logger) net.Listener {
	l := &peekListener{
		Listener: inner,
		log:      log,
		ready:    make(chan net.Conn),
		done:     make(chan struct{}),
	}
	go l.accept()
	return l
}

const (
	tlsHandshakeRecord = 0x16

	// peekTimeout bounds how long a connection may stay unclassified. A
	// browser preconnect completes its handshake promptly, so anything
	// silent for this long is not going to speak.
	peekTimeout = 30 * time.Second

	// redirectTimeout bounds serving one misdirected plaintext request.
	redirectTimeout = 5 * time.Second
)

type peekListener struct {
	net.Listener
	log *slog.Logger

	ready chan net.Conn
	done  chan struct{}
	once  sync.Once

	mu  sync.Mutex
	err error
}

// accept classifies connections off the caller's path.
func (l *peekListener) accept() {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			l.mu.Lock()
			l.err = err
			l.mu.Unlock()
			l.once.Do(func() { close(l.done) })
			return
		}
		go l.classify(conn)
	}
}

func (l *peekListener) classify(conn net.Conn) {
	_ = conn.SetReadDeadline(time.Now().Add(peekTimeout))
	br := bufio.NewReader(conn)
	first, err := br.Peek(1)
	if err != nil {
		conn.Close()
		return
	}
	// Hand the connection on with its own timeouts restored: TLS handshakes
	// and long-lived streams must not inherit the classification deadline.
	_ = conn.SetReadDeadline(time.Time{})

	if first[0] == tlsHandshakeRecord {
		select {
		case l.ready <- &peekConn{Conn: conn, reader: br}:
		case <-l.done:
			conn.Close()
		}
		return
	}
	l.redirect(conn, br)
}

func (l *peekListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.ready:
		return c, nil
	case <-l.done:
		l.mu.Lock()
		defer l.mu.Unlock()
		return nil, l.err
	}
}

// redirect answers one plaintext request and closes the connection.
func (l *peekListener) redirect(conn net.Conn, br *bufio.Reader) {
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(redirectTimeout))

	req, err := http.ReadRequest(br)
	if err != nil {
		return
	}
	host := req.Host
	if host == "" {
		host = l.Addr().String()
	}
	target := "https://" + host + req.URL.RequestURI()

	// 308 rather than 301: it preserves the method, and permanent caching is
	// correct because this listener will never serve plaintext.
	body := "Use " + target + "\n"
	fmt.Fprintf(conn, "HTTP/1.1 308 Permanent Redirect\r\n"+
		"Location: %s\r\n"+
		"Content-Type: text/plain; charset=utf-8\r\n"+
		"Content-Length: %d\r\n"+
		"Connection: close\r\n\r\n%s", target, len(body), body)

	if l.log != nil {
		l.log.Debug("redirected a plaintext request", "to", target)
	}
}

// peekConn replays bytes already read from the underlying connection.
type peekConn struct {
	net.Conn
	reader *bufio.Reader
}

func (c *peekConn) Read(p []byte) (int, error) { return c.reader.Read(p) }
