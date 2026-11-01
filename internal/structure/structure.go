// Package structure parses the Miniserver's structure file (LoxAPP3.json): rooms, categories,
// controls, their states and details. It only reads; it knows nothing about the connection.
package structure

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
)

// File is the part of the structure file this server uses.
type File struct {
	LastModified   string              `json:"lastModified"`
	MsInfo         MsInfo              `json:"msInfo"`
	Rooms          map[string]Group    `json:"rooms"`
	Cats           map[string]Group    `json:"cats"`
	Controls       map[string]*Control `json:"controls"`
	OperatingModes map[string]string   `json:"operatingModes"`
	GlobalStates   map[string]StateRef `json:"globalStates"`
}

// MsInfo describes the Miniserver and the signed-in user.
type MsInfo struct {
	SerialNr     string      `json:"serialNr"`
	MsName       string      `json:"msName"`
	ProjectName  string      `json:"projectName"`
	LanguageCode string      `json:"languageCode"`
	Location     string      `json:"location"`
	CurrentUser  CurrentUser `json:"currentUser"`
}

// CurrentUser is the user the structure file was delivered to.
type CurrentUser struct {
	Name    string `json:"name"`
	IsAdmin bool   `json:"isAdmin"`
}

// Group is a room or a category.
type Group struct {
	UUID string `json:"uuid"`
	Name string `json:"name"`
}

// Control is one block of the user interface, or a sub-control of one.
type Control struct {
	UUID        string              `json:"uuidAction"`
	Name        string              `json:"name"`
	Type        string              `json:"type"`
	Room        string              `json:"room"`
	Cat         string              `json:"cat"`
	IsSecured   bool                `json:"isSecured"`
	States      map[string]StateRef `json:"states"`
	Details     json.RawMessage     `json:"details"`
	SubControls map[string]*Control `json:"subControls"`
}

// StateRef is the UUID (or UUIDs) a state's value is published under. Most states have one
// UUID; a few have a list.
type StateRef []string

// UnmarshalJSON accepts a string or a list of strings.
func (s *StateRef) UnmarshalJSON(b []byte) error {
	var one string
	if err := json.Unmarshal(b, &one); err == nil {
		*s = StateRef{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return fmt.Errorf("state reference is neither a string nor a list of strings: %w", err)
	}
	*s = many
	return nil
}

// First is the state's UUID, or "" when it has none.
func (s StateRef) First() string {
	if len(s) == 0 {
		return ""
	}
	return s[0]
}

// Parse decodes a structure file.
func Parse(b []byte) (*File, error) {
	var f File
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, fmt.Errorf("structure file: %w", err)
	}
	if f.LastModified == "" {
		return nil, errors.New("structure file: no lastModified")
	}
	if f.Controls == nil {
		f.Controls = map[string]*Control{}
	}
	for key, c := range f.Controls {
		if c == nil {
			delete(f.Controls, key)
			continue
		}
		if c.UUID == "" {
			c.UUID = key
		}
		for skey, sc := range c.SubControls {
			if sc == nil {
				delete(c.SubControls, skey)
				continue
			}
			if sc.UUID == "" {
				sc.UUID = skey
			}
		}
	}
	return &f, nil
}

// RoomName is the name of the room with this UUID, or "".
func (f *File) RoomName(uuid string) string { return f.Rooms[uuid].Name }

// CatName is the name of the category with this UUID, or "".
func (f *File) CatName(uuid string) string { return f.Cats[uuid].Name }

// SortedControls lists the top-level controls ordered by room name, then control name, then
// UUID, so output and generated files are stable.
func (f *File) SortedControls() []*Control {
	out := make([]*Control, 0, len(f.Controls))
	for _, c := range f.Controls {
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := f.RoomName(out[i].Room), f.RoomName(out[j].Room)
		if ri != rj {
			return ri < rj
		}
		if out[i].Name != out[j].Name {
			return out[i].Name < out[j].Name
		}
		return out[i].UUID < out[j].UUID
	})
	return out
}

// SortedSubControls lists a control's sub-controls ordered by UUID.
func (c *Control) SortedSubControls() []*Control {
	out := make([]*Control, 0, len(c.SubControls))
	for _, s := range c.SubControls {
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UUID < out[j].UUID })
	return out
}

// State is the UUID of the named state, or "".
func (c *Control) State(name string) string { return c.States[name].First() }

// DecodeDetails decodes the control's details into v. Missing details leave v unchanged.
func (c *Control) DecodeDetails(v any) error {
	if len(c.Details) == 0 || string(c.Details) == "null" {
		return nil
	}
	if err := json.Unmarshal(c.Details, v); err != nil {
		return fmt.Errorf("details of %s: %w", c.Type, err)
	}
	return nil
}
