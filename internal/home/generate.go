package home

import (
	"encoding/json"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/vsem-azamat/loxone-mcp/internal/structure"
)

func quote(s string) string {
	b, _ := json.Marshal(s) // a JSON string is a valid YAML double-quoted scalar
	return string(b)
}

// GenerateOverlay writes an overlay skeleton for every room and control of the structure file:
// generated ids, the Miniserver names as aliases and every device read-only. Edit it by hand:
// English names, descriptions, aliases, and safety: control only for what agents may change.
func GenerateOverlay(f *structure.File) string {
	m, err := Build(f, nil)
	if err != nil {
		return "# could not map the structure file: " + err.Error() + "\n"
	}
	var b strings.Builder
	b.WriteString("# loxone-mcp home overlay. Generated from the structure file of " + quote(f.LastModified) + ".\n")
	b.WriteString("# Keys are the Miniserver UUIDs. safety: control | read | hidden (default read).\n")
	b.WriteString("# Only devices with safety: control can be changed, and only lights, blinds, climate,\n")
	b.WriteString("# switches, options and values; alarms, intercom, presence and texts never.\n\n")
	b.WriteString("rooms:\n")
	for _, r := range m.Rooms {
		fmt.Fprintf(&b, "  %s:\n    id: %s\n    name: %s\n    aliases: [%s]\n", quote(r.UUID), quote(r.ID), quote(r.SourceName), quote(r.SourceName))
	}
	b.WriteString("\ndevices:\n")
	for _, d := range m.Devices {
		fmt.Fprintf(&b, "  # %s %s in %s\n", d.Type, quote(d.SourceName), quote(d.Room.SourceName))
		fmt.Fprintf(&b, "  %s:\n    id: %s\n    name: %s\n    aliases: [%s]\n    safety: read\n", quote(d.UUID), quote(d.ID), quote(d.SourceName), quote(d.SourceName))
		switch d.Kind {
		case KindOption:
			b.WriteString("    options:\n")
			for _, o := range d.options {
				fmt.Fprintf(&b, "      %s: %s\n", quote(strconv.Itoa(o.ID)), quote(o.SourceName))
			}
			if d.allOff != "" {
				fmt.Fprintf(&b, "      \"0\": %s\n", quote(d.allOff))
			}
		case KindText:
			b.WriteString("    tags: []\n")
		}
		subs := d.control.SortedSubControls()
		if len(subs) > 0 {
			names := make([]string, 0, len(subs))
			for _, s := range subs {
				names = append(names, s.Type+" "+quote(cleanText(s.Name)))
			}
			sort.Strings(names)
			fmt.Fprintf(&b, "    # sub-controls: %s\n", strings.Join(names, ", "))
		}
	}
	return b.String()
}
