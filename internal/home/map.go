// Package home is the map of the home that agents see: rooms and devices built from the
// structure file and named by the private overlay, the safety rules that decide what may be
// changed, name resolution, readable views of the current state and the commands of each
// action.
package home

import (
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vsem-azamat/loxone-mcp/internal/structure"
)

// Kind is what a device is, derived from its control type.
type Kind string

// The kinds of devices.
const (
	KindLight      Kind = "light"
	KindLightGroup Kind = "light_group"
	KindBlind      Kind = "blind"
	KindBlindGroup Kind = "blind_group"
	KindClimate    Kind = "climate"
	KindSwitch     Kind = "switch"
	KindOption     Kind = "option"
	KindValue      Kind = "value"
	KindText       Kind = "text"
	KindSmokeAlarm Kind = "smoke_alarm"
	KindPresence   Kind = "presence"
	KindIntercom   Kind = "intercom"
	KindOther      Kind = "other"
)

// Kinds lists every kind, for tool schemas.
var Kinds = []Kind{
	KindLight, KindLightGroup, KindBlind, KindBlindGroup, KindClimate, KindSwitch, KindOption,
	KindValue, KindText, KindSmokeAlarm, KindPresence, KindIntercom, KindOther,
}

// writableKinds are the only kinds a write tool may ever command. Alarms, the intercom,
// presence, texts and group controllers are never written, whatever the overlay says.
var writableKinds = map[Kind]bool{
	KindLight: true, KindBlind: true, KindClimate: true, KindSwitch: true, KindOption: true, KindValue: true,
}

func kindOf(controlType string) Kind {
	switch controlType {
	case "LightControllerV2":
		return KindLight
	case "CentralLightController":
		return KindLightGroup
	case "Jalousie":
		return KindBlind
	case "CentralJalousie":
		return KindBlindGroup
	case "IRoomControllerV2":
		return KindClimate
	case "Switch":
		return KindSwitch
	case "Radio":
		return KindOption
	case "Slider":
		return KindValue
	case "TextState":
		return KindText
	case "SmokeAlarm":
		return KindSmokeAlarm
	case "PresenceDetector":
		return KindPresence
	case "Intercom":
		return KindIntercom
	default:
		return KindOther
	}
}

// Room is a room of the map.
type Room struct {
	UUID       string
	ID         string
	Name       string
	SourceName string // the name in the structure file
	Aliases    []string
	Hidden     bool
}

// Device is one control of the map.
type Device struct {
	UUID        string
	ID          string
	Name        string
	SourceName  string // the name in the structure file
	Description string
	Kind        Kind
	Type        string // the structure file's control type
	Room        *Room
	Category    string
	Aliases     []string
	Safety      string
	Tags        []string
	Overview    bool
	Range       *Range
	Unit        string
	Members     []string // UUIDs of the controls a group commands

	options   []Option          // Radio outputs, ordered by id
	allOff    string            // Radio: the label of "no output", "" when there is none
	moodNames map[string]string // overlay names of light moods, by id
	slider    sliderDetails
	animation int // Jalousie: 0 = blinds with slats
	control   *structure.Control
}

// Option is one output of a Radio control.
type Option struct {
	ID         int
	Name       string
	SourceName string
}

type sliderDetails struct {
	Min, Max, Step float64
	Format         string
}

// HasTag reports whether the device carries the tag.
func (d *Device) HasTag(tag string) bool {
	for _, t := range d.Tags {
		if t == tag {
			return true
		}
	}
	return false
}

// Writable explains why the device may not be changed, or returns nil when it may. It is the one
// check every write goes through.
func (d *Device) Writable() error {
	if !writableKinds[d.Kind] {
		return fmt.Errorf("%s is a %s; this kind is never changed through this server", d.ID, d.Kind)
	}
	if d.Safety != SafetyControl {
		return fmt.Errorf("%s is read-only: the home map does not allow agents to change it; tell the user to use the Loxone app", d.ID)
	}
	if d.control.IsSecured {
		return fmt.Errorf("%s needs a visualisation password, which this server does not handle", d.ID)
	}
	if d.Kind == KindValue && d.Range == nil {
		return fmt.Errorf("%s has no range in the home map, so it is read-only", d.ID)
	}
	return nil
}

// Map is the home: rooms and devices, with their lookup tables.
type Map struct {
	Rooms        []*Room
	Devices      []*Device
	Warnings     []string
	LastModified string
	GlobalStates map[string]string // name → state UUID

	byID       map[string]*Device
	byUUID     map[string]*Device
	labels     map[string][]*Device // normalized label → devices
	roomLabels map[string]*Room
}

// Build makes the map from a structure file and an overlay.
func Build(f *structure.File, o *Overlay) (*Map, error) {
	if o == nil {
		o = &Overlay{}
	}
	m := &Map{
		LastModified: f.LastModified,
		GlobalStates: map[string]string{},
		byID:         map[string]*Device{},
		byUUID:       map[string]*Device{},
		labels:       map[string][]*Device{},
		roomLabels:   map[string]*Room{},
	}
	for name, ref := range f.GlobalStates {
		m.GlobalStates[name] = ref.First()
	}

	rooms := map[string]*Room{}
	roomByID := map[string]*Room{}
	roomUUIDs := make([]string, 0, len(f.Rooms))
	for uuid := range f.Rooms {
		roomUUIDs = append(roomUUIDs, uuid)
	}
	sort.Strings(roomUUIDs)
	for _, uuid := range roomUUIDs {
		g := f.Rooms[uuid]
		ro := o.Rooms[uuid]
		r := &Room{UUID: uuid, SourceName: cleanText(g.Name), Name: cleanText(g.Name), Hidden: ro.Hidden}
		if ro.Name != "" {
			r.Name = cleanText(ro.Name)
		}
		r.ID = ro.ID
		if r.ID == "" {
			r.ID = slug(g.Name)
		}
		if r.ID == "" {
			r.ID = "room-" + uuid[:8]
		}
		if roomByID[r.ID] != nil {
			r.ID += "-" + uuid[:8]
		}
		for _, a := range ro.Aliases {
			if a = cleanText(a); a != "" {
				r.Aliases = append(r.Aliases, a)
			}
		}
		rooms[uuid] = r
		roomByID[r.ID] = r
		m.Rooms = append(m.Rooms, r)
	}
	for uuid := range o.Rooms {
		if _, ok := f.Rooms[uuid]; !ok {
			m.Warnings = append(m.Warnings, fmt.Sprintf("overlay room %s is not in the structure file", uuid))
		}
	}
	sort.Slice(m.Rooms, func(i, j int) bool { return m.Rooms[i].SourceName < m.Rooms[j].SourceName })

	var errs []error
	for _, c := range f.SortedControls() {
		d, err := m.addDevice(c, o.Devices[c.UUID], rooms, roomByID)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		if d.Kind == KindLightGroup || d.Kind == KindBlindGroup {
			var det struct {
				Controls []struct {
					UUID string `json:"uuid"`
				} `json:"controls"`
			}
			if err := c.DecodeDetails(&det); err != nil {
				m.Warnings = append(m.Warnings, err.Error())
			}
			for _, mc := range det.Controls {
				d.Members = append(d.Members, mc.UUID)
			}
		}
	}
	for uuid := range o.Devices {
		if _, ok := f.Controls[uuid]; !ok {
			m.Warnings = append(m.Warnings, fmt.Sprintf("overlay device %s is not in the structure file", uuid))
		}
	}
	sort.Strings(m.Warnings)
	if err := errors.Join(errs...); err != nil {
		return nil, err
	}
	m.index()
	return m, nil
}

func (m *Map) addDevice(c *structure.Control, ov DeviceOverlay, rooms map[string]*Room, roomByID map[string]*Room) (*Device, error) {
	d := &Device{
		UUID:        c.UUID,
		SourceName:  cleanText(c.Name),
		Name:        cleanText(c.Name),
		Description: cleanText(ov.Description),
		Kind:        kindOf(c.Type),
		Type:        c.Type,
		Room:        rooms[c.Room],
		Safety:      ov.Safety,
		Range:       ov.Range,
		Unit:        cleanText(ov.Unit),
		Overview:    ov.Overview == nil || *ov.Overview,
		moodNames:   map[string]string{},
		control:     c,
	}
	if len(ov.Description) > maxNameLen {
		d.Description = cleanLong(ov.Description, 300)
	}
	if ov.Name != "" {
		d.Name = cleanText(ov.Name)
	}
	if d.Safety == "" {
		d.Safety = SafetyRead
	}
	if ov.Room != "" {
		d.Room = roomByID[ov.Room]
	}
	if d.Room == nil {
		d.Room = &Room{ID: "unassigned", Name: "Unassigned", SourceName: ""}
	}
	for _, a := range ov.Aliases {
		if a = cleanText(a); a != "" {
			d.Aliases = append(d.Aliases, a)
		}
	}
	for _, t := range ov.Tags {
		d.Tags = append(d.Tags, normalize(t))
	}
	for id, name := range ov.Moods {
		d.moodNames[strings.TrimSpace(id)] = cleanText(name)
	}
	d.ID = ov.ID
	if d.ID == "" {
		d.ID = d.Room.ID + "." + string(d.Kind) + "." + slug(c.Name)
	}
	if m.byID[d.ID] != nil {
		if ov.ID != "" {
			return nil, fmt.Errorf("device id %q is used twice", d.ID)
		}
		d.ID += "-" + c.UUID[:8]
	}
	switch d.Kind {
	case KindOption:
		var det struct {
			AllOff  string            `json:"allOff"`
			Outputs map[string]string `json:"outputs"`
		}
		if err := c.DecodeDetails(&det); err != nil {
			m.Warnings = append(m.Warnings, err.Error())
		}
		d.allOff = cleanText(det.AllOff)
		for k, v := range det.Outputs {
			id, err := strconv.Atoi(k)
			if err != nil {
				continue
			}
			opt := Option{ID: id, SourceName: cleanText(v), Name: cleanText(v)}
			if n := ov.Options[k]; n != "" {
				opt.Name = cleanText(n)
			}
			d.options = append(d.options, opt)
		}
		if n := ov.Options["0"]; n != "" && d.allOff != "" {
			d.allOff = cleanText(n)
		}
		sort.Slice(d.options, func(i, j int) bool { return d.options[i].ID < d.options[j].ID })
	case KindValue:
		var det struct {
			Min    float64 `json:"min"`
			Max    float64 `json:"max"`
			Step   float64 `json:"step"`
			Format string  `json:"format"`
		}
		if err := c.DecodeDetails(&det); err != nil {
			m.Warnings = append(m.Warnings, err.Error())
		}
		d.slider = sliderDetails{Min: det.Min, Max: det.Max, Step: det.Step, Format: det.Format}
	case KindBlind:
		var det struct {
			Animation int `json:"animation"`
		}
		if err := c.DecodeDetails(&det); err != nil {
			m.Warnings = append(m.Warnings, err.Error())
		}
		d.animation = det.Animation
	}
	m.Devices = append(m.Devices, d)
	m.byID[d.ID] = d
	m.byUUID[d.UUID] = d
	return d, nil
}

func cleanLong(s string, n int) string {
	r := []rune(strings.Join(strings.Fields(s), " "))
	if len(r) > n {
		r = r[:n]
	}
	var b strings.Builder
	for _, c := range r {
		if c >= 0x20 && c != 0x7f {
			b.WriteRune(c)
		}
	}
	return b.String()
}

// index fills the label tables used by name resolution.
func (m *Map) index() {
	for _, r := range m.Rooms {
		for _, l := range r.labels() {
			if l = normalize(l); l != "" {
				if _, taken := m.roomLabels[l]; !taken {
					m.roomLabels[l] = r
				}
			}
		}
	}
	for _, d := range m.Devices {
		if d.Safety == SafetyHidden {
			continue
		}
		seen := map[string]bool{}
		names := append([]string{d.ID, d.Name, d.SourceName}, d.Aliases...)
		var labels []string
		labels = append(labels, names...)
		for _, rl := range d.Room.labels() {
			for _, n := range names[1:] {
				labels = append(labels, rl+" "+n, n+" "+rl)
			}
		}
		for _, l := range labels {
			l = normalize(l)
			if l == "" || seen[l] {
				continue
			}
			seen[l] = true
			m.labels[l] = append(m.labels[l], d)
		}
	}
	for l, ds := range m.labels {
		r := m.roomLabels[l]
		if r == nil {
			continue
		}
		for _, d := range ds {
			if d.Room != r {
				m.Warnings = append(m.Warnings, fmt.Sprintf("%q names both the room %s and the device %s in another room", l, r.ID, d.ID))
				break
			}
		}
	}
	sort.Strings(m.Warnings)
}

func (r *Room) labels() []string {
	if r.Hidden {
		return nil
	}
	return append([]string{r.ID, r.Name, r.SourceName}, r.Aliases...)
}

// Device returns a device by its UUID.
func (m *Map) Device(uuid string) *Device { return m.byUUID[uuid] }

// Visible lists the devices agents may see, in map order.
func (m *Map) Visible() []*Device {
	out := make([]*Device, 0, len(m.Devices))
	for _, d := range m.Devices {
		if d.Safety != SafetyHidden && !d.Room.Hidden {
			out = append(out, d)
		}
	}
	return out
}

// Room finds a room by id, name or alias.
func (m *Map) Room(query string) *Room {
	return m.roomLabels[normalize(query)]
}

// NotFoundError is a failed lookup with the closest matches.
type NotFoundError struct {
	What    string
	Query   string
	Matches []string
}

func (e *NotFoundError) Error() string {
	if len(e.Matches) == 0 {
		return fmt.Sprintf("no %s matches %q; list_devices shows what exists", e.What, e.Query)
	}
	return fmt.Sprintf("no %s matches %q; did you mean: %s", e.What, e.Query, strings.Join(e.Matches, ", "))
}

// Resolve finds one visible device by id, name or alias, optionally qualified by its room
// ("bedroom left blind"). An ambiguous name is an error that lists the candidates.
func (m *Map) Resolve(query string) (*Device, error) {
	q := normalize(query)
	if q == "" {
		return nil, errors.New("no device given")
	}
	if d := m.byID[strings.TrimSpace(strings.ToLower(query))]; d != nil && d.Safety != SafetyHidden && !d.Room.Hidden {
		return d, nil
	}
	ds := m.visibleOnly(m.labels[q])
	switch len(ds) {
	case 1:
		return ds[0], nil
	case 0:
		return nil, &NotFoundError{What: "device", Query: query, Matches: m.suggestDevices(query)}
	default:
		ids := make([]string, len(ds))
		for i, d := range ds {
			ids[i] = d.ID
		}
		return nil, fmt.Errorf("%q matches several devices: %s; use one of these ids", query, strings.Join(ids, ", "))
	}
}

func (m *Map) visibleOnly(ds []*Device) []*Device {
	out := ds[:0:0]
	for _, d := range ds {
		if d.Safety != SafetyHidden && !d.Room.Hidden {
			out = append(out, d)
		}
	}
	return out
}

func (m *Map) suggestDevices(query string) []string {
	cands := map[string]string{}
	for l, ds := range m.labels {
		for _, d := range m.visibleOnly(ds) {
			cands[l] = d.ID
		}
	}
	return suggest(query, cands, 5)
}

// Scope is the result of resolving a write target: the devices to command.
type Scope struct {
	Description string    // what the target was understood as
	Devices     []*Device // writable devices of the requested kind
	Skipped     []string  // devices of the kind in the scope that may not be changed, with why
}

// ResolveScope resolves a write target for one kind: "all" (every such device in the home), a
// room, a group controller of that kind, or one device. Only writable devices are returned;
// the target must name at least one.
func (m *Map) ResolveScope(target string, kind Kind) (*Scope, error) {
	q := normalize(target)
	if q == "" {
		return nil, errors.New("no target given")
	}
	var cands []*Device
	var desc string
	group := map[Kind]Kind{KindLight: KindLightGroup, KindBlind: KindBlindGroup}[kind]
	switch {
	case q == "all":
		desc = "all " + string(kind) + " devices"
		for _, d := range m.Visible() {
			if d.Kind == kind {
				cands = append(cands, d)
			}
		}
	case m.byID[strings.TrimSpace(strings.ToLower(target))] == nil && m.Room(target) != nil:
		r := m.Room(target)
		// a device that shares the room's name and lives in it is covered by the room
		for _, d := range m.visibleOnly(m.labels[q]) {
			if d.Room != r {
				return nil, fmt.Errorf("%q names both the room %s and the device %s; use the room id or the device id", target, r.ID, d.ID)
			}
		}
		desc = string(kind) + " devices in " + r.Name
		for _, d := range m.Visible() {
			if d.Kind == kind && d.Room == r {
				cands = append(cands, d)
			}
		}
	default:
		d, err := m.Resolve(target)
		if err != nil {
			var nf *NotFoundError
			if errors.As(err, &nf) {
				rooms := map[string]string{}
				for l, r := range m.roomLabels {
					rooms[l] = r.ID
				}
				nf.Matches = append(nf.Matches, suggest(target, rooms, 3)...)
			}
			return nil, err
		}
		switch {
		case d.Kind == kind:
			desc = d.ID
			cands = []*Device{d}
		case group != "" && d.Kind == group:
			desc = "members of " + d.ID
			for _, uuid := range d.Members {
				if md := m.byUUID[uuid]; md != nil && md.Kind == kind && md.Safety != SafetyHidden && !md.Room.Hidden {
					cands = append(cands, md)
				}
			}
		default:
			return nil, fmt.Errorf("%s is a %s, not a %s", d.ID, d.Kind, kind)
		}
	}
	s := &Scope{Description: desc}
	for _, d := range cands {
		if err := d.Writable(); err != nil {
			s.Skipped = append(s.Skipped, err.Error())
			continue
		}
		s.Devices = append(s.Devices, d)
	}
	if len(s.Devices) == 0 {
		if len(s.Skipped) > 0 {
			return nil, fmt.Errorf("nothing to change in %s: %s", desc, strings.Join(s.Skipped, "; "))
		}
		return nil, fmt.Errorf("there are no %s devices in %s", kind, desc)
	}
	return s, nil
}
