// Package fakems is a fake Loxone Miniserver for tests: an HTTPS server with the WebSocket
// protocol (token authentication, structure file, binary event tables, keepalive) and control
// commands that tests script. It never talks to a real Miniserver.
package fakems

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
)

// Structure is the anonymised structure file the fake serves by default.
//
//go:embed testdata/structure.json
var Structure []byte

// NoAnswer is an Effect code: the command gets no answer.
const NoAnswer = -1

// Credentials of the fake.
const (
	User     = "resident"
	Password = "secret-password"
	salt     = "73616c74"
	key      = "6b65792d6f662d746865"
)

// Effect is what a control command does: the states it changes. Returning code 0 means 200;
// NoAnswer means the fake never answers the command.
type Effect func(uuid, cmd string) (numbers map[string]float64, texts map[string]string, code int)

// Server is a fake Miniserver.
type Server struct {
	*httptest.Server

	mu        sync.Mutex
	numbers   map[string]float64
	texts     map[string]string
	commands  []string
	effect    Effect
	tokens    map[string]bool
	logins    int
	conns     []*websocket.Conn
	structure []byte
}

// New starts a fake Miniserver with initial states.
func New(numbers map[string]float64, texts map[string]string) *Server {
	s := &Server{
		numbers:   map[string]float64{},
		texts:     map[string]string{},
		tokens:    map[string]bool{},
		structure: Structure,
	}
	for k, v := range numbers {
		s.numbers[k] = v
	}
	for k, v := range texts {
		s.texts[k] = v
	}
	s.Server = httptest.NewTLSServer(http.HandlerFunc(s.serveHTTP))
	return s
}

// SetEffect sets how control commands change states.
func (s *Server) SetEffect(e Effect) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.effect = e
}

// Commands lists the control commands received, as "<uuid>/<cmd>".
func (s *Server) Commands() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.commands...)
}

// Logins is how many password logins (getjwt) happened.
func (s *Server) Logins() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.logins
}

// DropConnections closes every open WebSocket, as a network failure would.
func (s *Server) DropConnections() {
	s.mu.Lock()
	conns := s.conns
	s.conns = nil
	s.mu.Unlock()
	for _, c := range conns {
		_ = c.CloseNow()
	}
}

// Push sends state changes to every connection.
func (s *Server) Push(numbers map[string]float64, texts map[string]string) {
	s.push(context.Background(), numbers, texts)
}

func (s *Server) push(ctx context.Context, numbers map[string]float64, texts map[string]string) {
	s.mu.Lock()
	for k, v := range numbers {
		s.numbers[k] = v
	}
	for k, v := range texts {
		s.texts[k] = v
	}
	conns := append([]*websocket.Conn(nil), s.conns...)
	s.mu.Unlock()
	for _, c := range conns {
		wctx, cancel := context.WithTimeout(ctx, 2*time.Second)
		_ = sendEvents(wctx, c, numbers, texts)
		cancel()
	}
}

func (s *Server) serveHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/ws/rfc6455" {
		http.NotFound(w, r)
		return
	}
	c, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{"remotecontrol"}})
	if err != nil {
		return
	}
	c.SetReadLimit(1 << 20)
	s.mu.Lock()
	s.conns = append(s.conns, c)
	s.mu.Unlock()
	s.handle(r.Context(), c)
}

func header(ident byte, n int) []byte {
	b := make([]byte, 8)
	b[0], b[1] = 0x03, ident
	binary.LittleEndian.PutUint32(b[4:], uint32(n))
	return b
}

func sendText(ctx context.Context, c *websocket.Conn, text string) error {
	if err := c.Write(ctx, websocket.MessageBinary, header(0, len(text))); err != nil {
		return err
	}
	return c.Write(ctx, websocket.MessageText, []byte(text))
}

func ll(control string, value any, code int) string {
	b, _ := json.Marshal(map[string]any{"LL": map[string]any{"control": control, "value": value, "Code": strconv.Itoa(code)}})
	return string(b)
}

func uuidBytes(u string) []byte {
	h := strings.ReplaceAll(u, "-", "")
	raw, _ := hex.DecodeString(h)
	b := make([]byte, 16)
	if len(raw) != 16 {
		return b
	}
	binary.LittleEndian.PutUint32(b[0:4], binary.BigEndian.Uint32(raw[0:4]))
	binary.LittleEndian.PutUint16(b[4:6], binary.BigEndian.Uint16(raw[4:6]))
	binary.LittleEndian.PutUint16(b[6:8], binary.BigEndian.Uint16(raw[6:8]))
	copy(b[8:], raw[8:])
	return b
}

func sendEvents(ctx context.Context, c *websocket.Conn, numbers map[string]float64, texts map[string]string) error {
	if len(numbers) > 0 {
		var p []byte
		for u, v := range numbers {
			p = append(p, uuidBytes(u)...)
			p = binary.LittleEndian.AppendUint64(p, math.Float64bits(v))
		}
		if err := c.Write(ctx, websocket.MessageBinary, header(2, len(p))); err != nil {
			return err
		}
		if err := c.Write(ctx, websocket.MessageBinary, p); err != nil {
			return err
		}
	}
	if len(texts) > 0 {
		var p []byte
		for u, t := range texts {
			p = append(p, uuidBytes(u)...)
			p = append(p, make([]byte, 16)...)
			p = binary.LittleEndian.AppendUint32(p, uint32(len(t)))
			p = append(p, t...)
			for len(p)%4 != 0 {
				p = append(p, 0)
			}
		}
		if err := c.Write(ctx, websocket.MessageBinary, header(3, len(p))); err != nil {
			return err
		}
		if err := c.Write(ctx, websocket.MessageBinary, p); err != nil {
			return err
		}
	}
	return nil
}

func hmacHex(hexKey, msg string) string {
	k, _ := hex.DecodeString(hexKey)
	m := hmac.New(sha256.New, k)
	m.Write([]byte(msg))
	return hex.EncodeToString(m.Sum(nil))
}

// PasswordHash is the hash a client must send to getjwt.
func PasswordHash() string {
	pw := sha256.Sum256([]byte(Password + ":" + salt))
	return hmacHex(key, User+":"+strings.ToUpper(hex.EncodeToString(pw[:])))
}

func (s *Server) handle(ctx context.Context, c *websocket.Conn) {
	authed := false
	for {
		_, b, err := c.Read(ctx)
		if err != nil {
			return
		}
		cmd := string(b)
		reply := func(v any, code int) {
			_ = sendText(ctx, c, ll(strings.TrimPrefix(cmd, "j"), v, code))
		}
		parts := strings.Split(cmd, "/")
		switch {
		case cmd == "keepalive":
			_ = c.Write(ctx, websocket.MessageBinary, header(6, 0))
		case strings.HasPrefix(cmd, "jdev/sys/getkey2/"):
			reply(map[string]string{"key": key, "salt": salt, "hashAlg": "SHA256"}, 200)
		case cmd == "jdev/sys/getkey":
			reply(key, 200)
		case strings.HasPrefix(cmd, "jdev/sys/getjwt/") && len(parts) >= 8:
			user, _ := url.PathUnescape(parts[4])
			if parts[3] != PasswordHash() || user != User || parts[5] != "4" {
				reply("", 401)
				continue
			}
			s.mu.Lock()
			s.logins++
			tok := fmt.Sprintf("token-%d", s.logins)
			s.tokens[tok] = true
			s.mu.Unlock()
			authed = true
			valid := int64(time.Since(time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC))/time.Second) + 30*86400
			reply(map[string]any{"token": tok, "key": key, "validUntil": valid, "tokenRights": 4, "unsecurePass": false}, 200)
		case strings.HasPrefix(cmd, "authwithtoken/") && len(parts) == 3:
			ok := false
			s.mu.Lock()
			for tok := range s.tokens {
				if hmacHex(key, tok) == parts[1] {
					ok = true
				}
			}
			s.mu.Unlock()
			if !ok {
				reply("", 401)
				continue
			}
			authed = true
			reply("", 200)
		case strings.HasPrefix(cmd, "jdev/sys/killtoken/") && len(parts) == 5:
			s.mu.Lock()
			for tok := range s.tokens {
				if hmacHex(key, tok) == parts[3] {
					delete(s.tokens, tok)
				}
			}
			s.mu.Unlock()
			reply("", 200)
		case !authed:
			reply("", 400)
		case cmd == "jdev/sps/LoxAPPversion3":
			var f struct {
				LastModified string `json:"lastModified"`
			}
			_ = json.Unmarshal(s.structure, &f)
			reply(f.LastModified, 200)
		case cmd == "data/LoxAPP3.json":
			_ = sendText(ctx, c, string(s.structure))
		case cmd == "jdev/sps/enablebinstatusupdate":
			reply("", 200)
			s.mu.Lock()
			nums, txts := map[string]float64{}, map[string]string{}
			for k, v := range s.numbers {
				nums[k] = v
			}
			for k, v := range s.texts {
				txts[k] = v
			}
			s.mu.Unlock()
			if len(nums) == 0 {
				nums["0f000000-0000-0002-ffff000000000001"] = 0
			}
			if len(txts) == 0 {
				txts["0f000000-0000-0001-ffff000000000001"] = "2026-01-02 03:04:05 +01:00:00"
			}
			_ = sendEvents(ctx, c, nums, txts)
		case strings.HasPrefix(cmd, "jdev/sps/io/") && len(parts) >= 5:
			uuid := parts[3]
			ctl := strings.Join(parts[4:], "/")
			s.mu.Lock()
			s.commands = append(s.commands, uuid+"/"+ctl)
			eff := s.effect
			s.mu.Unlock()
			var nums map[string]float64
			var txts map[string]string
			code := 200
			if eff != nil {
				var c0 int
				nums, txts, c0 = eff(uuid, ctl)
				if c0 != 0 {
					code = c0
				}
			}
			if code == NoAnswer {
				continue
			}
			reply("1", code)
			if code == 200 {
				s.push(ctx, nums, txts)
			}
		default:
			reply("", 404)
		}
	}
}
