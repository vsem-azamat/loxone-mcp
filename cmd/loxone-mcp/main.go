// Command loxone-mcp is an MCP server (stdio) for a Loxone Miniserver.
package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vsem-azamat/loxone-mcp/internal/home"
	"github.com/vsem-azamat/loxone-mcp/internal/loxone"
	"github.com/vsem-azamat/loxone-mcp/internal/server"
	"github.com/vsem-azamat/loxone-mcp/internal/structure"
)

// version is set at release builds.
var version = "dev"

const usage = `loxone-mcp: an MCP server (stdio) for a Loxone Miniserver.

Usage:
  loxone-mcp [serve]       run the MCP server on stdin/stdout
  loxone-mcp check         connect, build the home map and report it (no changes)
  loxone-mcp overlay-init  print an overlay skeleton generated from the live structure file
  loxone-mcp logout        revoke the cached token on the Miniserver and delete it
  loxone-mcp version       print the version

Environment:
  LOXONE_URL        cloud DNS address https://connect.loxonecloud.com/<serial>, or the Miniserver's https URL (required)
  LOXONE_USER       Miniserver user (required)
  LOXONE_PASSWORD   its password, used only to acquire a token (or LOXONE_PASSWORD_FILE: a file holding it)
  LOXONE_ALLOW_ADMIN  set to 1 to allow writes with an administrator account (refused by default)
  LOXONE_OVERLAY    overlay file (default $XDG_CONFIG_HOME/loxone-mcp/home.yaml)
  LOXONE_STATE_DIR  where the token is cached (default $XDG_STATE_HOME/loxone-mcp)
  LOXONE_LOG_LEVEL  debug, info (default), warn or error
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	switch cmd {
	case "version", "-version", "--version":
		fmt.Fprintln(stdout, version)
		return 0
	case "help", "-h", "-help", "--help":
		fmt.Fprint(stdout, usage)
		return 0
	case "serve", "check", "overlay-init", "logout":
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	cfg, err := loadConfig()
	if err != nil {
		fmt.Fprintln(stderr, "loxone-mcp:", err)
		return 2
	}
	log := slog.New(slog.NewJSONHandler(stderr, &slog.HandlerOptions{Level: cfg.level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if cmd == "check" || cmd == "overlay-init" {
		// next to a running server, use its token without refreshing or replacing it
		if unlock, err := lockStateDir(cfg.stateDir); err == nil {
			defer unlock()
		} else {
			cfg.tokenReadOnly = true
		}
	}
	switch cmd {
	case "check":
		return check(ctx, cfg, log, stdout)
	case "overlay-init":
		return overlayInit(ctx, cfg, log, stdout)
	case "logout":
		unlock, err := lockStateDir(cfg.stateDir)
		if err != nil {
			fmt.Fprintln(stderr, "logout:", err, "(stop the server first)")
			return 1
		}
		defer unlock()
		lctx, cancel := context.WithTimeout(ctx, 45*time.Second)
		defer cancel()
		if err := newClient(cfg, log, nil).Logout(lctx); err != nil {
			fmt.Fprintln(stderr, "logout:", err)
			return 1
		}
		fmt.Fprintln(stdout, "token revoked")
		return 0
	default:
		return serve(ctx, cfg, log, stdin, stdout)
	}
}

type config struct {
	url, user, password string
	overlayPath         string
	stateDir            string
	tokenFile           string
	allowAdmin          bool
	tokenReadOnly       bool
	level               slog.Level
}

func loadConfig() (config, error) {
	c := config{
		url:        strings.TrimSpace(os.Getenv("LOXONE_URL")),
		user:       os.Getenv("LOXONE_USER"),
		password:   os.Getenv("LOXONE_PASSWORD"),
		allowAdmin: os.Getenv("LOXONE_ALLOW_ADMIN") == "1",
	}
	if f := os.Getenv("LOXONE_PASSWORD_FILE"); f != "" && c.password == "" {
		b, err := os.ReadFile(f) //nolint:gosec // the password file is configuration
		if err != nil {
			return c, fmt.Errorf("LOXONE_PASSWORD_FILE: %w", err)
		}
		c.password = strings.TrimRight(string(b), "\r\n")
	}
	// not passed on to anything this process starts
	_ = os.Unsetenv("LOXONE_PASSWORD")
	var missing []string
	for name, v := range map[string]string{"LOXONE_URL": c.url, "LOXONE_USER": c.user, "LOXONE_PASSWORD": c.password} {
		if v == "" {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		return c, fmt.Errorf("missing environment: %s", strings.Join(missing, ", "))
	}
	home := os.Getenv("HOME")
	cfgHome := os.Getenv("XDG_CONFIG_HOME")
	if cfgHome == "" {
		cfgHome = filepath.Join(home, ".config")
	}
	stateHome := os.Getenv("XDG_STATE_HOME")
	if stateHome == "" {
		stateHome = filepath.Join(home, ".local", "state")
	}
	c.overlayPath = os.Getenv("LOXONE_OVERLAY")
	if c.overlayPath == "" {
		c.overlayPath = filepath.Join(cfgHome, "loxone-mcp", "home.yaml")
	}
	stateDir := os.Getenv("LOXONE_STATE_DIR")
	if stateDir == "" {
		stateDir = filepath.Join(stateHome, "loxone-mcp")
	}
	c.stateDir = stateDir
	c.tokenFile = filepath.Join(stateDir, "token.json")
	if err := c.level.UnmarshalText([]byte(os.Getenv("LOXONE_LOG_LEVEL"))); err != nil || os.Getenv("LOXONE_LOG_LEVEL") == "" {
		c.level = slog.LevelInfo
	}
	return c, nil
}

func newClient(cfg config, log *slog.Logger, onStructure func(*structure.File)) *loxone.Client {
	return loxone.New(loxone.Config{
		URL:           cfg.url,
		User:          cfg.user,
		Password:      cfg.password,
		TokenFile:     cfg.tokenFile,
		TokenReadOnly: cfg.tokenReadOnly,
		ClientInfo:    "loxone-mcp",
		Logger:        log.With("component", "miniserver"),
		OnStructure:   onStructure,
	})
}

func serve(ctx context.Context, cfg config, log *slog.Logger, stdin io.Reader, stdout io.Writer) int {
	ov, err := home.LoadOverlay(cfg.overlayPath)
	if err != nil {
		log.Error("cannot start", "err", err.Error())
		return 1
	}
	unlock, err := lockStateDir(cfg.stateDir)
	if err != nil {
		log.Error("cannot start", "err", err.Error())
		return 1
	}
	defer unlock()
	var srv *server.Server
	client := newClient(cfg, log, func(f *structure.File) { srv.SetStructure(f) })
	srv = server.New(client, server.Options{Overlay: ov, Logger: log.With("component", "server"), Version: version, AllowAdmin: cfg.allowAdmin})

	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { _ = client.Run(cctx) }()

	log.Info("serving", "version", version, "overlay", cfg.overlayPath)
	err = srv.MCP().Run(ctx, &mcp.IOTransport{Reader: io.NopCloser(stdin), Writer: nopWriteCloser{stdout}})
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, io.EOF) {
		log.Error("server stopped", "err", err.Error())
		return 1
	}
	return 0
}

type nopWriteCloser struct{ io.Writer }

func (nopWriteCloser) Close() error { return nil }

func connect(ctx context.Context, cfg config, log *slog.Logger) (*loxone.Client, context.CancelFunc, error) {
	client := newClient(cfg, log, nil)
	cctx, cancel := context.WithCancel(ctx)
	go func() { _ = client.Run(cctx) }()
	wctx, wcancel := context.WithTimeout(ctx, 45*time.Second)
	defer wcancel()
	if err := client.WaitReady(wctx); err != nil {
		cancel()
		return nil, nil, err
	}
	return client, cancel, nil
}

func check(ctx context.Context, cfg config, log *slog.Logger, stdout io.Writer) int {
	ov, err := home.LoadOverlay(cfg.overlayPath)
	if err != nil {
		fmt.Fprintln(stdout, "overlay:", err)
		return 1
	}
	client, cancel, err := connect(ctx, cfg, log)
	if err != nil {
		fmt.Fprintln(stdout, "miniserver:", err)
		return 1
	}
	defer cancel()
	f := client.Structure()
	m, err := home.Build(f, ov)
	if err != nil {
		fmt.Fprintln(stdout, "home map:", err)
		return 1
	}
	fmt.Fprintf(stdout, "connected; structure %s; %d states\n", f.LastModified, client.Store().Len())
	fmt.Fprintf(stdout, "overlay %s: %d rooms, %d devices\n", cfg.overlayPath, len(ov.Rooms), len(ov.Devices))
	fmt.Fprintf(stdout, "map: %d rooms, %d devices, %d visible\n", len(m.Rooms), len(m.Devices), len(m.Visible()))
	for _, w := range m.Warnings {
		fmt.Fprintln(stdout, "warning:", w)
	}
	st := home.States{Map: m, Store: client.Store()}
	for _, d := range m.Visible() {
		mark := "read   "
		if d.Writable() == nil {
			mark = "CONTROL"
		}
		fmt.Fprintf(stdout, "%s %-14s %-40s %v\n", mark, d.Kind, d.ID, st.Summary(d))
	}
	return 0
}

func overlayInit(ctx context.Context, cfg config, log *slog.Logger, stdout io.Writer) int {
	client, cancel, err := connect(ctx, cfg, log)
	if err != nil {
		fmt.Fprintln(os.Stderr, "miniserver:", err)
		return 1
	}
	defer cancel()
	fmt.Fprint(stdout, home.GenerateOverlay(client.Structure()))
	return 0
}
