# loxone-mcp

An MCP server that lets AI agents understand and control a home run by a Loxone Miniserver: what is on, how warm each room is, where the blinds are, and changing lights, blinds, room temperature, ventilation and a few switches. Agents use English (or local) names; they never see Loxone UUIDs.

It is one static Go binary speaking MCP over stdio. It connects to the Miniserver's WebSocket (through the Loxone cloud DNS and Remote Connect, or directly), authenticates with a long-lived token, keeps every state current from the Miniserver's event stream, and builds a map of the home from the structure file plus a private overlay that names rooms and devices and says which ones agents may change.

## Tools

| Tool | What it does |
| --- | --- |
| `home_overview` | Every room's climate, lights, blinds and other devices, alerts, connection status |
| `list_devices` | Devices with id, kind, room, writable flag and a short state; filter by room and kind |
| `get_device` | One device in full: state, raw states, moods/options, ranges, allowed actions |
| `get_meters` | Utility meter readings |
| `get_intercom_bells` | Doorbell ringing now and recent unanswered rings |
| `set_lights` | Off, on, or a mood; one light, a room, or `all` |
| `set_blinds` | Open, close, stop, shade, position %, slats %, automation; one blind, a room, or `all` |
| `set_climate` | Hold a temperature for a while, comfort/eco override, stop override, permanent comfort setpoint |
| `set_device` | Switch on/off, select an option, set a value, for devices the overlay allows |

Details: [docs/tools.md](docs/tools.md).

## Safety

Nothing is writable unless the overlay marks it `safety: control`, and alarms, the intercom, presence detectors, texts and group controllers are never writable whatever the overlay says. Every write is validated against the map, logged as an audit record with the state before and after, and answered with the resulting state. See [docs/safety.md](docs/safety.md).

## Quick start

```bash
go install github.com/vsem-azamat/loxone-mcp/cmd/loxone-mcp@latest   # or a release archive

export LOXONE_URL=https://connect.loxonecloud.com/<serial> LOXONE_USER=<user>
export LOXONE_PASSWORD_FILE=~/.config/loxone-mcp/password            # or LOXONE_PASSWORD

loxone-mcp overlay-init > ~/.config/loxone-mcp/home.yaml   # a skeleton: everything read-only
$EDITOR ~/.config/loxone-mcp/home.yaml                     # names, aliases, safety: control
loxone-mcp check                                           # connect and show the map, change nothing
loxone-mcp serve                                           # MCP on stdin/stdout
```

Configuration and the overlay format: [docs/configuration.md](docs/configuration.md). Running it behind an MCP gateway, the token, and refreshing the map: [docs/operations.md](docs/operations.md).

## Development

`make check` runs what CI runs (repository policy, tidy, golangci-lint, tests with the race detector, govulncheck); `make release-check` builds a GoReleaser snapshot. Tests run against an anonymised structure file and a fake Miniserver, never a real one. Contributor rules: [AGENTS.md](AGENTS.md).

## License

[Apache License 2.0](LICENSE).
