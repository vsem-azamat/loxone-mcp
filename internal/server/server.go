// Package server is the MCP server: the tools agents call, on top of the home map and the
// Miniserver client. Every write goes through one path that checks the map's safety rules,
// sends the commands, waits for the resulting state and writes an audit record.
package server

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vsem-azamat/loxone-mcp/internal/home"
	"github.com/vsem-azamat/loxone-mcp/internal/loxone"
	"github.com/vsem-azamat/loxone-mcp/internal/state"
	"github.com/vsem-azamat/loxone-mcp/internal/structure"
)

// Miniserver is what the server needs from the Miniserver client.
type Miniserver interface {
	WaitReady(ctx context.Context) error
	Status() loxone.Status
	Store() *state.Store
	Command(ctx context.Context, uuid, cmd string) (loxone.Response, error)
}

// Options configures the server.
type Options struct {
	Overlay *home.Overlay
	Logger  *slog.Logger
	Version string
	// ReadyWait is how long a tool waits for the first connection (default 10 s).
	ReadyWait time.Duration
	// SettleWait is how long a write waits for the device's states to change (default 3 s).
	SettleWait time.Duration
	Now        func() time.Time
	// AllowAdmin allows writes with an administrator account.
	AllowAdmin bool
}

// Server holds the current home map.
type Server struct {
	ms   Miniserver
	opts Options
	log  *slog.Logger

	mu    sync.RWMutex
	hm    *home.Map
	err   error // why the last structure file could not be mapped
	admin bool  // the Miniserver user is an administrator
}

// writesAllowed refuses every write when the Miniserver user is an administrator, unless
// allowed: an admin token can do far more than the allowlist, so a dedicated user is expected.
func (s *Server) writesAllowed() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.admin && !s.opts.AllowAdmin {
		return errors.New("writes are disabled by the server configuration (the Miniserver user is an administrator); tell the user, do not retry")
	}
	return nil
}

// New returns a server. Call SetStructure with every structure file the client loads.
func New(ms Miniserver, opts Options) *Server {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.DiscardHandler)
	}
	if opts.Overlay == nil {
		opts.Overlay = &home.Overlay{}
	}
	if opts.ReadyWait <= 0 {
		opts.ReadyWait = 10 * time.Second
	}
	if opts.SettleWait <= 0 {
		opts.SettleWait = 3 * time.Second
	}
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Version == "" {
		opts.Version = "dev"
	}
	return &Server{ms: ms, opts: opts, log: opts.Logger}
}

// SetStructure rebuilds the home map from a structure file. A file that cannot be mapped keeps
// the previous map.
func (s *Server) SetStructure(f *structure.File) {
	m, err := home.Build(f, s.opts.Overlay)
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.err = err
		s.log.Error("home map not rebuilt", "err", err.Error())
		return
	}
	// a structure file that does not name its user counts as an administrator's: fail closed
	s.hm, s.err, s.admin = m, nil, f.MsInfo.CurrentUser.IsAdmin || f.MsInfo.CurrentUser.Name == ""
	if s.admin && !s.opts.AllowAdmin {
		s.log.Warn("the Miniserver user is an administrator; writes are disabled")
	}
	for _, w := range m.Warnings {
		s.log.Warn("home map", "warning", w)
	}
	s.log.Info("home map built", "last_modified", m.LastModified, "devices", len(m.Devices), "visible", len(m.Visible()))
}

// Map is the current home map, or nil.
func (s *Server) Map() *home.Map {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.hm
}

// view waits for the first connection (bounded) and returns the map and its states.
func (s *Server) view(ctx context.Context) (home.States, error) {
	wctx, cancel := context.WithTimeout(ctx, s.opts.ReadyWait)
	defer cancel()
	waitErr := s.ms.WaitReady(wctx)
	s.mu.RLock()
	m, mapErr := s.hm, s.err
	s.mu.RUnlock()
	if m == nil {
		if mapErr != nil {
			return home.States{}, errors.New("the home map could not be built: " + mapErr.Error())
		}
		if waitErr != nil {
			return home.States{}, waitErr
		}
		return home.States{}, errors.New("the home map is not loaded yet; retry in a few seconds")
	}
	return home.States{Map: m, Store: s.ms.Store()}, nil
}

// MCP builds the MCP server with all tools.
func (s *Server) MCP() *mcp.Server {
	srv := mcp.NewServer(&mcp.Implementation{Name: "loxone-mcp", Version: s.opts.Version}, &mcp.ServerOptions{
		Instructions: instructions,
	})
	s.addReadTools(srv)
	s.addWriteTools(srv)
	return srv
}

const instructions = `Reads and controls one home's Loxone smart-home system: lights, blinds, room climate, ventilation and a few switches.
Start with home_overview for the current state; values are live from the Miniserver (temperatures in °C, positions in percent, times ISO 8601 in the home's time zone). Devices are named by id (e.g. "bedroom.blind.left"), name or alias, in English or the local language; list_devices and get_device show what exists and what each device allows.
Write tools change real things in an inhabited home: act only on the user's request, name targets precisely, and check the returned "after" state. Devices marked read-only cannot be changed by any tool; say so instead of looking for a way around it.
There is no single away tool. When the user leaves: set_lights target "all" action "off", and switch on a leave-mode switch if list_devices (query "leave") shows a writable one. Do not close blinds or change heating unless asked. Report what was done.`

// guard turns a panic in a tool into an error answer, so one bug does not end the server and
// every client's session.
func guard[In any](log *slog.Logger, name string, h mcp.ToolHandlerFor[In, any]) mcp.ToolHandlerFor[In, any] {
	return func(ctx context.Context, req *mcp.CallToolRequest, in In) (res *mcp.CallToolResult, out any, err error) {
		defer func() {
			if r := recover(); r != nil {
				log.Error("tool panicked", "tool", name, "panic", fmt.Sprint(r), "stack", string(debug.Stack()))
				res, out, err = nil, nil, errors.New("internal error in the server; check the result with get_device before retrying")
			}
		}()
		return h(ctx, req, in)
	}
}

func staleWarning(st loxone.Status) string {
	if st.Connected {
		return ""
	}
	return "the Miniserver is disconnected since " + st.Since.Format(time.RFC3339) + ": the values and alerts are the last known, not current"
}

func connectionInfo(st loxone.Status) map[string]any {
	out := map[string]any{"connected": st.Connected, "since": st.Since.Format(time.RFC3339)}
	if !st.Connected && st.LastError != "" {
		out["last_error"] = st.LastError
		out["note"] = "values are the last known before the connection dropped"
	}
	return out
}
