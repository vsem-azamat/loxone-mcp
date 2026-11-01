package server

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vsem-azamat/loxone-mcp/internal/home"
)

func writeTool(title string, idempotent bool) *mcp.ToolAnnotations {
	f := false
	return &mcp.ToolAnnotations{Title: title, DestructiveHint: &f, IdempotentHint: idempotent, OpenWorldHint: &f}
}

type lightsIn struct {
	Target string `json:"target" jsonschema:"a light id/name/alias, a room (all its lights), a light group, or \"all\" for every light in the home"`
	Action string `json:"action" jsonschema:"off; on (the light's first favourite mood); mood (switch to the named mood)"`
	Mood   string `json:"mood,omitempty" jsonschema:"for action mood: the mood name or id, as get_device lists it"`
}

type blindsIn struct {
	Target   string   `json:"target" jsonschema:"a blind id/name/alias, a room (all its blinds), a blind group, or \"all\" for every blind in the home"`
	Action   string   `json:"action" jsonschema:"open (fully up); close (fully down); stop; shade (the automatic sun-shading position); position (move to position, optionally with slats); slats (turn the slats only); automatic_on / automatic_off (the blind's sun automation, which may move it again later)"`
	Position *float64 `json:"position,omitempty" jsonschema:"for action position: how far closed, in percent: 0 = fully open (up), 100 = fully closed (down)"`
	Slats    *float64 `json:"slats,omitempty" jsonschema:"slat angle in percent: 0 = horizontal (lets light in), 100 = vertical (shut)"`
}

type climateIn struct {
	Target      string   `json:"target" jsonschema:"a room (its climate controller, e.g. 'bathroom'), a climate device id/name/alias, or \"all\"; a towel heater is a separate switch (set_device)"`
	Action      string   `json:"action" jsonschema:"temperature (hold the room at the given temperature for minutes, then the schedule resumes; use this for 'set the room to X °C'); override (comfort or eco mode for minutes); stop_override (end a running override, also one set in the Loxone app); comfort_setpoint (change the schedule's comfort temperature permanently; only when the user asks for a permanent change)"`
	Temperature *float64 `json:"temperature,omitempty" jsonschema:"°C, rounded to 0.5; for temperature and comfort_setpoint"`
	Mode        string   `json:"mode,omitempty" jsonschema:"for override: comfort or eco"`
	Minutes     int      `json:"minutes,omitempty" jsonschema:"for temperature and override: duration in minutes, 1..1440 (default 240)"`
}

type deviceSetIn struct {
	Device string   `json:"device" jsonschema:"a switch, option or value device id/name/alias (e.g. ventilation speed, towel heater, leave mode)"`
	Action string   `json:"action" jsonschema:"on or off for a switch; select for an option (with option); set for a value (with value)"`
	Option string   `json:"option,omitempty" jsonschema:"for select: the option name or id as get_device lists it"`
	Value  *float64 `json:"value,omitempty" jsonschema:"for set: the number, within the device's range"`
}

func (s *Server) addWriteTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_lights",
		Description: "Turn lights off or on, or switch them to a mood (a lighting scene; there is no dimming or colour). Target one light, a room, or \"all\". " +
			"Returns each light's state before and after. Only lights the home map allows are changed.",
		InputSchema: schemaFor[lightsIn](map[string][]any{"action": {"off", "on", "mood"}}),
		Annotations: writeTool("Set lights", true),
	}, guard(s.log, "setLights", s.setLights))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_blinds",
		Description: "Move window blinds: open, close, stop, move to a position (percent closed) and/or turn the slats. Target one blind, a room, or \"all\". " +
			"Blinds take up to a minute to move: the result shows the state right after the command, with moving direction and target. A locked blind is refused.",
		InputSchema: schemaFor[blindsIn](map[string][]any{"action": {"open", "close", "stop", "shade", "position", "slats", "automatic_on", "automatic_off"}}),
		Annotations: writeTool("Set blinds", false),
	}, guard(s.log, "setBlinds", s.setBlinds))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_climate",
		Description: "Change a room's heating and cooling. To set a room to a temperature, use action temperature: it holds that temperature for minutes (default 240) and then the schedule resumes. " +
			"For 'warmer' or 'cooler' without a number, read target_temperature_c first (home_overview) and go 1 to 2 °C up or down; for 'for an hour' use minutes 60. " +
			"override switches to comfort or eco mode for a while; stop_override returns to the schedule; comfort_setpoint changes the schedule's comfort temperature for good. " +
			"Temperatures outside the room's allowed range are refused; get_device shows the range, setpoints and any running override.",
		InputSchema: schemaFor[climateIn](map[string][]any{
			"action": {"temperature", "override", "stop_override", "comfort_setpoint"},
			"mode":   {"comfort", "eco"},
		}),
		Annotations: writeTool("Set room climate", true),
	}, guard(s.log, "setClimate", s.setClimate))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "set_device",
		Description: "Change one switch, option or value device the home map allows: switch on/off (e.g. towel heater, leave mode, WC ventilation boost), " +
			"select an option (e.g. ventilation speed), or set a value (e.g. supply-air temperature). Devices that get_device shows as not writable are refused.",
		InputSchema: schemaFor[deviceSetIn](map[string][]any{"action": {"on", "off", "select", "set"}}),
		Annotations: writeTool("Set device", true),
	}, guard(s.log, "setDevice", s.setDevice))
}

func (s *Server) setLights(ctx context.Context, req *mcp.CallToolRequest, in lightsIn) (*mcp.CallToolResult, any, error) {
	return s.write(ctx, req, "set_lights", in.Target, home.KindLight, in, func(st home.States, d *home.Device) (home.Command, error) {
		return st.LightCommand(d, home.LightAction{Action: in.Action, Mood: in.Mood})
	})
}

func (s *Server) setBlinds(ctx context.Context, req *mcp.CallToolRequest, in blindsIn) (*mcp.CallToolResult, any, error) {
	return s.write(ctx, req, "set_blinds", in.Target, home.KindBlind, in, func(st home.States, d *home.Device) (home.Command, error) {
		return st.BlindCommand(d, home.BlindAction{Action: in.Action, Position: in.Position, Slats: in.Slats})
	})
}

func (s *Server) setClimate(ctx context.Context, req *mcp.CallToolRequest, in climateIn) (*mcp.CallToolResult, any, error) {
	now := s.opts.Now()
	return s.write(ctx, req, "set_climate", in.Target, home.KindClimate, in, func(st home.States, d *home.Device) (home.Command, error) {
		return st.ClimateCommand(d, home.ClimateAction{Action: in.Action, Temperature: in.Temperature, Mode: in.Mode, Minutes: in.Minutes}, now)
	})
}

func (s *Server) setDevice(ctx context.Context, req *mcp.CallToolRequest, in deviceSetIn) (*mcp.CallToolResult, any, error) {
	st, err := s.view(ctx)
	if err != nil {
		return nil, nil, err
	}
	d, err := st.Map.Resolve(in.Device)
	if err != nil {
		return nil, nil, err
	}
	return s.execute(ctx, req, st, "set_device", in, &home.Scope{Description: d.ID, Devices: []*home.Device{d}},
		func(st home.States, d *home.Device) (home.Command, error) {
			return st.DeviceCommand(d, home.DeviceAction{Action: in.Action, Option: in.Option, Value: in.Value})
		})
}

type builder func(home.States, *home.Device) (home.Command, error)

func (s *Server) write(ctx context.Context, req *mcp.CallToolRequest, tool, target string, kind home.Kind, args any, build builder) (*mcp.CallToolResult, any, error) {
	st, err := s.view(ctx)
	if err != nil {
		return nil, nil, err
	}
	scope, err := st.Map.ResolveScope(target, kind)
	if err != nil {
		return nil, nil, err
	}
	return s.execute(ctx, req, st, tool, args, scope, build)
}

type deviceResult struct {
	Device string `json:"device"`
	Name   string `json:"name"`
	OK     bool   `json:"ok"`
	// Confirmed is whether the Miniserver published a state of the device after the command;
	// absent when nothing was sent.
	Confirmed *bool          `json:"confirmed,omitempty"`
	NoChange  bool           `json:"no_change,omitempty"`
	Command   string         `json:"-"` // for the audit record only
	Error     string         `json:"error,omitempty"`
	Before    map[string]any `json:"before"`
	After     map[string]any `json:"after,omitempty"`
}

// execute is the one write path: build every command first (a validation error fails the whole
// call before anything moves), send them, wait for the states, audit.
func (s *Server) execute(ctx context.Context, req *mcp.CallToolRequest, st home.States, tool string, args any, scope *home.Scope, build builder) (*mcp.CallToolResult, any, error) {
	if !s.ms.Status().Connected {
		return nil, nil, errors.New("the Miniserver is not connected; nothing was changed. Retry later and tell the user if it persists")
	}
	if err := s.writesAllowed(); err != nil {
		return nil, nil, err
	}
	client := clientName(req)
	results := make([]deviceResult, len(scope.Devices))
	cmds := make([]home.Command, len(scope.Devices))
	var buildErrs []string
	for i, d := range scope.Devices {
		results[i] = deviceResult{Device: d.ID, Name: d.Name, Before: st.Summary(d)}
		if err := d.Writable(); err != nil { // the scope already filtered; checked again here
			buildErrs = append(buildErrs, err.Error())
			continue
		}
		c, err := build(st, d)
		if err != nil {
			buildErrs = append(buildErrs, err.Error())
			continue
		}
		if c.UUID != d.UUID {
			buildErrs = append(buildErrs, d.ID+": internal error: command for another control")
			continue
		}
		cmds[i] = c
		results[i].Command = c.Cmd
	}
	if len(buildErrs) > 0 {
		s.log.Warn("write refused", "tool", tool, "client", client, "target", scope.Description, "args", args, "errors", buildErrs)
		return nil, nil, fmt.Errorf("nothing was changed: %s", strings.Join(buildErrs, "; "))
	}

	sent := time.Now()
	watch := map[string][]string{}
	for i, d := range scope.Devices {
		if cmds[i].NoChange {
			results[i].OK, results[i].NoChange = true, true
			results[i].Command = ""
			continue
		}
		_, err := s.ms.Command(ctx, cmds[i].UUID, cmds[i].Cmd)
		if err != nil {
			results[i].Error = err.Error()
			continue
		}
		results[i].OK = true
		watch[d.ID] = d.StateUUIDs()
	}
	confirmed := s.waitForStates(ctx, watch, sent)
	okCount := 0
	for i, d := range scope.Devices {
		results[i].After = st.Summary(d)
		if !results[i].NoChange && results[i].OK {
			c := confirmed[d.ID]
			results[i].Confirmed = &c
		}
		outcome := "ok"
		switch {
		case !results[i].OK:
			outcome = "error"
		case results[i].NoChange:
			outcome = "no_change"
			okCount++
		case !*results[i].Confirmed:
			outcome = "unconfirmed"
			okCount++
		default:
			okCount++
		}
		s.log.Info("write", "tool", tool, "client", client, "target", scope.Description, "device", d.ID,
			"command", results[i].Command, "outcome", outcome, "error", results[i].Error,
			"before", results[i].Before, "after", results[i].After)
	}
	out := map[string]any{"target": scope.Description, "results": results}
	if len(scope.Skipped) > 0 {
		out["skipped"] = scope.Skipped
	}
	if okCount < len(results) {
		out["warning"] = fmt.Sprintf("%d of %d commands failed", len(results)-okCount, len(results))
	}
	for _, r := range results {
		if r.OK && r.Confirmed != nil && !*r.Confirmed {
			out["note"] = "unconfirmed: the Miniserver accepted the command but published no new state yet; check with get_device"
			break
		}
	}
	return nil, out, nil
}

// waitForStates waits until every watched device published a state after the commands, or
// until the settle time ends, and reports which devices did. A command that changes nothing
// publishes nothing, so the wait is bounded rather than exact.
func (s *Server) waitForStates(ctx context.Context, watch map[string][]string, since time.Time) map[string]bool {
	done := map[string]bool{}
	if len(watch) == 0 {
		return done
	}
	deadline := time.NewTimer(s.opts.SettleWait)
	defer deadline.Stop()
	store := s.ms.Store()
	for {
		changed := store.Changed()
		for id, uuids := range watch {
			for _, u := range uuids {
				if v, ok := store.Get(u); ok && v.Updated.After(since) {
					done[id] = true
					break
				}
			}
		}
		if len(done) == len(watch) {
			// let the related states (position, direction, target) arrive too
			select {
			case <-time.After(400 * time.Millisecond):
			case <-ctx.Done():
			}
			return done
		}
		select {
		case <-changed:
		case <-deadline.C:
			return done
		case <-ctx.Done():
			return done
		}
	}
}

func clientName(req *mcp.CallToolRequest) string {
	if req == nil || req.Session == nil {
		return ""
	}
	p := req.Session.InitializeParams()
	if p == nil || p.ClientInfo == nil {
		return ""
	}
	return p.ClientInfo.Name
}
