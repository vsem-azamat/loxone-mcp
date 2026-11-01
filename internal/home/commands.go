package home

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"
)

// Command is one control command, ready for jdev/sps/io/<UUID>/<Cmd>. Cmd is always built
// here from validated parts, never taken from the caller.
type Command struct {
	UUID string
	Cmd  string
	// NoChange is set when the device is already in the requested state; nothing is sent.
	NoChange bool
}

// checkLive refuses a device the Miniserver currently locks against commands.
func (s States) checkLive(d *Device) error {
	t, ok := s.text(d, "jLocked")
	if !ok || strings.TrimSpace(t) == "" {
		return nil
	}
	var l struct {
		Locked int    `json:"locked"`
		Reason string `json:"reason"`
	}
	if err := json.Unmarshal([]byte(t), &l); err != nil {
		return fmt.Errorf("%s has an unreadable lock state on the Miniserver; not changed", d.ID)
	}
	if l.Locked != 0 {
		why := cleanText(l.Reason)
		if why == "" {
			why = "no reason given"
		}
		return fmt.Errorf("%s is locked on the Miniserver (%s)", d.ID, why)
	}
	return nil
}

func formatNumber(f float64) string { return strconv.FormatFloat(f, 'f', -1, 64) }

func finite(f float64) bool { return !math.IsNaN(f) && !math.IsInf(f, 0) }

// LightAction is the action of set_lights.
type LightAction struct {
	Action string // on, off, mood
	Mood   string
}

// LightCommand builds the command of a light action.
func (s States) LightCommand(d *Device, a LightAction) (Command, error) {
	if err := d.Writable(); err != nil {
		return Command{}, err
	}
	if d.Kind != KindLight {
		return Command{}, fmt.Errorf("%s is not a light", d.ID)
	}
	if err := s.checkLive(d); err != nil {
		return Command{}, err
	}
	active := s.activeMoods(d)
	_, known := s.text(d, "activeMoods") // NoChange only when the current state is known
	only := func(id int) bool { return known && len(active) == 1 && active[0] == id }
	moods := s.moods(d)
	has := func(id int) bool {
		for _, m := range moods {
			if m.ID == id {
				return true
			}
		}
		return false
	}
	switch a.Action {
	case "off":
		if !has(offMoodID) && len(moods) > 0 {
			return Command{}, fmt.Errorf("%s has no off mood", d.ID)
		}
		return Command{UUID: d.UUID, Cmd: "changeTo/" + strconv.Itoa(offMoodID), NoChange: known && !lightOn(active)}, nil
	case "on":
		if known && lightOn(active) {
			return Command{UUID: d.UUID, Cmd: "changeTo/" + strconv.Itoa(active[0]), NoChange: true}, nil
		}
		t, _ := s.text(d, "favoriteMoods")
		var fav []int
		_ = json.Unmarshal([]byte(t), &fav)
		for _, id := range fav {
			if id != offMoodID && has(id) {
				return Command{UUID: d.UUID, Cmd: "changeTo/" + strconv.Itoa(id)}, nil
			}
		}
		for _, m := range moods {
			if m.ID != offMoodID {
				return Command{UUID: d.UUID, Cmd: "changeTo/" + strconv.Itoa(m.ID)}, nil
			}
		}
		return Command{}, fmt.Errorf("%s has no mood that turns it on", d.ID)
	case "mood":
		if strings.TrimSpace(a.Mood) == "" {
			return Command{}, errors.New("action mood needs mood=<name>; get_device lists the light's moods")
		}
		m, err := s.FindMood(d, a.Mood)
		if err != nil {
			return Command{}, err
		}
		return Command{UUID: d.UUID, Cmd: "changeTo/" + strconv.Itoa(m.ID), NoChange: only(m.ID)}, nil
	default:
		return Command{}, fmt.Errorf("unknown light action %q (on, off, mood)", a.Action)
	}
}

// BlindAction is the action of set_blinds.
type BlindAction struct {
	Action   string   // open, close, stop, shade, automatic_on, automatic_off, position, slats
	Position *float64 // % closed, 0..100
	Slats    *float64 // 0 horizontal .. 100 vertical
}

func percentArg(name string, v *float64) (int, error) {
	if v == nil {
		return 0, fmt.Errorf("%s is required", name)
	}
	if !finite(*v) || *v < 0 || *v > 100 {
		return 0, fmt.Errorf("%s must be 0..100", name)
	}
	return int(math.Round(*v)), nil
}

// BlindCommand builds the command of a blind action. A blind that is locked or in a safety
// shutdown is refused.
func (s States) BlindCommand(d *Device, a BlindAction) (Command, error) {
	if err := d.Writable(); err != nil {
		return Command{}, err
	}
	if d.Kind != KindBlind {
		return Command{}, fmt.Errorf("%s is not a blind", d.ID)
	}
	if err := s.checkLive(d); err != nil {
		return Command{}, err
	}
	if why := s.blindBlocked(d); why != "" && a.Action != "stop" {
		return Command{}, fmt.Errorf("%s does not move now (%s)", d.ID, why)
	}
	if a.Slats != nil && d.animation != 0 {
		return Command{}, fmt.Errorf("%s has no slats", d.ID)
	}
	simple := map[string]string{"open": "FullUp", "close": "FullDown", "stop": "stop", "shade": "shade", "automatic_on": "auto", "automatic_off": "NoAuto"}
	if cmd, ok := simple[a.Action]; ok {
		if a.Position != nil || a.Slats != nil {
			return Command{}, fmt.Errorf("action %s takes no position or slats", a.Action)
		}
		c := Command{UUID: d.UUID, Cmd: cmd}
		pos, posOK := s.num(d, "position")
		up, upOK := s.flag(d, "up")
		down, downOK := s.flag(d, "down")
		still := posOK && upOK && downOK && !up && !down
		switch a.Action {
		case "open":
			c.NoChange = still && pos <= 0.005
		case "close":
			c.NoChange = still && pos >= 0.995
		case "automatic_on", "automatic_off":
			if auto, ok := s.flag(d, "autoActive"); ok {
				c.NoChange = auto == (a.Action == "automatic_on")
			}
		}
		return c, nil
	}
	switch a.Action {
	case "position":
		p, err := percentArg("position", a.Position)
		if err != nil {
			return Command{}, err
		}
		if a.Slats != nil {
			sl, err := percentArg("slats", a.Slats)
			if err != nil {
				return Command{}, err
			}
			return Command{UUID: d.UUID, Cmd: fmt.Sprintf("manualPosBlind/%d/%d", p, sl)}, nil
		}
		return Command{UUID: d.UUID, Cmd: fmt.Sprintf("manualPosition/%d", p)}, nil
	case "slats":
		if a.Position != nil {
			return Command{}, errors.New("action slats takes no position; use action position with both")
		}
		sl, err := percentArg("slats", a.Slats)
		if err != nil {
			return Command{}, err
		}
		return Command{UUID: d.UUID, Cmd: fmt.Sprintf("manualLamelle/%d", sl)}, nil
	default:
		return Command{}, fmt.Errorf("unknown blind action %q (open, close, stop, shade, automatic_on, automatic_off, position, slats)", a.Action)
	}
}

// ClimateAction is the action of set_climate.
type ClimateAction struct {
	Action      string // temperature, comfort_setpoint, override, stop_override
	Temperature *float64
	Mode        string // comfort, eco (for override)
	Minutes     int    // for temperature and override; 0 = DefaultOverrideMinutes
}

// Override durations.
const (
	DefaultOverrideMinutes = 240
	MaxOverrideMinutes     = 24 * 60
)

func (d *Device) checkTemperature(t *float64) (string, error) {
	if t == nil {
		return "", errors.New("temperature is required")
	}
	lo, hi := d.TempRange()
	if !finite(*t) || *t < lo || *t > hi {
		return "", fmt.Errorf("temperature must be %s..%s °C for %s", formatNumber(lo), formatNumber(hi), d.ID)
	}
	return formatNumber(math.Round(*t*2) / 2), nil
}

// ClimateCommand builds the command of a climate action.
func (s States) ClimateCommand(d *Device, a ClimateAction, now time.Time) (Command, error) {
	if err := d.Writable(); err != nil {
		return Command{}, err
	}
	if d.Kind != KindClimate {
		return Command{}, fmt.Errorf("%s is not a room climate controller", d.ID)
	}
	if err := s.checkLive(d); err != nil {
		return Command{}, err
	}
	minutes := a.Minutes
	if minutes == 0 {
		minutes = DefaultOverrideMinutes
	}
	if (a.Action == "temperature" || a.Action == "override") && (minutes < 1 || minutes > MaxOverrideMinutes) {
		return Command{}, fmt.Errorf("minutes must be 1..%d", MaxOverrideMinutes)
	}
	if (a.Action == "temperature" || a.Action == "override") && !s.ZoneKnown() {
		return Command{}, errors.New("the Miniserver's clock is not known yet, so an end time cannot be computed; retry in a minute")
	}
	until := s.ToMiniserver(now.Add(time.Duration(minutes) * time.Minute))
	switch a.Action {
	case "temperature":
		t, err := d.checkTemperature(a.Temperature)
		if err != nil {
			return Command{}, err
		}
		return Command{UUID: d.UUID, Cmd: fmt.Sprintf("override/3/%d/%s", until, t)}, nil
	case "comfort_setpoint":
		t, err := d.checkTemperature(a.Temperature)
		if err != nil {
			return Command{}, err
		}
		cur, ok := s.num(d, "comfortTemperature")
		return Command{UUID: d.UUID, Cmd: "setComfortTemperature/" + t, NoChange: ok && formatNumber(cur) == t}, nil
	case "override":
		if a.Temperature != nil {
			return Command{}, errors.New("override takes no temperature; use action temperature for a given temperature")
		}
		id, ok := map[string]int{"eco": 0, "comfort": 1}[a.Mode]
		if !ok {
			return Command{}, fmt.Errorf("unknown override mode %q (comfort, eco)", a.Mode)
		}
		return Command{UUID: d.UUID, Cmd: fmt.Sprintf("override/%d/%d", id, until)}, nil
	case "stop_override":
		_, known := s.text(d, "overrideEntries")
		return Command{UUID: d.UUID, Cmd: "stopOverride", NoChange: known && len(s.overrides(d)) == 0}, nil
	default:
		return Command{}, fmt.Errorf("unknown climate action %q (temperature, comfort_setpoint, override, stop_override)", a.Action)
	}
}

// DeviceAction is the action of set_device.
type DeviceAction struct {
	Action string // on, off, select, set
	Option string
	Value  *float64
}

// DeviceCommand builds the command for a switch, an option or a value device.
func (s States) DeviceCommand(d *Device, a DeviceAction) (Command, error) {
	if err := d.Writable(); err != nil {
		return Command{}, err
	}
	if err := s.checkLive(d); err != nil {
		return Command{}, err
	}
	switch d.Kind {
	case KindSwitch:
		if a.Action != "on" && a.Action != "off" {
			return Command{}, fmt.Errorf("%s is a switch: action on or off", d.ID)
		}
		if a.Action == "off" {
			if lockedOn, _ := s.flag(d, "lockedOn"); lockedOn {
				return Command{}, fmt.Errorf("%s is held on by the Miniserver's logic and cannot be switched off", d.ID)
			}
		}
		cur, ok := s.flag(d, "active")
		return Command{UUID: d.UUID, Cmd: a.Action, NoChange: ok && cur == (a.Action == "on")}, nil
	case KindOption:
		if a.Action != "select" {
			return Command{}, fmt.Errorf("%s is an option: action select with option", d.ID)
		}
		q := normalize(a.Option)
		if q == "" {
			return Command{}, errors.New("option is required")
		}
		cur, curOK := s.num(d, "activeOutput")
		denied := func(id int, name string) error {
			return fmt.Errorf("%q is not allowed for agents on %s (the home map forbids it); tell the user to use the Loxone app", name, d.ID)
		}
		if d.allOff != "" && (q == normalize(d.allOff) || q == "off" || q == "none" || q == "0") {
			if d.denied[0] {
				return Command{}, denied(0, d.allOff)
			}
			return Command{UUID: d.UUID, Cmd: "reset", NoChange: curOK && cur == 0}, nil
		}
		cands := map[string]string{}
		for _, o := range d.options {
			if q == normalize(o.Name) || q == normalize(o.SourceName) || q == strconv.Itoa(o.ID) {
				if d.denied[o.ID] {
					return Command{}, denied(o.ID, o.Name)
				}
				return Command{UUID: d.UUID, Cmd: strconv.Itoa(o.ID), NoChange: curOK && int(cur) == o.ID}, nil
			}
			cands[o.Name] = o.Name
			cands[o.SourceName] = o.Name
		}
		if d.allOff != "" {
			cands[d.allOff] = d.allOff
		}
		m := suggest(a.Option, cands, 5)
		if len(m) == 0 {
			for _, o := range d.options {
				m = append(m, o.Name)
			}
		}
		return Command{}, &NotFoundError{What: "option of " + d.ID, Query: a.Option, Matches: m}
	case KindValue:
		if a.Action != "set" {
			return Command{}, fmt.Errorf("%s is a value: action set with value", d.ID)
		}
		if a.Value == nil || !finite(*a.Value) {
			return Command{}, errors.New("value is required")
		}
		lo, hi, step := d.ValueRange()
		if *a.Value < lo || *a.Value > hi {
			return Command{}, fmt.Errorf("value must be %s..%s for %s", formatNumber(lo), formatNumber(hi), d.ID)
		}
		base := d.slider.Min // steps count from the slider's own minimum
		v := math.Round((*a.Value-base)/step)*step + base
		// stay on the slider's grid inside the allowed range
		if v < lo {
			v = math.Ceil((lo-base)/step)*step + base
		}
		if v > hi {
			v = math.Floor((hi-base)/step)*step + base
		}
		if v < lo || v > hi {
			return Command{}, fmt.Errorf("no value of %s's step %s lies in %s..%s", d.ID, formatNumber(step), formatNumber(lo), formatNumber(hi))
		}
		v = math.Round(v*1000) / 1000
		cur, ok := s.num(d, "value")
		return Command{UUID: d.UUID, Cmd: formatNumber(v), NoChange: ok && cur == v}, nil
	default:
		return Command{}, fmt.Errorf("%s is a %s; set_device changes switches, options and values", d.ID, d.Kind)
	}
}
