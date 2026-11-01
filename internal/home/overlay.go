package home

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Safety classes of a device in the overlay.
const (
	SafetyControl = "control" // agents may change it through the write tools
	SafetyRead    = "read"    // agents see it but cannot change it (the default)
	SafetyHidden  = "hidden"  // agents do not see it
)

// Overlay is the private file that names the home's rooms and devices for agents and says which
// devices they may change. Rooms and devices are keyed by their structure-file UUID.
type Overlay struct {
	Rooms   map[string]RoomOverlay   `yaml:"rooms"`
	Devices map[string]DeviceOverlay `yaml:"devices"`
}

// RoomOverlay names one room.
type RoomOverlay struct {
	ID      string   `yaml:"id"`
	Name    string   `yaml:"name"`
	Aliases []string `yaml:"aliases"`
	Hidden  bool     `yaml:"hidden"`
}

// DeviceOverlay names one control and sets its safety class.
type DeviceOverlay struct {
	ID          string            `yaml:"id"`
	Name        string            `yaml:"name"`
	Description string            `yaml:"description"`
	Aliases     []string          `yaml:"aliases"`
	Safety      string            `yaml:"safety"`
	Room        string            `yaml:"room"` // room id overriding the structure file's room
	Tags        []string          `yaml:"tags"`
	Overview    *bool             `yaml:"overview"`
	Range       *Range            `yaml:"range"`
	Options     map[string]string `yaml:"options"` // Radio output id → name
	// DenyOptions are Radio output ids agents may not select; "0" is the all-off option.
	DenyOptions []string          `yaml:"deny_options"`
	Moods       map[string]string `yaml:"moods"` // light mood id → name
	Unit        string            `yaml:"unit"`
}

// Range bounds the values a write tool accepts for a device.
type Range struct {
	Min float64 `yaml:"min"`
	Max float64 `yaml:"max"`
}

var idPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)

// LoadOverlay reads the overlay file. A missing file is an empty overlay.
func LoadOverlay(path string) (*Overlay, error) {
	b, err := os.ReadFile(path) //nolint:gosec // the overlay path is configuration
	if errors.Is(err, os.ErrNotExist) {
		return &Overlay{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("overlay: %w", err)
	}
	return ParseOverlay(b)
}

// ParseOverlay decodes and checks an overlay. Unknown keys are errors, so a typo in a safety
// field cannot silently fall back to a default.
func ParseOverlay(b []byte) (*Overlay, error) {
	var o Overlay
	dec := yaml.NewDecoder(bytes.NewReader(b))
	dec.KnownFields(true)
	if err := dec.Decode(&o); err != nil && !errors.Is(err, io.EOF) {
		return nil, fmt.Errorf("overlay: %w", err)
	}
	if err := o.validate(); err != nil {
		return nil, fmt.Errorf("overlay: %w", err)
	}
	return &o, nil
}

func (o *Overlay) validate() error {
	var errs []error
	roomIDs := map[string]bool{}
	for uuid, r := range o.Rooms {
		if r.ID == "" {
			continue
		}
		if !idPattern.MatchString(r.ID) {
			errs = append(errs, fmt.Errorf("room %s: id %q must be lower case letters, digits, '.', '_' or '-'", uuid, r.ID))
		}
		if roomIDs[r.ID] {
			errs = append(errs, fmt.Errorf("room id %q is used twice", r.ID))
		}
		roomIDs[r.ID] = true
	}
	devIDs := map[string]bool{}
	for uuid, d := range o.Devices {
		switch d.Safety {
		case "", SafetyControl, SafetyRead, SafetyHidden:
		default:
			errs = append(errs, fmt.Errorf("device %s: safety %q is not control, read or hidden", uuid, d.Safety))
		}
		if d.ID != "" {
			if !idPattern.MatchString(d.ID) {
				errs = append(errs, fmt.Errorf("device %s: id %q must be lower case letters, digits, '.', '_' or '-'", uuid, d.ID))
			}
			if devIDs[d.ID] {
				errs = append(errs, fmt.Errorf("device id %q is used twice", d.ID))
			}
			devIDs[d.ID] = true
		}
		if d.Range != nil && d.Range.Min > d.Range.Max {
			errs = append(errs, fmt.Errorf("device %s: range min is above max", uuid))
		}
		if d.Room != "" && !roomIDs[d.Room] {
			errs = append(errs, fmt.Errorf("device %s: room %q is not a room id of the overlay", uuid, d.Room))
		}
		for _, id := range d.DenyOptions {
			if _, err := strconv.Atoi(strings.TrimSpace(id)); err != nil {
				errs = append(errs, fmt.Errorf("device %s: deny_options entry %q is not an option id", uuid, id))
			}
		}
		for _, t := range d.Tags {
			if strings.TrimSpace(t) == "" {
				errs = append(errs, fmt.Errorf("device %s: empty tag", uuid))
			}
		}
	}
	return errors.Join(errs...)
}
