package home

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vsem-azamat/loxone-mcp/internal/fakems"
	"github.com/vsem-azamat/loxone-mcp/internal/state"
	"github.com/vsem-azamat/loxone-mcp/internal/structure"
)

const testOverlay = `
rooms:
  "01000000-0000-0001-ffff000000000001": {id: living, name: Living room, aliases: [lounge, obývák]}
  "01000000-0000-0002-ffff000000000001": {id: bedroom, name: Bedroom}
  "01000000-0000-0003-ffff000000000001": {id: house, name: Whole flat}
devices:
  "10000000-0000-0001-ffff000000000001": {id: living.light, name: Living light, safety: control, moods: {"1": kitchen, "778": "off"}}
  "10000000-0000-0002-ffff000000000001": {id: bedroom.light, name: Bedroom light, safety: read}
  "20000000-0000-0001-ffff000000000001": {id: living.blind, name: Living blind, safety: control}
  "20000000-0000-0002-ffff000000000001": {id: bedroom.blind, name: Bedroom blind, safety: control, aliases: [žaluzie ložnice]}
  "30000000-0000-0001-ffff000000000001": {id: living.climate, name: Living climate, safety: control, range: {min: 18, max: 25}}
  "40000000-0000-0001-ffff000000000001": {id: bedroom.towel, name: Towel rail, safety: control}
  "40000000-0000-0002-ffff000000000001": {id: house.storm, name: Storm protection, safety: control}
  "50000000-0000-0001-ffff000000000001": {id: vent.speed, safety: control, options: {"1": minimum, "3": normal}}
  "50000000-0000-0002-ffff000000000001": {id: vent.supply, safety: control}
  "60000000-0000-0001-ffff000000000001": {id: house.water, tags: [meter]}
  "70000000-0000-0001-ffff000000000001": {id: house.smoke, safety: control}
  "70000000-0000-0002-ffff000000000001": {id: house.door, safety: hidden}
`

func testMap(t *testing.T, overlay string) *Map {
	t.Helper()
	f, err := structure.Parse(fakems.Structure)
	if err != nil {
		t.Fatal(err)
	}
	o, err := ParseOverlay([]byte(overlay))
	if err != nil {
		t.Fatal(err)
	}
	m, err := Build(f, o)
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func testStates(t *testing.T, nums map[string]float64, texts map[string]string) States {
	t.Helper()
	st := state.New(nil)
	for k, v := range nums {
		st.SetNumber(k, v)
	}
	for k, v := range texts {
		st.SetText(k, v)
	}
	st.SetText("0f000000-0000-0001-ffff000000000001", "2026-01-02 03:04:05 +01:00:00")
	return States{Map: testMap(t, testOverlay), Store: st}
}

const moodList = `[{"name":"Kuchyně","id":1},{"name":"Bright","id":777},{"name":"Off","id":778,"static":true}]`

func TestOverlayRejectsTyposAndDuplicates(t *testing.T) {
	for name, in := range map[string]string{
		"unknown key": `devices: {"x": {id: a, safetyy: control}}`,
		"bad safety":  `devices: {"x": {id: a, safety: write}}`,
		"dup id":      `devices: {"x": {id: a}, "y": {id: a}}`,
		"bad id":      `devices: {"x": {id: "A B"}}`,
		"range":       `devices: {"x": {id: a, range: {min: 5, max: 1}}}`,
		"room ref":    `devices: {"x": {id: a, room: nowhere}}`,
	} {
		if _, err := ParseOverlay([]byte(in)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	if o, err := ParseOverlay(nil); err != nil || o == nil {
		t.Fatalf("empty overlay: %v", err)
	}
}

func TestDefaultsAreReadOnly(t *testing.T) {
	m := testMap(t, "")
	for _, d := range m.Devices {
		if d.Writable() == nil {
			t.Errorf("%s writable without an overlay", d.ID)
		}
	}
}

func TestSafetyEnforcedByKind(t *testing.T) {
	m := testMap(t, testOverlay)
	byID := func(id string) *Device {
		d, err := m.Resolve(id)
		if err != nil {
			t.Fatal(err)
		}
		return d
	}
	if err := byID("house.smoke").Writable(); err == nil {
		t.Fatal("smoke alarm writable although the overlay says control")
	}
	if err := byID("bedroom.light").Writable(); err == nil {
		t.Fatal("read device writable")
	}
	if err := byID("vent.supply").Writable(); err == nil {
		t.Fatal("slider without overlay range writable")
	}
	if err := byID("living.light").Writable(); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Resolve("house.door"); err == nil {
		t.Fatal("hidden device resolvable")
	}
}

func TestResolveNames(t *testing.T) {
	m := testMap(t, testOverlay)
	for q, want := range map[string]string{
		"living.light":          "living.light",
		"Living Light":          "living.light",
		"Light Living":          "living.light", // Miniserver name
		"zaluzie loznice":       "bedroom.blind",
		"bedroom blind left":    "bedroom.blind", // room + Miniserver name
		"Blind left 02 Bedroom": "bedroom.blind",
	} {
		d, err := m.Resolve(q)
		if err != nil || d.ID != want {
			t.Errorf("%q: %v %v, want %s", q, d, err, want)
		}
	}
	// two controls share the Miniserver name "Blind left"
	if _, err := m.Resolve("blind left"); err == nil || !strings.Contains(err.Error(), "several") {
		t.Fatalf("ambiguous name: %v", err)
	}
	_, err := m.Resolve("livng light")
	var nf *NotFoundError
	if !errors.As(err, &nf) || len(nf.Matches) == 0 || nf.Matches[0] != "living.light" {
		t.Fatalf("suggestions: %v", err)
	}
}

func TestResolveScope(t *testing.T) {
	m := testMap(t, testOverlay)
	s, err := m.ResolveScope("all", KindBlind)
	if err != nil || len(s.Devices) != 2 {
		t.Fatalf("all blinds: %+v %v", s, err)
	}
	s, err = m.ResolveScope("lounge", KindBlind)
	if err != nil || len(s.Devices) != 1 || s.Devices[0].ID != "living.blind" {
		t.Fatalf("room: %+v %v", s, err)
	}
	// the group controller expands to its writable members
	s, err = m.ResolveScope("All blinds", KindBlind)
	if err != nil || len(s.Devices) != 2 {
		t.Fatalf("group: %+v %v", s, err)
	}
	s, err = m.ResolveScope("all", KindLight)
	if err != nil || len(s.Devices) != 1 || len(s.Skipped) != 1 {
		t.Fatalf("lights: %+v %v", s, err)
	}
	if _, err := m.ResolveScope("bedroom", KindLight); err == nil {
		t.Fatal("room with only read-only lights resolved")
	}
	if _, err := m.ResolveScope("living.light", KindBlind); err == nil {
		t.Fatal("a light as a blind target")
	}
	if _, err := m.ResolveScope("everything", KindLight); err == nil {
		t.Fatal("only the literal all means all")
	}
}

func TestLightCommands(t *testing.T) {
	st := testStates(t, nil, map[string]string{
		"10000000-0000-0001-ffff0000000000a3": moodList,
		"10000000-0000-0001-ffff0000000000a2": "[778]",
		"10000000-0000-0001-ffff0000000000a4": "[1,778]",
	})
	d, _ := st.Map.Resolve("living.light")
	c, err := st.LightCommand(d, LightAction{Action: "on"})
	if err != nil || c.Cmd != "changeTo/1" || c.NoChange {
		t.Fatalf("on: %+v %v", c, err)
	}
	c, err = st.LightCommand(d, LightAction{Action: "off"})
	if err != nil || c.Cmd != "changeTo/778" || !c.NoChange {
		t.Fatalf("off when off: %+v %v", c, err)
	}
	for _, q := range []string{"kitchen", "Kuchyne", "777", "bright"} {
		if _, err := st.LightCommand(d, LightAction{Action: "mood", Mood: q}); err != nil {
			t.Errorf("mood %q: %v", q, err)
		}
	}
	if _, err := st.LightCommand(d, LightAction{Action: "mood", Mood: "disco"}); err == nil {
		t.Fatal("unknown mood accepted")
	}
	if got := st.Summary(d); got["on"] != false {
		t.Fatalf("summary %v", got)
	}
	ro, _ := st.Map.Resolve("bedroom.light")
	if _, err := st.LightCommand(ro, LightAction{Action: "off"}); err == nil {
		t.Fatal("read-only light commanded")
	}
}

func TestBlindCommands(t *testing.T) {
	st := testStates(t, map[string]float64{"20000000-0000-0001-ffff0000000000a4": 0, "20000000-0000-0001-ffff0000000000a2": 0, "20000000-0000-0001-ffff0000000000a3": 0}, nil)
	d, _ := st.Map.Resolve("living.blind")
	p := func(f float64) *float64 { return &f }
	cases := []struct {
		a    BlindAction
		want string
	}{
		{BlindAction{Action: "close"}, "FullDown"},
		{BlindAction{Action: "stop"}, "stop"},
		{BlindAction{Action: "position", Position: p(40)}, "manualPosition/40"},
		{BlindAction{Action: "position", Position: p(40), Slats: p(100)}, "manualPosBlind/40/100"},
		{BlindAction{Action: "slats", Slats: p(30.4)}, "manualLamelle/30"},
		{BlindAction{Action: "automatic_on"}, "auto"},
	}
	for _, c := range cases {
		got, err := st.BlindCommand(d, c.a)
		if err != nil || got.Cmd != c.want {
			t.Errorf("%+v: %+v %v", c.a, got, err)
		}
	}
	if c, _ := st.BlindCommand(d, BlindAction{Action: "open"}); !c.NoChange {
		t.Error("open when open is a change")
	}
	for _, a := range []BlindAction{
		{Action: "position"},
		{Action: "position", Position: p(101)},
		{Action: "slats", Slats: p(-1)},
		{Action: "up"},
		{Action: "close", Position: p(3)},
	} {
		if _, err := st.BlindCommand(d, a); err == nil {
			t.Errorf("%+v accepted", a)
		}
	}
	st.Store.SetNumber("20000000-0000-0001-ffff0000000000a8", 1)
	st.Store.SetText("20000000-0000-0001-ffff0000000000a9", "window open")
	if _, err := st.BlindCommand(d, BlindAction{Action: "close"}); err == nil || !strings.Contains(err.Error(), "window open") {
		t.Fatalf("locked blind: %v", err)
	}
	if _, err := st.BlindCommand(d, BlindAction{Action: "stop"}); err != nil {
		t.Fatalf("stop of a locked blind: %v", err)
	}
}

func TestClimateCommands(t *testing.T) {
	st := testStates(t, map[string]float64{"30000000-0000-0001-ffff0000000000a4": 22}, map[string]string{
		"30000000-0000-0001-ffff0000000000a9": "[]",
	})
	d, _ := st.Map.Resolve("living.climate")
	p := func(f float64) *float64 { return &f }
	now := time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC) // 04:00 at the Miniserver (+01:00)
	// 2026-01-02 08:00 local = days since 2009-01-01 local
	wantUntil := time.Date(2026, 1, 2, 8, 0, 0, 0, time.UTC).Sub(time.Date(2009, 1, 1, 0, 0, 0, 0, time.UTC)) / time.Second
	c, err := st.ClimateCommand(d, ClimateAction{Action: "temperature", Temperature: p(21.3)}, now)
	if err != nil || c.Cmd != "override/3/"+formatNumber(float64(wantUntil))+"/21.5" {
		t.Fatalf("temperature: %+v %v (want until %d)", c, err, wantUntil)
	}
	c, err = st.ClimateCommand(d, ClimateAction{Action: "override", Mode: "eco", Minutes: 60}, now)
	if err != nil || !strings.HasPrefix(c.Cmd, "override/0/") {
		t.Fatalf("eco: %+v %v", c, err)
	}
	c, err = st.ClimateCommand(d, ClimateAction{Action: "comfort_setpoint", Temperature: p(22)}, now)
	if err != nil || c.Cmd != "setComfortTemperature/22" || !c.NoChange {
		t.Fatalf("setpoint: %+v %v", c, err)
	}
	for _, a := range []ClimateAction{
		{Action: "temperature", Temperature: p(30)},
		{Action: "temperature"},
		{Action: "temperature", Temperature: p(21), Minutes: 5000},
		{Action: "override", Mode: "manual"},
		{Action: "override", Mode: "comfort", Temperature: p(21)},
		{Action: "heat"},
	} {
		if _, err := st.ClimateCommand(d, a, now); err == nil {
			t.Errorf("%+v accepted", a)
		}
	}
}

func TestDeviceCommands(t *testing.T) {
	st := testStates(t, map[string]float64{
		"40000000-0000-0001-ffff0000000000a2": 0,
		"50000000-0000-0001-ffff0000000000a2": 3,
	}, nil)
	towel, _ := st.Map.Resolve("towel rail")
	if c, err := st.DeviceCommand(towel, DeviceAction{Action: "on"}); err != nil || c.Cmd != "on" {
		t.Fatalf("towel: %+v %v", c, err)
	}
	if _, err := st.DeviceCommand(towel, DeviceAction{Action: "select", Option: "x"}); err == nil {
		t.Fatal("select on a switch")
	}
	fan, _ := st.Map.Resolve("vent.speed")
	for q, want := range map[string]string{"minimum": "1", "MIN": "1", "2": "2", "off": "reset", "OFF": "reset"} {
		c, err := st.DeviceCommand(fan, DeviceAction{Action: "select", Option: q})
		if err != nil || c.Cmd != want {
			t.Errorf("option %q: %+v %v", q, c, err)
		}
	}
	if c, _ := st.DeviceCommand(fan, DeviceAction{Action: "select", Option: "normal"}); !c.NoChange {
		t.Error("selecting the current option is a change")
	}
	if _, err := st.DeviceCommand(fan, DeviceAction{Action: "select", Option: "turbo"}); err == nil {
		t.Fatal("unknown option accepted")
	}
	// the overlay says control, but a meter is a text and never writable
	water, _ := st.Map.Resolve("house.water")
	if _, err := st.DeviceCommand(water, DeviceAction{Action: "on"}); err == nil {
		t.Fatal("text device commanded")
	}
}

func TestSliderRange(t *testing.T) {
	m := testMap(t, testOverlay) // no range in the base overlay: read-only
	d, _ := m.Resolve("vent.supply")
	if d.Writable() == nil {
		t.Fatal("slider without range writable")
	}
	o := strings.Replace(testOverlay, `{id: vent.supply, safety: control}`, `{id: vent.supply, safety: control, range: {min: 16, max: 24}}`, 1)
	st := States{Map: testMap(t, o), Store: state.New(nil)}
	d, _ = st.Map.Resolve("vent.supply")
	p := func(f float64) *float64 { return &f }
	if c, err := st.DeviceCommand(d, DeviceAction{Action: "set", Value: p(19.4)}); err != nil || c.Cmd != "19" {
		t.Fatalf("set: %+v %v", c, err)
	}
	if _, err := st.DeviceCommand(d, DeviceAction{Action: "set", Value: p(28)}); err == nil {
		t.Fatal("value beyond the overlay range accepted (slider allows it)")
	}
}

func TestNamesAreCleaned(t *testing.T) {
	if got := cleanText("a\x00b\u202e\n  c"); got != "a b c" {
		t.Fatalf("%q", got)
	}
	if got := len([]rune(cleanText(strings.Repeat("x", 500)))); got != maxNameLen {
		t.Fatalf("length %d", got)
	}
	if normalize("Žaluzie  levá – Obývák!") != "zaluzie leva obyvak" {
		t.Fatal(normalize("Žaluzie  levá – Obývák!"))
	}
}

func TestOverviewTimes(t *testing.T) {
	st := testStates(t, map[string]float64{"70000000-0000-0003-ffff0000000000a1": 1, "70000000-0000-0003-ffff0000000000a2": 0},
		map[string]string{"70000000-0000-0002-ffff0000000000a2": "20240131090932|20240307110459"})
	if z := st.Zone(); z.String() != "miniserver" {
		t.Fatalf("zone %v", z)
	}
	_, off := time.Now().In(st.Zone()).Zone()
	if off != 3600 {
		t.Fatalf("offset %d", off)
	}
	if got := st.FromMiniserver(0).Format(time.RFC3339); got != "2009-01-01T00:00:00+01:00" {
		t.Fatal(got)
	}
	if st.ToMiniserver(st.FromMiniserver(12345)) != 12345 {
		t.Fatal("round trip")
	}
}

func TestGenerateOverlayParses(t *testing.T) {
	f, _ := structure.Parse(fakems.Structure)
	out := GenerateOverlay(f)
	o, err := ParseOverlay([]byte(out))
	if err != nil {
		t.Fatalf("generated overlay does not parse: %v\n%s", err, out)
	}
	if _, err := Build(f, o); err != nil {
		t.Fatal(err)
	}
	for _, d := range o.Devices {
		if d.Safety != SafetyRead {
			t.Fatal("generated overlay allows a write")
		}
	}
}

func TestNormalizeConcurrently(t *testing.T) {
	done := make(chan struct{})
	for range 8 {
		go func() {
			defer func() { done <- struct{}{} }()
			for range 500 {
				if normalize("Žaluzie levá Obývák") != "zaluzie leva obyvak" {
					t.Error("wrong fold")
					return
				}
			}
		}()
	}
	for range 8 {
		<-done
	}
}

func TestUnknownStateIsNeverNoChange(t *testing.T) {
	st := testStates(t, nil, map[string]string{"10000000-0000-0001-ffff0000000000a3": moodList})
	light, _ := st.Map.Resolve("living.light")
	if c, err := st.LightCommand(light, LightAction{Action: "off"}); err != nil || c.NoChange {
		t.Fatalf("off with unknown moods: %+v %v", c, err)
	}
	blind, _ := st.Map.Resolve("living.blind")
	if c, err := st.BlindCommand(blind, BlindAction{Action: "open"}); err != nil || c.NoChange {
		t.Fatalf("open with unknown position: %+v %v", c, err)
	}
	cl, _ := st.Map.Resolve("living.climate")
	if c, err := st.ClimateCommand(cl, ClimateAction{Action: "stop_override"}, time.Now()); err != nil || c.NoChange {
		t.Fatalf("stop_override with unknown overrides: %+v %v", c, err)
	}
}

func TestLockState(t *testing.T) {
	st := testStates(t, nil, map[string]string{
		"10000000-0000-0001-ffff0000000000a3": moodList,
		"10000000-0000-0001-ffff0000000000a1": `{"locked":0,"reason":""}`,
	})
	d, _ := st.Map.Resolve("living.light")
	if _, err := st.LightCommand(d, LightAction{Action: "off"}); err != nil {
		t.Fatalf("unlocked refused: %v", err)
	}
	st.Store.SetText("10000000-0000-0001-ffff0000000000a1", `{"locked":1,"reason":"maintenance"}`)
	if _, err := st.LightCommand(d, LightAction{Action: "off"}); err == nil || !strings.Contains(err.Error(), "maintenance") {
		t.Fatalf("locked accepted: %v", err)
	}
}

func TestTimedOverrideNeedsClock(t *testing.T) {
	st := States{Map: testMap(t, testOverlay), Store: state.New(nil)}
	d, _ := st.Map.Resolve("living.climate")
	tmp := 21.0
	if _, err := st.ClimateCommand(d, ClimateAction{Action: "temperature", Temperature: &tmp}, time.Now()); err == nil {
		t.Fatal("override without the Miniserver clock")
	}
}

func TestHiddenRoomMembersAreNotCommanded(t *testing.T) {
	o := strings.Replace(testOverlay, `{id: bedroom, name: Bedroom}`, `{id: bedroom, name: Bedroom, hidden: true}`, 1)
	m := testMap(t, o)
	s, err := m.ResolveScope("All blinds", KindBlind)
	if err != nil || len(s.Devices) != 1 || s.Devices[0].ID != "living.blind" {
		t.Fatalf("group with a hidden-room member: %+v %v", s, err)
	}
}
