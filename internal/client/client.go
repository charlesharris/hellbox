// Package client speaks the hellboxd socket protocol.
//
// It exists so that `slay` — and any later client — never opens the state
// database directly. The daemon stays the single writer, and a remote client
// remains possible without reworking anything.
package client

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"

	"hellbox/internal/proto"
)

// Message is one thing received from the daemon.
//
// Responses and pushes arrive interleaved on the same connection and are
// delivered through one channel, so a consumer written as an event loop — a
// Bubble Tea program, for instance — needs no correlation logic of its own.
type Message struct {
	// Push is set for a server-initiated message.
	Push *proto.Push

	// Response is set for a reply to a request.
	Response *proto.Response

	// Connected and Disconnected mark transport changes. The daemon may be
	// restarted underneath a running client, which is normal rather than
	// exceptional: the client outlives it and reconnects.
	Connected    bool
	Disconnected bool
	Err          error
}

// Client maintains a connection to hellboxd, reconnecting as needed.
type Client struct {
	path string

	mu   sync.Mutex
	conn net.Conn
	id   int

	out    chan Message
	closed chan struct{}
	once   sync.Once
}

// New creates a client for the socket at path. Nothing is connected until Run
// is called.
func New(path string) *Client {
	return &Client{
		path:   path,
		out:    make(chan Message, 64),
		closed: make(chan struct{}),
	}
}

// Messages returns the stream of everything received. It is closed when the
// client stops.
func (c *Client) Messages() <-chan Message { return c.out }

// Close stops the client and releases the connection.
func (c *Client) Close() {
	c.once.Do(func() {
		close(c.closed)
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
		}
		c.mu.Unlock()
	})
}

// Run connects and keeps reading until Close. It reconnects with a bounded
// backoff, so starting `slay` before the daemon, or leaving it running across a
// daemon restart, both behave the way an operator would expect.
func (c *Client) Run() {
	defer close(c.out)

	backoff := 250 * time.Millisecond
	const maxBackoff = 5 * time.Second

	for {
		select {
		case <-c.closed:
			return
		default:
		}

		conn, err := net.Dial("unix", c.path)
		if err != nil {
			c.emit(Message{Err: fmt.Errorf("connecting to %s: %w", c.path, err)})
			select {
			case <-c.closed:
				return
			case <-time.After(backoff):
			}
			if backoff < maxBackoff {
				backoff *= 2
			}
			continue
		}
		backoff = 250 * time.Millisecond

		c.mu.Lock()
		c.conn = conn
		c.mu.Unlock()
		c.emit(Message{Connected: true})

		// Subscribing is the first thing done on every connection: the daemon
		// answers it with a full status snapshot, so the client is never
		// rendering a blank screen while it waits for something to change.
		if _, err := c.Send(proto.MethodSubscribe, nil); err != nil {
			c.emit(Message{Err: err})
		}

		c.read(conn)

		c.mu.Lock()
		c.conn = nil
		c.mu.Unlock()
		c.emit(Message{Disconnected: true})

		select {
		case <-c.closed:
			return
		case <-time.After(backoff):
		}
	}
}

// read consumes one connection until it fails or the client is closed.
func (c *Client) read(conn net.Conn) {
	sc := bufio.NewScanner(conn)
	// Status snapshots carry every drive and every health check; the default
	// scanner limit is not generous enough for a machine with several drives.
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for sc.Scan() {
		select {
		case <-c.closed:
			return
		default:
		}

		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}

		// A push has an "event" field and a response has "ok"; decoding into a
		// probe first avoids guessing wrong on a message that has neither.
		var probe struct {
			Event string `json:"event"`
			ID    *int   `json:"id"`
		}
		if err := json.Unmarshal(line, &probe); err != nil {
			c.emit(Message{Err: fmt.Errorf("malformed message from daemon: %w", err)})
			continue
		}

		if probe.Event != "" {
			var p proto.Push
			if err := json.Unmarshal(line, &p); err != nil {
				c.emit(Message{Err: fmt.Errorf("malformed push: %w", err)})
				continue
			}
			c.emit(Message{Push: &p})
			continue
		}

		var r proto.Response
		if err := json.Unmarshal(line, &r); err != nil {
			c.emit(Message{Err: fmt.Errorf("malformed response: %w", err)})
			continue
		}
		c.emit(Message{Response: &r})
	}
}

// Send issues a request and returns the id it was given. Its reply arrives on
// Messages like anything else, carrying the same id.
//
// The id is returned rather than hidden because responses cannot be told apart
// by shape: a log entry and a job summary both carry an "id" field and each
// decodes cleanly into the other's type. Correlating by id is the only correct
// way to know what came back.
func (c *Client) Send(method string, params any) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn == nil {
		return 0, fmt.Errorf("not connected to hellboxd")
	}

	c.id++
	id := c.id
	req := proto.Request{ID: id, Method: method}
	if params != nil {
		b, err := json.Marshal(params)
		if err != nil {
			return 0, fmt.Errorf("encode %s parameters: %w", method, err)
		}
		req.Params = b
	}

	b, err := json.Marshal(req)
	if err != nil {
		return 0, fmt.Errorf("encode %s: %w", method, err)
	}
	if _, err := c.conn.Write(append(b, '\n')); err != nil {
		return 0, fmt.Errorf("send %s: %w", method, err)
	}
	return id, nil
}

// emit delivers a message, dropping it if the consumer has stopped. A client
// that is shutting down must not block the reader.
func (c *Client) emit(m Message) {
	select {
	case c.out <- m:
	case <-c.closed:
	}
}
