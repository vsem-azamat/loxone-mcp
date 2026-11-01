package server_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/vsem-azamat/loxone-mcp/internal/fakems"
	"github.com/vsem-azamat/loxone-mcp/internal/home"
	"github.com/vsem-azamat/loxone-mcp/internal/loxone"
	"github.com/vsem-azamat/loxone-mcp/internal/server"
	"github.com/vsem-azamat/loxone-mcp/internal/structure"
)

const overlay = `
rooms:
  "01000000-0000-0001-ffff000000000001": {id: living, name: Living room}
  "01000000-0000-0002-ffff000000000001": {id: bedroom, name: Bedroom}
  "01000000-0000-0003-ffff000000000001": {id: house, name: Whole flat}
devices:
  "10000000-0000-0001-ffff000000000001": {id: living.light, name: Living light, safety: control}
  "10000000-0000-0002-ffff000000000001": {id: bedroom.light, name: Bedroom light}
  "20000000-0000-0001-ffff000000000001": {id: living.blind, name: Living blind, safety: control}
  "20000000-0000-0002-ffff000000000001": {id: bedroom.blind, name: Bedroom blind, safety: control}
  "30000000-0000-0001-ffff000000000001": {id: living.climate, name: Living climate, safety: control}
  "40000000-0000-0001-ffff000000000001": {id: bedroom.towel, name: Towel rail, safety: control}
  "40000000-0000-0002-ffff000000000001": {id: house.storm, name: Storm protection}
  "60000000-0000-0001-ffff000000000001": {id: house.water, name: Water meter, tags: [meter]}
  "70000000-0000-0001-ffff000000000001": {id: house.smoke, name: Smoke alarm, safety: control}
`

const (
	livingMoods  = "10000000-0000-0001-ffff0000000000a3"
	livingActive = "10000000-0000-0001-ffff0000000000a2"
	livingFav    = "10000000-0000-0001-ffff0000000000a4"
	bedroomPos   = "20000000-0000-0002-ffff0000000000a4"
	livingPos    = "20000000-0000-0001-ffff0000000000a4"
	smokeLevel   = "70000000-0000-0001-ffff0000000000a1"
	meterText    = "60000000-0000-0001-ffff0000000000a1"
	tempActual   = "30000000-0000-0001-ffff0000000000a2"
)

type env struct {
	ms      *fakems.Server
	session *mcp.ClientSession
	logs    *bytes.Buffer
}

func setup(t *testing.T, structureFile []byte) *env {
	t.Helper()
	ms := fakems.New(
		map[string]float64{bedroomPos: 0, livingPos: 0.5, smokeLevel: 0, tempActual: 21.4},
		map[string]string{
			livingMoods:                           `[{"name":"Bright","id":777},{"name":"Off","id":778}]`,
			livingActive:                          "[777]",
			livingFav:                             "[777,778]",
			meterText:                             "12.5 m3",
			"0f000000-0000-0001-ffff000000000001": "2026-01-02 03:04:05 +01:00:00",
		})
	t.Cleanup(ms.Close)
	ms.SetEffect(func(uuid, cmd string) (map[string]float64, map[string]string, int) {
		switch {
		case strings.HasPrefix(uuid, "20000000-0000-0002") && cmd == "FullDown":
			return map[string]float64{bedroomPos: 1}, nil, 0
		case strings.HasPrefix(uuid, "10000000-0000-0001") && cmd == "changeTo/778":
			return nil, map[string]string{livingActive: "[778]"}, 0
		}
		return nil, nil, 0
	})
	ov, err := home.ParseOverlay([]byte(overlay))
	if err != nil {
		t.Fatal(err)
	}
	logs := &bytes.Buffer{}
	log := slog.New(slog.NewJSONHandler(logs, nil))
	var srv *server.Server
	client := loxone.New(loxone.Config{
		URL: ms.URL, User: fakems.User, Password: fakems.Password,
		TokenFile: filepath.Join(t.TempDir(), "token.json"), HTTPClient: ms.Client(),
		OnStructure: func(f *structure.File) {
			if structureFile != nil {
				f, _ = structure.Parse(structureFile)
			}
			srv.SetStructure(f)
		},
	})
	srv = server.New(client, server.Options{Overlay: ov, Logger: log, SettleWait: time.Second})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = client.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); <-done })

	st, ct := mcp.NewInMemoryTransports()
	ss, err := srv.MCP().Connect(ctx, st, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test-agent", Version: "1"}, nil).Connect(ctx, ct, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = cs.Close() })
	return &env{ms: ms, session: cs, logs: logs}
}

func (e *env) call(t *testing.T, tool string, args map[string]any) (map[string]any, bool, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	res, err := e.session.CallTool(ctx, &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", tool, err)
	}
	var sb strings.Builder
	for _, c := range res.Content {
		if tc, ok := c.(*mcp.TextContent); ok {
			sb.WriteString(tc.Text)
		}
	}
	text := sb.String()
	var out map[string]any
	if res.StructuredContent != nil {
		b, _ := json.Marshal(res.StructuredContent)
		_ = json.Unmarshal(b, &out)
	}
	return out, res.IsError, text
}

func TestToolList(t *testing.T) {
	e := setup(t, nil)
	res, err := e.session.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{
		"home_overview": true, "list_devices": true, "get_device": true, "get_meters": true,
		"get_intercom_bells": true, "set_lights": false, "set_blinds": false, "set_climate": false, "set_device": false,
	}
	if len(res.Tools) != len(want) {
		t.Fatalf("%d tools", len(res.Tools))
	}
	for _, tl := range res.Tools {
		ro, ok := want[tl.Name]
		if !ok {
			t.Fatalf("unexpected tool %s", tl.Name)
		}
		if tl.Annotations == nil || tl.Annotations.ReadOnlyHint != ro {
			t.Errorf("%s: read-only hint %v", tl.Name, tl.Annotations)
		}
		if len(tl.Description) < 40 {
			t.Errorf("%s: description too short", tl.Name)
		}
	}
}

func TestOverview(t *testing.T) {
	e := setup(t, nil)
	out, isErr, text := e.call(t, "home_overview", nil)
	if isErr {
		t.Fatal(text)
	}
	if out["connection"].(map[string]any)["connected"] != true {
		t.Fatalf("connection %v", out["connection"])
	}
	if !strings.Contains(text, `"temperature_c":21.4`) || !strings.Contains(text, `"moods":["Bright"]`) {
		t.Fatalf("overview: %s", text)
	}
	if strings.Contains(text, "12.5 m3") {
		t.Fatal("meter in the overview")
	}
	e.ms.Push(map[string]float64{smokeLevel: 2}, nil)
	time.Sleep(100 * time.Millisecond)
	_, _, text = e.call(t, "home_overview", nil)
	if !strings.Contains(text, "SMOKE/FIRE ALARM") {
		t.Fatalf("no alarm: %s", text)
	}
	out, _, _ = e.call(t, "get_meters", nil)
	if !strings.Contains(mustJSON(out), "12.5 m3") {
		t.Fatalf("meters %v", out)
	}
}

func mustJSON(v any) string {
	b, _ := json.Marshal(v)
	return string(b)
}

func TestCloseBedroomBlinds(t *testing.T) {
	e := setup(t, nil)
	out, isErr, text := e.call(t, "set_blinds", map[string]any{"target": "bedroom", "action": "close"})
	if isErr {
		t.Fatal(text)
	}
	cmds := e.ms.Commands()
	if len(cmds) != 1 || cmds[0] != "20000000-0000-0002-ffff000000000001/FullDown" {
		t.Fatalf("commands %v", cmds)
	}
	r := out["results"].([]any)[0].(map[string]any)
	if r["ok"] != true || r["confirmed"] != true || r["after"].(map[string]any)["closed_percent"] != float64(100) {
		t.Fatalf("result %v", r)
	}
	if !strings.Contains(e.logs.String(), `"msg":"write"`) || !strings.Contains(e.logs.String(), `"client":"test-agent"`) {
		t.Fatalf("no audit line: %s", e.logs.String())
	}
	if strings.Contains(e.logs.String(), fakems.Password) {
		t.Fatal("password in the logs")
	}
}

func TestAllLightsOffSkipsReadOnly(t *testing.T) {
	e := setup(t, nil)
	out, isErr, text := e.call(t, "set_lights", map[string]any{"target": "all", "action": "off"})
	if isErr {
		t.Fatal(text)
	}
	if cmds := e.ms.Commands(); len(cmds) != 1 || cmds[0] != "10000000-0000-0001-ffff000000000001/changeTo/778" {
		t.Fatalf("commands %v", cmds)
	}
	if len(out["skipped"].([]any)) != 1 {
		t.Fatalf("skipped %v", out["skipped"])
	}
	// again: already off, nothing is sent
	out, _, _ = e.call(t, "set_lights", map[string]any{"target": "living.light", "action": "off"})
	if len(e.ms.Commands()) != 1 || out["results"].([]any)[0].(map[string]any)["no_change"] != true {
		t.Fatalf("second off: %v %v", e.ms.Commands(), out)
	}
}

func TestRefusals(t *testing.T) {
	e := setup(t, nil)
	cases := []struct {
		tool string
		args map[string]any
		want string
	}{
		{"set_device", map[string]any{"device": "house.smoke", "action": "off"}, "never changed"},
		{"set_device", map[string]any{"device": "storm protection", "action": "on"}, "read-only"},
		{"set_lights", map[string]any{"target": "bedroom", "action": "off"}, "nothing to change"},
		{"set_blinds", map[string]any{"target": "livng blind", "action": "close"}, "did you mean: living.blind"},
		{"set_blinds", map[string]any{"target": "living", "action": "position", "position": 150}, "0..100"},
		{"set_climate", map[string]any{"target": "living", "action": "temperature", "temperature": 35}, "temperature must be"},
		{"set_blinds", map[string]any{"target": "living", "action": "fly"}, ""},
	}
	for _, c := range cases {
		_, isErr, text := e.call(t, c.tool, c.args)
		if !isErr || !strings.Contains(text, c.want) {
			t.Errorf("%s %v: error=%v %q", c.tool, c.args, isErr, text)
		}
	}
	if cmds := e.ms.Commands(); len(cmds) != 0 {
		t.Fatalf("refused calls sent %v", cmds)
	}
}

func TestAdminUserCannotWrite(t *testing.T) {
	adminStructure := bytes.Replace(fakems.Structure, []byte(`"isAdmin": false`), []byte(`"isAdmin": true`), 1)
	e := setup(t, adminStructure)
	_, isErr, text := e.call(t, "set_blinds", map[string]any{"target": "bedroom", "action": "close"})
	if !isErr || !strings.Contains(text, "administrator") {
		t.Fatalf("admin write: %v %s", isErr, text)
	}
	if _, isErr, _ := e.call(t, "home_overview", nil); isErr {
		t.Fatal("reads refused for admin")
	}
}

func TestGetDevice(t *testing.T) {
	e := setup(t, nil)
	out, isErr, text := e.call(t, "get_device", map[string]any{"device": "Living climate"})
	if isErr {
		t.Fatal(text)
	}
	if out["writable"] != true || len(out["actions"].([]any)) == 0 || out["setpoint_range"] == nil {
		t.Fatalf("%v", out)
	}
	out, _, _ = e.call(t, "get_device", map[string]any{"device": "house.smoke"})
	if out["writable"] != false || len(out["actions"].([]any)) != 0 {
		t.Fatalf("smoke alarm %v", out)
	}
}
