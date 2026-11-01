package home

import (
	"encoding/json"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/vsem-azamat/loxone-mcp/internal/state"
)

// Mood id of the static "off" mood of every LightControllerV2.
const offMoodID = 778

// States reads the current values of a map's devices.
type States struct {
	Map   *Map
	Store *state.Store
}

func (s States) value(d *Device, name string) (state.Value, bool) {
	uuid := d.control.State(name)
	if uuid == "" {
		return state.Value{}, false
	}
	return s.Store.Get(uuid)
}

func (s States) num(d *Device, name string) (float64, bool) {
	v, ok := s.value(d, name)
	if !ok || v.IsText {
		return 0, false
	}
	return v.Number, true
}

func (s States) text(d *Device, name string) (string, bool) {
	v, ok := s.value(d, name)
	if !ok || !v.IsText {
		return "", false
	}
	return v.Text, true
}

func (s States) flag(d *Device, name string) (bool, bool) {
	n, ok := s.num(d, name)
	return n != 0, ok
}

// ZoneKnown reports whether the Miniserver has published its clock, so its zone is known.
func (s States) ZoneKnown() bool {
	v, ok := s.Store.Get(s.Map.GlobalStates["miniserverTime"])
	return ok && v.IsText && parseZone(v.Text) != nil
}

// Zone is the Miniserver's time zone, taken from its clock state; the local zone when unknown.
func (s States) Zone() *time.Location {
	uuid := s.Map.GlobalStates["miniserverTime"]
	if v, ok := s.Store.Get(uuid); ok && v.IsText {
		if z := parseZone(v.Text); z != nil {
			return z
		}
	}
	return time.Local
}

var zonePattern = regexp.MustCompile(`([+-])(\d{2}):(\d{2})(?::\d{2})?\s*$`)

func parseZone(s string) *time.Location {
	m := zonePattern.FindStringSubmatch(s)
	if m == nil {
		return nil
	}
	h, _ := strconv.Atoi(m[2])
	mi, _ := strconv.Atoi(m[3])
	off := h*3600 + mi*60
	if m[1] == "-" {
		off = -off
	}
	return time.FixedZone("miniserver", off)
}

// FromMiniserver converts Miniserver seconds since 2009 (its local wall clock) to a time.
func (s States) FromMiniserver(sec float64) time.Time {
	z := s.Zone()
	return time.Date(2009, 1, 1, 0, 0, 0, 0, z).Add(time.Duration(sec) * time.Second)
}

// ToMiniserver converts a time to Miniserver seconds since 2009.
func (s States) ToMiniserver(t time.Time) int64 {
	z := s.Zone()
	return int64(t.In(z).Sub(time.Date(2009, 1, 1, 0, 0, 0, 0, z)) / time.Second)
}

func round1(f float64) float64 { return math.Round(f*10) / 10 }

func percent(f float64) int { return int(math.Round(f * 100)) }

// Summary is a short view of a device's state for overviews and lists.
func (s States) Summary(d *Device) map[string]any {
	out := map[string]any{}
	switch d.Kind {
	case KindLight:
		active := s.activeMoods(d)
		out["on"] = lightOn(active)
		if names := s.moodNamesOf(d, active); lightOn(active) && len(names) > 0 {
			out["moods"] = names
		}
	case KindBlind:
		if p, ok := s.num(d, "position"); ok {
			out["closed_percent"] = percent(p)
		}
		if p, ok := s.num(d, "shadePosition"); ok {
			out["slats_percent"] = percent(p)
		}
		if up, _ := s.flag(d, "up"); up {
			out["moving"] = "up"
		} else if down, _ := s.flag(d, "down"); down {
			out["moving"] = "down"
		}
		if out["moving"] != nil {
			if t, ok := s.num(d, "targetPosition"); ok {
				out["target_closed_percent"] = percent(t)
			}
		}
		if a, ok := s.flag(d, "autoActive"); ok {
			out["automatic"] = a
		}
		if why := s.blindBlocked(d); why != "" {
			out["blocked"] = why
		}
	case KindClimate:
		s.climateSummary(d, out)
	case KindSwitch:
		if v, ok := s.flag(d, "active"); ok {
			out["on"] = v
		}
		if v, _ := s.flag(d, "lockedOn"); v {
			out["locked_on"] = true
		}
	case KindOption:
		if n, ok := s.num(d, "activeOutput"); ok {
			out["selected"] = d.optionName(int(n))
		}
	case KindValue:
		if n, ok := s.num(d, "value"); ok {
			out["value"] = round1(n)
			if d.Unit != "" {
				out["unit"] = d.Unit
			}
		}
	case KindText:
		if t, ok := s.text(d, "textAndIcon"); ok {
			out["text"] = cleanLong(t, 200)
		}
	case KindSmokeAlarm:
		lvl, _ := s.num(d, "level")
		out["alarm"] = lvl != 0
		if lvl != 0 {
			out["level"] = int(lvl)
			if c, ok := s.num(d, "alarmCause"); ok {
				out["cause"] = int(c)
			}
		}
		if t, _ := s.flag(d, "testAlarm"); t {
			out["test_alarm"] = true
		}
	case KindPresence:
		if v, ok := s.flag(d, "active"); ok {
			out["present"] = v
		}
		if since, ok := s.num(d, "activeSince"); ok && since > 0 {
			out["since"] = s.FromMiniserver(since).Format(time.RFC3339)
		}
	case KindIntercom:
		if v, ok := s.flag(d, "bell"); ok {
			out["ringing"] = v
		}
		if bells := s.bells(d); len(bells) > 0 {
			out["last_bell"] = bells[0]
		}
	case KindLightGroup, KindBlindGroup:
		ids := []string{}
		for _, uuid := range d.Members {
			if md := s.Map.byUUID[uuid]; md != nil && md.Safety != SafetyHidden && !md.Room.Hidden {
				ids = append(ids, md.ID)
			}
		}
		out["members"] = ids
	case KindOther:
		for name := range d.control.States {
			if v, ok := s.value(d, name); ok && !v.IsText {
				out[name] = v.Number
			}
		}
	}
	return out
}

func (s States) climateSummary(d *Device, out map[string]any) {
	if v, ok := s.num(d, "tempActual"); ok {
		out["temperature_c"] = round1(v)
	}
	if v, ok := s.num(d, "tempTarget"); ok {
		out["target_temperature_c"] = round1(v)
	}
	// 0 means the sensor is not connected
	if v, ok := s.num(d, "humidityActual"); ok && v > 0 {
		out["humidity_percent"] = round1(v)
	}
	if v, ok := s.num(d, "co2"); ok && v > 0 {
		out["co2_ppm"] = int(math.Round(v))
	}
	if v, ok := s.num(d, "activeMode"); ok {
		out["mode"] = activeModeName(int(v))
	}
	if v, _ := s.flag(d, "openWindow"); v {
		out["window_open"] = true
	}
	if ov := s.overrides(d); len(ov) > 0 {
		out["override"] = ov[0]
	}
}

func activeModeName(m int) string {
	switch m {
	case 0:
		return "eco"
	case 1:
		return "comfort"
	case 2:
		return "building_protection"
	case 3:
		return "manual"
	case 4:
		return "off"
	default:
		return strconv.Itoa(m)
	}
}

func operatingModeName(m int) string {
	switch m {
	case 0:
		return "automatic, heating and cooling"
	case 1:
		return "automatic, heating only"
	case 2:
		return "automatic, cooling only"
	case 3:
		return "manual, heating and cooling"
	case 4:
		return "manual, heating only"
	case 5:
		return "manual, cooling only"
	case -1:
		return "off"
	default:
		return strconv.Itoa(m)
	}
}

var overrideReasons = map[int]string{
	0: "none", 1: "presence (comfort)", 2: "window open (eco+)", 3: "comfort override", 4: "eco override",
	5: "eco+ override", 6: "preparing: heating up", 7: "preparing: cooling down", 8: "overridden by source",
	9: "cooling forbidden", 10: "heating forbidden", 11: "manual", 12: "no cooling available",
	13: "no heating available", 14: "low request", 15: "pause timer", 16: "different mode",
	17: "load shedding", 18: "locked", 19: "device offline", 20: "different mode than central",
}

func (s States) overrides(d *Device) []map[string]any {
	t, ok := s.text(d, "overrideEntries")
	if !ok || t == "" {
		return nil
	}
	var entries []struct {
		Start   float64 `json:"start"`
		End     float64 `json:"end"`
		Reason  int     `json:"reason"`
		IsTimer bool    `json:"isTimer"`
	}
	if json.Unmarshal([]byte(t), &entries) != nil {
		return nil
	}
	out := []map[string]any{}
	for _, e := range entries {
		m := map[string]any{"reason": overrideReasons[e.Reason], "started_by_app_timer": e.IsTimer}
		if m["reason"] == nil {
			m["reason"] = strconv.Itoa(e.Reason)
		}
		if e.Start > 0 {
			m["since"] = s.FromMiniserver(e.Start).Format(time.RFC3339)
		}
		if e.End > 0 {
			m["until"] = s.FromMiniserver(e.End).Format(time.RFC3339)
		}
		out = append(out, m)
	}
	return out
}

func (s States) blindBlocked(d *Device) string {
	locked, _ := s.flag(d, "locked")
	safety, _ := s.flag(d, "safetyActive")
	if !locked && !safety {
		return ""
	}
	info, _ := s.text(d, "infoText")
	info = cleanText(info)
	switch {
	case locked && info != "":
		return "locked: " + info
	case locked:
		return "locked"
	case info != "":
		return "safety shutdown: " + info
	default:
		return "safety shutdown"
	}
}

// Mood is a light mood with its id and names.
type Mood struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	SourceName string `json:"source_name,omitempty"`
}

func (s States) moods(d *Device) []Mood {
	t, _ := s.text(d, "moodList")
	var list []struct {
		ID   int    `json:"id"`
		Name string `json:"name"`
	}
	_ = json.Unmarshal([]byte(t), &list)
	out := make([]Mood, 0, len(list))
	for _, m := range list {
		mood := Mood{ID: m.ID, Name: cleanText(m.Name)}
		if n := d.moodNames[strconv.Itoa(m.ID)]; n != "" {
			mood.SourceName = mood.Name
			mood.Name = n
		}
		out = append(out, mood)
	}
	return out
}

func (s States) activeMoods(d *Device) []int {
	t, _ := s.text(d, "activeMoods")
	var ids []int
	_ = json.Unmarshal([]byte(t), &ids)
	return ids
}

func lightOn(active []int) bool {
	for _, id := range active {
		if id != offMoodID {
			return true
		}
	}
	return false
}

func (s States) moodNamesOf(d *Device, ids []int) []string {
	byID := map[int]string{}
	for _, m := range s.moods(d) {
		byID[m.ID] = m.Name
	}
	out := []string{}
	for _, id := range ids {
		if n := byID[id]; n != "" {
			out = append(out, n)
		} else {
			out = append(out, strconv.Itoa(id))
		}
	}
	return out
}

// FindMood resolves a mood of a light by id, name or overlay name.
func (s States) FindMood(d *Device, query string) (Mood, error) {
	moods := s.moods(d)
	q := normalize(query)
	for _, m := range moods {
		if strconv.Itoa(m.ID) == strings.TrimSpace(query) || normalize(m.Name) == q || (m.SourceName != "" && normalize(m.SourceName) == q) {
			return m, nil
		}
	}
	cands := map[string]string{}
	for _, m := range moods {
		cands[m.Name] = m.Name
		if m.SourceName != "" {
			cands[m.SourceName] = m.Name
		}
	}
	matches := suggest(query, cands, 5)
	if len(matches) == 0 {
		for _, m := range moods {
			matches = append(matches, m.Name)
		}
	}
	return Mood{}, &NotFoundError{What: "mood of " + d.ID, Query: query, Matches: matches}
}

func (d *Device) optionName(id int) string {
	if id == 0 {
		if d.allOff != "" {
			return d.allOff
		}
		return "none"
	}
	for _, o := range d.options {
		if o.ID == id {
			return o.Name
		}
	}
	return strconv.Itoa(id)
}

func (s States) bells(d *Device) []string {
	t, _ := s.text(d, "lastBellEvents")
	var out []string
	z := s.Zone()
	for _, p := range strings.Split(t, "|") {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		ts, err := time.ParseInLocation("20060102150405", p, z)
		if err != nil {
			continue
		}
		out = append(out, ts.Format(time.RFC3339))
	}
	sort.Sort(sort.Reverse(sort.StringSlice(out)))
	return out
}

// Bells lists the intercom's recent unanswered bell times, newest first.
func (s States) Bells(d *Device) []string { return s.bells(d) }

// Detail is the full view of one device: identity, summary, decoded details, raw states and
// the actions available.
func (s States) Detail(d *Device) map[string]any {
	out := map[string]any{
		"id":      d.ID,
		"name":    d.Name,
		"kind":    d.Kind,
		"room":    d.Room.ID,
		"state":   s.Summary(d),
		"actions": d.Actions(),
	}
	if d.SourceName != d.Name {
		out["miniserver_name"] = d.SourceName
	}
	if d.Description != "" {
		out["description"] = d.Description
	}
	if len(d.Aliases) > 0 {
		out["aliases"] = d.Aliases
	}
	if len(d.Tags) > 0 {
		out["tags"] = d.Tags
	}
	if err := d.Writable(); err != nil {
		out["writable"] = false
		out["why_read_only"] = err.Error()
	} else {
		out["writable"] = true
	}
	switch d.Kind {
	case KindLight:
		out["moods"] = s.moods(d)
	case KindClimate:
		st := map[string]any{}
		for n, key := range map[string]string{
			"comfortTemperature":       "comfort_heating_c",
			"comfortTemperatureCool":   "comfort_cooling_c",
			"comfortTemperatureOffset": "comfort_offset_c",
			"frostProtectTemperature":  "building_protection_min_c",
			"heatProtectTemperature":   "building_protection_max_c",
			"actualOutdoorTemp":        "outdoor_temperature_c",
		} {
			if v, ok := s.num(d, n); ok && v > -1000 {
				st[key] = round1(v)
			}
		}
		if v, ok := s.num(d, "operatingMode"); ok {
			st["operating_mode"] = operatingModeName(int(v))
		}
		out["climate"] = st
		if ov := s.overrides(d); len(ov) > 0 {
			out["overrides"] = ov
		}
		lo, hi := d.TempRange()
		out["setpoint_range"] = map[string]float64{"min": lo, "max": hi}
	case KindOption:
		opts := []map[string]any{}
		for _, o := range d.options {
			m := map[string]any{"id": o.ID, "name": o.Name}
			if o.SourceName != o.Name {
				m["miniserver_name"] = o.SourceName
			}
			opts = append(opts, m)
		}
		out["options"] = opts
		if d.allOff != "" {
			out["off_option"] = d.allOff
		}
	case KindValue:
		lo, hi, step := d.ValueRange()
		out["range"] = map[string]float64{"min": lo, "max": hi, "step": step}
	case KindIntercom:
		out["recent_bells"] = s.bells(d)
	case KindSmokeAlarm:
		if t, ok := s.text(d, "sensors"); ok {
			out["sensor_log"] = cleanLong(t, 500)
		}
	}
	raw := map[string]any{}
	for name := range d.control.States {
		v, ok := s.value(d, name)
		if !ok {
			continue
		}
		if v.IsText {
			if v.Text != "" {
				raw[name] = cleanLong(v.Text, 500)
			}
		} else {
			raw[name] = v.Number
		}
	}
	out["raw_states"] = raw
	return out
}

// Matches reports whether a free-text query occurs in the device's id, names, aliases,
// description, tags or kind (folded, every word must occur).
func (d *Device) Matches(query string) bool {
	hay := normalize(strings.Join(append(append([]string{d.ID, d.Name, d.SourceName, d.Description, string(d.Kind), d.Room.Name}, d.Aliases...), d.Tags...), " "))
	for _, w := range strings.Fields(normalize(query)) {
		if !strings.Contains(hay, w) {
			return false
		}
	}
	return true
}

// TempRange is the setpoint range a climate device accepts: the overlay's, else 15..26 °C.
func (d *Device) TempRange() (float64, float64) {
	if d.Range != nil {
		return d.Range.Min, d.Range.Max
	}
	return 15, 26
}

// ValueRange is the range and step a value device accepts: the slider's own, narrowed by the
// overlay.
func (d *Device) ValueRange() (lo, hi, step float64) {
	lo, hi, step = d.slider.Min, d.slider.Max, d.slider.Step
	if d.Range != nil {
		lo, hi = math.Max(lo, d.Range.Min), math.Min(hi, d.Range.Max)
		if d.slider.Min == 0 && d.slider.Max == 0 {
			lo, hi = d.Range.Min, d.Range.Max
		}
	}
	if step <= 0 {
		step = 0.5
	}
	return lo, hi, step
}

// Actions describes what a write tool can do with the device.
func (d *Device) Actions() []string {
	if d.Writable() != nil {
		return []string{}
	}
	switch d.Kind {
	case KindLight:
		return []string{"set_lights action=off", "set_lights action=on (first favourite mood)", "set_lights action=mood mood=<name from moods>"}
	case KindBlind:
		acts := []string{"set_blinds action=open|close|stop|shade|automatic_on|automatic_off", "set_blinds action=position position=<0..100 % closed>"}
		if d.animation == 0 {
			acts = append(acts, "set_blinds action=slats slats=<0..100>", "set_blinds action=position position=<%> slats=<%>")
		}
		return acts
	case KindClimate:
		lo, hi := d.TempRange()
		r := strconv.FormatFloat(lo, 'f', -1, 64) + ".." + strconv.FormatFloat(hi, 'f', -1, 64)
		return []string{
			"set_climate action=temperature temperature=<" + r + "> [minutes=<1..1440, default 240>]",
			"set_climate action=override mode=comfort|eco [minutes=<1..1440, default 240>]",
			"set_climate action=stop_override",
			"set_climate action=comfort_setpoint temperature=<" + r + "> (permanent schedule change)",
		}
	case KindSwitch:
		return []string{"set_device action=on", "set_device action=off"}
	case KindOption:
		names := make([]string, 0, len(d.options)+1)
		for _, o := range d.options {
			names = append(names, o.Name)
		}
		if d.allOff != "" {
			names = append(names, d.allOff)
		}
		return []string{"set_device action=select option=<" + strings.Join(names, "|") + ">"}
	case KindValue:
		lo, hi, step := d.ValueRange()
		return []string{"set_device action=set value=<" + strconv.FormatFloat(lo, 'f', -1, 64) + ".." + strconv.FormatFloat(hi, 'f', -1, 64) + ", step " + strconv.FormatFloat(step, 'f', -1, 64) + ">"}
	default:
		return []string{}
	}
}

// StateUUIDs lists the state UUIDs of a device, to watch for changes after a command.
func (d *Device) StateUUIDs() []string {
	out := []string{}
	for _, ref := range d.control.States {
		out = append(out, ref...)
	}
	return out
}
