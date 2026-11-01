package server

import (
	"context"
	"fmt"
	"slices"
	"time"

	"github.com/google/jsonschema-go/jsonschema"
	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vsem-azamat/loxone-mcp/internal/home"
)

// schemaFor infers the input schema of T and sets enums on the named properties.
func schemaFor[T any](enums map[string][]any) *jsonschema.Schema {
	sch, err := jsonschema.For[T](nil)
	if err != nil {
		panic(err)
	}
	for prop, vals := range enums {
		p := sch.Properties[prop]
		if p == nil {
			panic("schemaFor: no property " + prop)
		}
		p.Enum = vals
	}
	return sch
}

func readOnly(title string) *mcp.ToolAnnotations {
	f := false
	return &mcp.ToolAnnotations{Title: title, ReadOnlyHint: true, IdempotentHint: true, OpenWorldHint: &f}
}

type overviewIn struct{}

type listIn struct {
	Query    string `json:"query,omitempty" jsonschema:"free text matched against id, name, aliases, description, tags and kind, e.g. 'filter', 'ventilation', 'leave'"`
	Room     string `json:"room,omitempty" jsonschema:"only devices in this room (id, name or alias)"`
	Kind     string `json:"kind,omitempty" jsonschema:"only devices of this kind"`
	Writable bool   `json:"writable,omitempty" jsonschema:"only devices the write tools may change"`
}

type deviceIn struct {
	Device string `json:"device" jsonschema:"device id, name or alias, optionally with its room (\"bedroom left blind\")"`
}

type bellsIn struct {
	Limit int `json:"limit,omitempty" jsonschema:"how many recent bells to return (default 10, at most 50)"`
}

func kindEnum() []any {
	out := make([]any, len(home.Kinds))
	for i, k := range home.Kinds {
		out[i] = string(k)
	}
	return out
}

func item(st home.States, d *home.Device) map[string]any {
	out := map[string]any{"id": d.ID, "name": d.Name}
	for k, v := range st.Summary(d) {
		out[k] = v
	}
	return out
}

func (s *Server) addReadTools(srv *mcp.Server) {
	mcp.AddTool(srv, &mcp.Tool{
		Name: "home_overview",
		Description: "Current state of the whole home in one call: per room the climate (temperature, target, humidity and CO2 where measured, mode, active override), " +
			"lights (on/off and active mood), blinds (closed % and slats %, 0 = open), switches, ventilation and other values; plus alerts " +
			"(smoke alarm, doorbell ringing, blocked blinds, open windows) and the Miniserver connection status. Use it first for \"what's going on at home\". " +
			"Meters, group controllers and devices kept out of the overview are listed by list_devices.",
		InputSchema: schemaFor[overviewIn](nil),
		Annotations: readOnly("Home overview"),
	}, guard(s.log, "overview", s.overview))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "list_devices",
		Description: "List devices with their id, kind, room, whether the write tools may change them, and a one-line state. " +
			"Filter by room, kind or a free-text query; use it to find status devices such as a filter, a service alarm or a leave-mode switch. " +
			"Use the returned ids as targets of the other tools.",
		InputSchema: schemaFor[listIn](map[string][]any{"kind": kindEnum()}),
		Annotations: readOnly("List devices"),
	}, guard(s.log, "listDevices", s.listDevices))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_device",
		Description: "Full current state of one device: decoded state, every raw Miniserver state, its description and aliases, " +
			"available light moods or options, setpoint ranges, and the exact write-tool actions it allows (none when read-only).",
		InputSchema: schemaFor[deviceIn](nil),
		Annotations: readOnly("Get device"),
	}, guard(s.log, "getDevice", s.getDevice))

	mcp.AddTool(srv, &mcp.Tool{
		Name:        "get_meters",
		Description: "Readings of the home's utility meters (heat, water, power: every device tagged meter), as the Miniserver shows them, with units.",
		InputSchema: schemaFor[overviewIn](nil),
		Annotations: readOnly("Meters"),
	}, guard(s.log, "meters", s.meters))

	mcp.AddTool(srv, &mcp.Tool{
		Name: "get_intercom_bells",
		Description: "Whether the doorbell is ringing now and the times of recent unanswered rings, newest first (ISO 8601, home time zone). " +
			"Times only: no caller identity or picture.",
		InputSchema: schemaFor[bellsIn](nil),
		Annotations: readOnly("Doorbell history"),
	}, guard(s.log, "bells", s.bells))
}

func (s *Server) overview(ctx context.Context, _ *mcp.CallToolRequest, _ overviewIn) (*mcp.CallToolResult, any, error) {
	st, err := s.view(ctx)
	if err != nil {
		return nil, nil, err
	}
	type roomOut struct {
		ID      string           `json:"id"`
		Name    string           `json:"name"`
		Climate []map[string]any `json:"climate,omitempty"`
		Lights  []map[string]any `json:"lights,omitempty"`
		Blinds  []map[string]any `json:"blinds,omitempty"`
		Other   []map[string]any `json:"other,omitempty"`
	}
	var rooms []*roomOut
	byRoom := map[*home.Room]*roomOut{}
	alerts := []string{}
	for _, d := range st.Map.Visible() {
		sum := st.Summary(d)
		switch d.Kind {
		case home.KindSmokeAlarm:
			if sum["alarm"] == true {
				alerts = append(alerts, "SMOKE/FIRE ALARM: "+d.Name)
			}
		case home.KindIntercom:
			if sum["ringing"] == true {
				alerts = append(alerts, "doorbell ringing: "+d.Name)
			}
		case home.KindBlind:
			if why, ok := sum["blocked"].(string); ok {
				alerts = append(alerts, fmt.Sprintf("blind %s does not move: %s", d.ID, why))
			}
		case home.KindClimate:
			if sum["window_open"] == true {
				alerts = append(alerts, "window open: "+d.Room.Name)
			}
		}
		if !d.Overview || d.HasTag("meter") || d.Kind == home.KindLightGroup || d.Kind == home.KindBlindGroup {
			continue
		}
		r := byRoom[d.Room]
		if r == nil {
			r = &roomOut{ID: d.Room.ID, Name: d.Room.Name}
			byRoom[d.Room] = r
			rooms = append(rooms, r)
		}
		it := item(st, d)
		switch d.Kind {
		case home.KindClimate:
			r.Climate = append(r.Climate, it)
		case home.KindLight:
			r.Lights = append(r.Lights, it)
		case home.KindBlind:
			r.Blinds = append(r.Blinds, it)
		default:
			it["kind"] = d.Kind
			r.Other = append(r.Other, it)
		}
	}
	out := map[string]any{
		"connection": connectionInfo(s.ms.Status()),
		"rooms":      rooms,
		"alerts":     alerts,
	}
	if w := staleWarning(s.ms.Status()); w != "" {
		out["warning"] = w
	}
	return nil, out, nil
}

func (s *Server) listDevices(ctx context.Context, _ *mcp.CallToolRequest, in listIn) (*mcp.CallToolResult, any, error) {
	st, err := s.view(ctx)
	if err != nil {
		return nil, nil, err
	}
	var room *home.Room
	if in.Room != "" {
		if room = st.Map.Room(in.Room); room == nil {
			ids := []string{}
			for _, r := range st.Map.Rooms {
				if !r.Hidden {
					ids = append(ids, r.ID)
				}
			}
			return nil, nil, fmt.Errorf("no room matches %q; rooms: %v", in.Room, ids)
		}
	}
	if in.Kind != "" && !slices.Contains(home.Kinds, home.Kind(in.Kind)) {
		return nil, nil, fmt.Errorf("unknown kind %q; kinds: %v", in.Kind, home.Kinds)
	}
	list := []map[string]any{}
	for _, d := range st.Map.Visible() {
		if room != nil && d.Room != room {
			continue
		}
		if in.Kind != "" && string(d.Kind) != in.Kind {
			continue
		}
		if in.Query != "" && !d.Matches(in.Query) {
			continue
		}
		writable := d.Writable() == nil
		if in.Writable && !writable {
			continue
		}
		list = append(list, map[string]any{
			"id": d.ID, "name": d.Name, "kind": d.Kind, "room": d.Room.ID, "writable": writable, "state": st.Summary(d),
		})
	}
	rooms := []map[string]any{}
	for _, r := range st.Map.Rooms {
		if !r.Hidden {
			rooms = append(rooms, map[string]any{"id": r.ID, "name": r.Name})
		}
	}
	return nil, map[string]any{"devices": list, "rooms": rooms, "connection": connectionInfo(s.ms.Status())}, nil
}

func (s *Server) getDevice(ctx context.Context, _ *mcp.CallToolRequest, in deviceIn) (*mcp.CallToolResult, any, error) {
	st, err := s.view(ctx)
	if err != nil {
		return nil, nil, err
	}
	d, err := st.Map.Resolve(in.Device)
	if err != nil {
		return nil, nil, err
	}
	out := st.Detail(d)
	out["connection"] = connectionInfo(s.ms.Status())
	return nil, out, nil
}

func (s *Server) meters(ctx context.Context, _ *mcp.CallToolRequest, _ overviewIn) (*mcp.CallToolResult, any, error) {
	st, err := s.view(ctx)
	if err != nil {
		return nil, nil, err
	}
	list := []map[string]any{}
	for _, d := range st.Map.Visible() {
		if !d.HasTag("meter") {
			continue
		}
		it := item(st, d)
		if d.Description != "" {
			it["description"] = d.Description
		}
		list = append(list, it)
	}
	return nil, map[string]any{"meters": list, "connection": connectionInfo(s.ms.Status())}, nil
}

func (s *Server) bells(ctx context.Context, _ *mcp.CallToolRequest, in bellsIn) (*mcp.CallToolResult, any, error) {
	st, err := s.view(ctx)
	if err != nil {
		return nil, nil, err
	}
	limit := in.Limit
	if limit <= 0 {
		limit = 10
	}
	limit = min(limit, 50)
	list := []map[string]any{}
	for _, d := range st.Map.Visible() {
		if d.Kind != home.KindIntercom {
			continue
		}
		b := st.Bells(d)
		if len(b) > limit {
			b = b[:limit]
		}
		sum := st.Summary(d)
		list = append(list, map[string]any{"id": d.ID, "name": d.Name, "ringing": sum["ringing"], "recent_bells": b})
	}
	return nil, map[string]any{"intercoms": list, "now": s.opts.Now().In(st.Zone()).Format(time.RFC3339), "connection": connectionInfo(s.ms.Status())}, nil
}
