package loxone

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
	"strings"
	"sync"

	"github.com/coder/websocket"
)

// maxMessageBytes bounds one WebSocket message; the structure file is the largest.
const maxMessageBytes = 32 << 20

// ErrOutOfService is reported when the Miniserver announces that it goes out of service (an
// update or a restart) and closes the connection.
var ErrOutOfService = errors.New("miniserver is out of service")

// CodeError is a non-200 answer to a command.
type CodeError struct {
	Command string // the command without secrets
	Code    int
}

func (e *CodeError) Error() string {
	return fmt.Sprintf("miniserver answered %d to %s", e.Code, e.Command)
}

// Response is the answer to a command: {"LL": {"control": ..., "value": ..., "Code": ...}}.
type Response struct {
	Control string
	Value   json.RawMessage
	Code    int
}

// ValueString is the value as a string (strings unquoted, other JSON as is).
func (r Response) ValueString() string {
	var s string
	if json.Unmarshal(r.Value, &s) == nil {
		return s
	}
	return string(r.Value)
}

type llEnvelope struct {
	LL struct {
		Control string          `json:"control"`
		Value   json.RawMessage `json:"value"`
		Code    json.RawMessage `json:"Code"` // matched case-insensitively: "Code" or "code"
	} `json:"LL"`
}

func parseResponse(b []byte) (Response, error) {
	var env llEnvelope
	if err := json.Unmarshal(b, &env); err != nil {
		return Response{}, fmt.Errorf("answer is not JSON: %w", err)
	}
	code := 0
	raw := strings.Trim(string(env.LL.Code), `"`)
	if raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Response{}, fmt.Errorf("answer code %q: %w", raw, err)
		}
		code = n
	}
	return Response{Control: env.LL.Control, Value: env.LL.Value, Code: code}, nil
}

// eventSink receives the event tables of a connection.
type eventSink interface {
	SetNumber(uuid string, v float64)
	SetText(uuid, text string)
}

// conn is one authenticated or authenticating WebSocket connection.
type conn struct {
	ws   *websocket.Conn
	log  *slog.Logger
	sink eventSink

	texts      chan []byte // text messages: answers to commands, the structure file
	keepalives chan struct{}
	valueTable chan struct{} // closed when the first value event table arrived
	textTable  chan struct{} // closed when the first text event table arrived

	cmdMu sync.Mutex

	stop     context.CancelFunc
	done     chan struct{}
	doneOnce sync.Once
	err      error
}

func dial(ctx context.Context, wsURL string, opts *websocket.DialOptions, log *slog.Logger, sink eventSink) (*conn, error) {
	ws, resp, err := websocket.Dial(ctx, wsURL, opts)
	if resp != nil && resp.Body != nil {
		resp.Body.Close()
	}
	if err != nil {
		return nil, fmt.Errorf("websocket: %w", err)
	}
	ws.SetReadLimit(maxMessageBytes)
	c := &conn{
		ws:         ws,
		log:        log,
		sink:       sink,
		texts:      make(chan []byte, 1),
		keepalives: make(chan struct{}, 1),
		valueTable: make(chan struct{}),
		textTable:  make(chan struct{}),
		done:       make(chan struct{}),
	}
	// the read loop lives as long as the connection, not as long as the dial
	loopCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	c.stop = cancel
	go c.readLoop(loopCtx)
	return c, nil
}

func (c *conn) fail(err error) {
	c.doneOnce.Do(func() {
		c.err = err
		close(c.done)
		if c.stop != nil {
			c.stop()
		}
		_ = c.ws.Close(websocket.StatusNormalClosure, "")
	})
}

// Err is why the connection ended; valid after done is closed.
func (c *conn) Err() error {
	<-c.done
	return c.err
}

func (c *conn) close() { c.fail(errors.New("connection closed")) }

func (c *conn) readLoop(ctx context.Context) {
	var pending *header
	valueSeen, textSeen := false, false
	for {
		typ, b, err := c.ws.Read(ctx)
		if err != nil {
			c.fail(fmt.Errorf("read: %w", err))
			return
		}
		if pending == nil {
			if typ != websocket.MessageBinary {
				// a text message without a header: treat it as an answer
				c.deliverText(b)
				continue
			}
			h, err := parseHeader(b)
			if err != nil {
				c.fail(err)
				return
			}
			if h.estimated {
				continue // an exact header follows
			}
			switch {
			case h.ident == msgKeepalive:
				select {
				case c.keepalives <- struct{}{}:
				default:
				}
			case h.ident == msgOutOfService:
				c.fail(ErrOutOfService)
				return
			case h.hasPayload():
				pending = &h
			}
			continue
		}
		h := *pending
		pending = nil
		switch h.ident {
		case msgText:
			c.deliverText(b)
		case msgValueEvents:
			evs, err := parseValueEvents(b)
			if err != nil {
				c.log.Warn("bad value event table", "err", err)
			}
			for _, e := range evs {
				c.sink.SetNumber(e.UUID, e.Value)
			}
			if !valueSeen {
				valueSeen = true
				close(c.valueTable)
			}
		case msgTextEvents:
			evs, err := parseTextEvents(b)
			if err != nil {
				c.log.Warn("bad text event table", "err", err)
			}
			for _, e := range evs {
				c.sink.SetText(e.UUID, e.Text)
			}
			if !textSeen {
				textSeen = true
				close(c.textTable)
			}
		case msgBinary, msgDaytimer, msgWeatherEvents:
			// not used
		default:
			c.log.Debug("unknown message", "ident", h.ident)
		}
	}
}

func (c *conn) deliverText(b []byte) {
	// Never block the read loop: an older message nobody took is replaced.
	for {
		select {
		case c.texts <- b:
			return
		default:
		}
		select {
		case <-c.texts:
			c.log.Debug("dropped an unsolicited text message")
		default:
		}
	}
}

// send writes a command and returns its answer: the next text message that match accepts.
// Commands are serialized; the Miniserver answers them in order. A command that times out
// closes the connection, so its late answer can never be taken for the next command's.
func (c *conn) send(ctx context.Context, cmd string, match func([]byte) bool) ([]byte, error) {
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()
	// drop a stale unsolicited message
	select {
	case <-c.texts:
	default:
	}
	if err := c.ws.Write(ctx, websocket.MessageText, []byte(cmd)); err != nil {
		c.fail(fmt.Errorf("write: %w", err))
		return nil, c.err
	}
	for {
		select {
		case b := <-c.texts:
			if match == nil || match(b) {
				return b, nil
			}
			c.log.Debug("ignored a text message that does not answer the command")
		case <-c.done:
			return nil, c.err
		case <-ctx.Done():
			c.fail(errors.New("a command timed out"))
			return nil, ctx.Err()
		}
	}
}

// controlKey is the first three path segments of a command or of the control an answer
// echoes, in one spelling: answers say "dev/sps/io/<uuid>/on" for "jdev/sps/io/<uuid>/on".
func controlKey(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	if u, err := url.PathUnescape(s); err == nil {
		s = u
	}
	s = strings.TrimPrefix(s, "j")
	s = strings.TrimPrefix(s, "dev/")
	parts := strings.Split(s, "/")
	if len(parts) > 3 {
		parts = parts[:3]
	}
	return strings.Join(parts, "/")
}

// answers reports whether the text message is the LL answer to cmd.
func answers(cmd string) func([]byte) bool {
	want := controlKey(cmd)
	return func(b []byte) bool {
		r, err := parseResponse(b)
		if err != nil {
			return false
		}
		return controlKey(r.Control) == want
	}
}

// isFile accepts a text message that is not an LL answer: a file such as LoxAPP3.json.
func isFile(b []byte) bool {
	r, err := parseResponse(b)
	return err != nil || r.Control == ""
}

// command sends a command and decodes the LL answer. redacted is the command as it may be
// logged or reported; it never contains a password, hash or token.
func (c *conn) command(ctx context.Context, cmd, redacted string) (Response, error) {
	b, err := c.send(ctx, cmd, answers(cmd))
	if err != nil {
		return Response{}, err
	}
	r, err := parseResponse(b)
	if err != nil {
		return Response{}, fmt.Errorf("%s: %w", redacted, err)
	}
	if r.Code != 200 {
		return r, &CodeError{Command: redacted, Code: r.Code}
	}
	return r, nil
}

func (c *conn) keepalive(ctx context.Context) error {
	c.cmdMu.Lock()
	defer c.cmdMu.Unlock()
	select {
	case <-c.keepalives:
	default:
	}
	if err := c.ws.Write(ctx, websocket.MessageText, []byte("keepalive")); err != nil {
		c.fail(fmt.Errorf("write: %w", err))
		return c.err
	}
	select {
	case <-c.keepalives:
		return nil
	case <-c.done:
		return c.err
	case <-ctx.Done():
		return fmt.Errorf("keepalive: %w", ctx.Err())
	}
}
