// Package loxone talks to a Loxone Miniserver over its WebSocket: it finds the Miniserver
// through the Loxone cloud DNS, authenticates with a JSON Web Token, loads the structure file,
// keeps the state event tables in a store and sends control commands.
package loxone

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/vsem-azamat/loxone-mcp/internal/state"
	"github.com/vsem-azamat/loxone-mcp/internal/structure"
)

// Token permission of a long-lived app token (weeks), as the Loxone apps use.
const permissionApp = 4

// Config configures a Client.
type Config struct {
	// URL is the cloud DNS address (https://dns.loxonecloud.com/<serial>) or the Miniserver's
	// own https base URL.
	URL      string
	User     string
	Password string
	// TokenFile caches the token between runs; "" keeps it in memory.
	TokenFile string
	// ClientInfo names this client in the user's token list.
	ClientInfo string

	HTTPClient *http.Client // nil: a client with a 20 s timeout
	Logger     *slog.Logger
	Store      *state.Store
	// OnStructure is called with every structure file loaded, before state updates start.
	OnStructure func(*structure.File)

	KeepaliveInterval time.Duration // default 60 s
	MaxBackoff        time.Duration // default 60 s
	AuthFailBackoff   time.Duration // default 5 min, after the Miniserver rejected the credentials
}

// Status describes the connection.
type Status struct {
	Connected bool
	Since     time.Time // connected or disconnected since
	LastError string
}

// Client keeps one connection to the Miniserver and reconnects when it drops.
type Client struct {
	cfg    Config
	log    *slog.Logger
	tokens tokenStore

	mu        sync.Mutex
	cur       *conn
	ready     chan struct{} // closed while connected and the first state table arrived
	status    Status
	structure *structure.File
}

// New returns a client; Run connects it.
func New(cfg Config) *Client {
	if cfg.HTTPClient == nil {
		cfg.HTTPClient = &http.Client{Timeout: 20 * time.Second}
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.New(slog.DiscardHandler)
	}
	if cfg.Store == nil {
		cfg.Store = state.New(nil)
	}
	if cfg.KeepaliveInterval <= 0 {
		cfg.KeepaliveInterval = 60 * time.Second
	}
	if cfg.MaxBackoff <= 0 {
		cfg.MaxBackoff = 60 * time.Second
	}
	if cfg.AuthFailBackoff <= 0 {
		cfg.AuthFailBackoff = 5 * time.Minute
	}
	if cfg.ClientInfo == "" {
		cfg.ClientInfo = "loxone-mcp"
	}
	return &Client{
		cfg:    cfg,
		log:    cfg.Logger,
		tokens: tokenStore{path: cfg.TokenFile},
		ready:  make(chan struct{}),
		status: Status{Since: time.Now()},
	}
}

// Store is the state store the client fills.
func (c *Client) Store() *state.Store { return c.cfg.Store }

// Status reports the connection state.
func (c *Client) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.status
}

// Structure is the last loaded structure file, or nil.
func (c *Client) Structure() *structure.File {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.structure
}

// WaitReady waits until the client is connected and has the initial states.
func (c *Client) WaitReady(ctx context.Context) error {
	c.mu.Lock()
	ready := c.ready
	c.mu.Unlock()
	select {
	case <-ready:
		return nil
	case <-ctx.Done():
		st := c.Status()
		if st.LastError != "" {
			return fmt.Errorf("miniserver not connected: %s", st.LastError)
		}
		return errors.New("miniserver not connected yet")
	}
}

// Run connects and reconnects until ctx ends.
func (c *Client) Run(ctx context.Context) error {
	backoff := time.Second
	for {
		started := time.Now()
		err := c.session(ctx)
		select {
		case <-ctx.Done():
			return nil
		default:
		}
		c.setDisconnected(err)
		wait := backoff
		var ce *CodeError
		switch {
		case errors.As(err, &ce) && (ce.Code == 401 || ce.Code == 403 || ce.Code == 423 || ce.Code == 429):
			wait = c.cfg.AuthFailBackoff
			c.log.Error("miniserver rejected the login", "code", ce.Code, "retry_in", wait.String())
		case websocket.CloseStatus(err) == 4003 || websocket.CloseStatus(err) == 420:
			wait = c.cfg.AuthFailBackoff
			c.log.Error("miniserver blocks this client (too many failed logins or no login in time)",
				"close_code", int(websocket.CloseStatus(err)), "retry_in", wait.String())
		case errors.Is(err, ErrOutOfService):
			c.log.Warn("miniserver went out of service", "retry_in", wait.String())
		default:
			c.log.Warn("miniserver connection lost", "err", err.Error(), "retry_in", wait.String())
		}
		if time.Since(started) > 2*time.Minute {
			backoff = time.Second // the connection had worked; start over
		} else {
			backoff = min(backoff*2, c.cfg.MaxBackoff)
		}
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(wait):
		}
	}
}

func (c *Client) setDisconnected(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.status.Connected {
		c.status.Since = time.Now()
	}
	c.status.Connected = false
	if err != nil {
		c.status.LastError = err.Error()
	}
	c.cur = nil
	select {
	case <-c.ready:
		c.ready = make(chan struct{})
	default:
	}
}

// session runs one connection from dial to failure.
func (c *Client) session(ctx context.Context) error {
	base, err := Resolve(ctx, c.cfg.HTTPClient, c.cfg.URL)
	if err != nil {
		return err
	}
	wsURL, err := websocketURL(base)
	if err != nil {
		return err
	}
	dctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cn, err := dial(dctx, wsURL, &websocket.DialOptions{
		HTTPClient:   c.cfg.HTTPClient,
		Subprotocols: []string{"remotecontrol"},
	}, c.log, c.cfg.Store)
	if err != nil {
		return err
	}
	defer cn.close()
	if err := c.authenticate(dctx, cn); err != nil {
		return err
	}
	if err := c.loadStructure(dctx, cn); err != nil {
		return err
	}
	if _, err := cn.command(dctx, "jdev/sps/enablebinstatusupdate", "jdev/sps/enablebinstatusupdate"); err != nil {
		return err
	}
	select {
	case <-cn.valueTable:
	case <-cn.done:
		return cn.Err()
	case <-dctx.Done():
		return errors.New("no state table after enabling status updates")
	}
	// texts (moods, overrides) come in their own table; a home without any is possible
	select {
	case <-cn.textTable:
	case <-cn.done:
		return cn.Err()
	case <-time.After(5 * time.Second):
		c.log.Warn("no text state table within 5 s")
	}
	cancel()

	c.mu.Lock()
	c.cur = cn
	c.status = Status{Connected: true, Since: time.Now()}
	close(c.ready)
	c.mu.Unlock()
	c.log.Info("miniserver connected", "states", c.cfg.Store.Len())

	tick := time.NewTicker(c.cfg.KeepaliveInterval)
	defer tick.Stop()
	refresh := time.NewTicker(6 * time.Hour)
	defer refresh.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-cn.done:
			return cn.Err()
		case <-tick.C:
			kctx, kcancel := context.WithTimeout(ctx, 30*time.Second)
			err := cn.keepalive(kctx)
			kcancel()
			if err != nil {
				return err
			}
		case <-refresh.C:
			rctx, rcancel := context.WithTimeout(ctx, 30*time.Second)
			err := c.refreshIfDue(rctx, cn)
			rcancel()
			if err != nil {
				c.log.Warn("token refresh failed", "err", err.Error())
			}
		}
	}
}

func websocketURL(base string) (string, error) {
	u, err := url.Parse(base)
	if err != nil {
		return "", fmt.Errorf("miniserver address: %w", err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	default:
		return "", fmt.Errorf("miniserver address: unsupported scheme %q", u.Scheme)
	}
	u.Path = "/ws/rfc6455"
	u.RawQuery = ""
	return u.String(), nil
}

// authenticate uses the cached token, or acquires a new one with the password.
func (c *Client) authenticate(ctx context.Context, cn *conn) error {
	user := c.cfg.User
	tf := c.tokens.load(user)
	if tf.Token != "" && time.Until(FromEpoch2009(tf.ValidUntil)) > time.Minute {
		err := c.authWithToken(ctx, cn, tf)
		if err == nil {
			if err := c.refreshIfDue(ctx, cn); err != nil {
				c.log.Warn("token refresh failed", "err", err.Error())
			}
			return nil
		}
		var ce *CodeError
		if !errors.As(err, &ce) {
			return err // the connection failed, not the token
		}
		c.log.Warn("cached token rejected; acquiring a new one", "code", ce.Code)
		c.tokens.clear(user)
		tf = c.tokens.load(user)
	}
	return c.acquireToken(ctx, cn, tf)
}

func (c *Client) authWithToken(ctx context.Context, cn *conn, tf tokenFile) error {
	r, err := cn.command(ctx, "jdev/sys/getkey", "jdev/sys/getkey")
	if err != nil {
		return err
	}
	h, err := tokenHash(tf.HashAlg, r.ValueString(), tf.Token)
	if err != nil {
		return err
	}
	_, err = cn.command(ctx, "authwithtoken/"+h+"/"+url.PathEscape(tf.User), "authwithtoken/<hash>/<user>")
	return err
}

func (c *Client) acquireToken(ctx context.Context, cn *conn, tf tokenFile) error {
	user := c.cfg.User
	r, err := cn.command(ctx, "jdev/sys/getkey2/"+url.PathEscape(user), "jdev/sys/getkey2/<user>")
	if err != nil {
		return err
	}
	var ki keyInfo
	if err := decodeJSONValue(r.Value, &ki); err != nil {
		return fmt.Errorf("getkey2: %w", err)
	}
	h, err := passwordHash(ki, user, c.cfg.Password)
	if err != nil {
		return err
	}
	if tf.ClientUUID == "" {
		tf.ClientUUID = newClientUUID()
	}
	cmd := fmt.Sprintf("jdev/sys/getjwt/%s/%s/%d/%s/%s", h, url.PathEscape(user), permissionApp,
		tf.ClientUUID, url.PathEscape(c.cfg.ClientInfo))
	r, err = cn.command(ctx, cmd, "jdev/sys/getjwt/<hash>/<user>/...")
	if err != nil {
		return err
	}
	var ans jwtAnswer
	if err := decodeJSONValue(r.Value, &ans); err != nil {
		return fmt.Errorf("getjwt: %w", err)
	}
	if ans.Token == "" {
		return errors.New("getjwt: no token in the answer")
	}
	if ans.UnsecurePass {
		c.log.Warn("the miniserver reports the password as weak")
	}
	tf.Token, tf.ValidUntil, tf.HashAlg = ans.Token, ans.ValidUntil, ki.HashAlg
	if err := c.tokens.save(tf); err != nil {
		c.log.Warn("could not cache the token", "err", err.Error())
	}
	c.log.Info("token acquired", "valid_until", FromEpoch2009(ans.ValidUntil).Format(time.RFC3339))
	return nil
}

// refreshIfDue refreshes the token when less than three days of it are left.
func (c *Client) refreshIfDue(ctx context.Context, cn *conn) error {
	tf := c.tokens.load(c.cfg.User)
	if tf.Token == "" || time.Until(FromEpoch2009(tf.ValidUntil)) > 72*time.Hour {
		return nil
	}
	r, err := cn.command(ctx, "jdev/sys/getkey", "jdev/sys/getkey")
	if err != nil {
		return err
	}
	h, err := tokenHash(tf.HashAlg, r.ValueString(), tf.Token)
	if err != nil {
		return err
	}
	r, err = cn.command(ctx, "jdev/sys/refreshjwt/"+h+"/"+url.PathEscape(tf.User), "jdev/sys/refreshjwt/<hash>/<user>")
	if err != nil {
		return err
	}
	var ans jwtAnswer
	if err := decodeJSONValue(r.Value, &ans); err != nil {
		return fmt.Errorf("refreshjwt: %w", err)
	}
	if ans.Token != "" {
		tf.Token = ans.Token
	}
	if ans.ValidUntil != 0 {
		tf.ValidUntil = ans.ValidUntil
	}
	if err := c.tokens.save(tf); err != nil {
		return err
	}
	c.log.Info("token refreshed", "valid_until", FromEpoch2009(tf.ValidUntil).Format(time.RFC3339))
	return nil
}

// loadStructure loads the structure file when it changed since the last load.
func (c *Client) loadStructure(ctx context.Context, cn *conn) error {
	r, err := cn.command(ctx, "jdev/sps/LoxAPPversion3", "jdev/sps/LoxAPPversion3")
	if err != nil {
		return err
	}
	version := r.ValueString()
	if cur := c.Structure(); cur != nil && cur.LastModified == version {
		return nil
	}
	b, err := cn.send(ctx, "data/LoxAPP3.json", isFile)
	if err != nil {
		return fmt.Errorf("structure file: %w", err)
	}
	f, err := structure.Parse(b)
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.structure = f
	c.mu.Unlock()
	c.log.Info("structure file loaded", "last_modified", f.LastModified, "controls", len(f.Controls))
	if c.cfg.OnStructure != nil {
		c.cfg.OnStructure(f)
	}
	return nil
}

// Command sends a command to a control: jdev/sps/io/<uuid>/<cmd>. cmd must come from code, never
// from free text: callers build it from validated parts.
func (c *Client) Command(ctx context.Context, uuid, cmd string) (Response, error) {
	if err := c.WaitReady(ctx); err != nil {
		return Response{}, err
	}
	c.mu.Lock()
	cn := c.cur
	c.mu.Unlock()
	if cn == nil {
		return Response{}, errors.New("miniserver not connected")
	}
	path := "jdev/sps/io/" + uuid + "/" + cmd
	if strings.ContainsAny(uuid, "/?#% ") {
		return Response{}, fmt.Errorf("invalid control id %q", uuid)
	}
	return cn.command(ctx, path, path)
}

// Logout revokes the cached token on the Miniserver and forgets it. Without a cached token it
// does nothing.
func (c *Client) Logout(ctx context.Context) error {
	tf := c.tokens.load(c.cfg.User)
	if tf.Token == "" {
		return nil
	}
	base, err := Resolve(ctx, c.cfg.HTTPClient, c.cfg.URL)
	if err != nil {
		return err
	}
	wsURL, err := websocketURL(base)
	if err != nil {
		return err
	}
	cn, err := dial(ctx, wsURL, &websocket.DialOptions{HTTPClient: c.cfg.HTTPClient, Subprotocols: []string{"remotecontrol"}}, c.log, state.New(nil))
	if err != nil {
		return err
	}
	defer cn.close()
	if err := c.authWithToken(ctx, cn, tf); err != nil {
		var ce *CodeError
		if errors.As(err, &ce) {
			c.tokens.clear(c.cfg.User) // already invalid
			return nil
		}
		return err
	}
	r, err := cn.command(ctx, "jdev/sys/getkey", "jdev/sys/getkey")
	if err != nil {
		return err
	}
	h, err := tokenHash(tf.HashAlg, r.ValueString(), tf.Token)
	if err != nil {
		return err
	}
	if _, err := cn.command(ctx, "jdev/sys/killtoken/"+h+"/"+url.PathEscape(tf.User), "jdev/sys/killtoken/<hash>/<user>"); err != nil {
		return err
	}
	c.tokens.clear(c.cfg.User)
	return nil
}
