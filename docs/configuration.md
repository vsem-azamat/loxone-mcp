# Configuration

## Environment

| Variable | Default | Meaning |
| --- | --- | --- |
| `LOXONE_URL` | required | Cloud DNS address `https://connect.loxonecloud.com/<serial>` (resolved on every connection), or the Miniserver's own `https://host:port` |
| `LOXONE_USER` | required | Miniserver user. Use a user without admin rights: with an admin user every write is refused unless `LOXONE_ALLOW_ADMIN=1` |
| `LOXONE_PASSWORD` | | The user's password. It is used only to acquire a token and removed from the process environment at start |
| `LOXONE_PASSWORD_FILE` | | A file holding the password (a trailing newline is ignored), instead of `LOXONE_PASSWORD` |
| `LOXONE_OVERLAY` | `$XDG_CONFIG_HOME/loxone-mcp/home.yaml` | The overlay file. A missing file means an empty overlay: everything read-only |
| `LOXONE_STATE_DIR` | `$XDG_STATE_HOME/loxone-mcp` | The token cache `token.json` and the `serve.lock`; created `0700` |
| `LOXONE_ALLOW_ADMIN` | | `1` allows writes with an administrator account |
| `LOXONE_LOG_LEVEL` | `info` | `debug`, `info`, `warn` or `error`; logs are JSON lines on stderr |

`$XDG_CONFIG_HOME` and `$XDG_STATE_HOME` default to `~/.config` and `~/.local/state`.

## Commands

| Command | What it does |
| --- | --- |
| `loxone-mcp` / `loxone-mcp serve` | The MCP server on stdin/stdout. Refuses to start with an invalid overlay or when another `serve` holds the state directory |
| `loxone-mcp check` | Connects, builds the map with the overlay, prints every device with its kind, id, `CONTROL`/`read` and current state, and the map warnings. Changes nothing |
| `loxone-mcp overlay-init` | Prints an overlay skeleton for the live structure file: generated ids, Miniserver names as aliases, every device `safety: read` |
| `loxone-mcp logout` | Revokes the cached token on the Miniserver (`jdev/sys/killtoken`) and forgets it |
| `loxone-mcp version` | Prints the version |

## Overlay

A YAML file with two maps, keyed by the UUIDs of the structure file (`rooms` keys, and each control's `uuidAction`). Unknown keys are errors, so a typo cannot silently fall back to a default. Entries for UUIDs that are not in the structure file are logged as warnings and ignored. See [examples/home.yaml](../examples/home.yaml).

### `rooms.<uuid>`

| Key | Meaning |
| --- | --- |
| `id` | Stable id agents use (`[a-z0-9][a-z0-9._-]*`). Default: the folded Miniserver name |
| `name` | Display name. Default: the Miniserver name |
| `aliases` | More names that select the room |
| `hidden` | `true` hides the room and every device in it |

### `devices.<uuid>`

| Key | Meaning |
| --- | --- |
| `id` | Stable id (`[a-z0-9][a-z0-9._-]*`, unique). Default: `<room id>.<kind>.<folded name>` |
| `name`, `description` | What agents see. The Miniserver name always matches too |
| `aliases` | More names, in any language |
| `safety` | `control` (the write tools may change it), `read` (default) or `hidden` (not shown at all) |
| `room` | A room id of the overlay, to move the device to another room |
| `tags` | Free tags. `meter` lists a device in `get_meters` and keeps it out of `home_overview`; `ventilation`, `alarm` and others are shown in `get_device` |
| `overview` | `false` keeps the device out of `home_overview` |
| `range` | `{min, max}`: for a climate device the temperatures agents may set (default 15..26 °C); for a value device the allowed values, within the slider's own bounds. A value device without `range` is read-only |
| `options` | Names of a Radio's outputs by id; `"0"` names the all-off option |
| `moods` | Names of a light's moods by id (`778` is the static Off mood) |
| `unit` | Unit shown with a value device |

After the Loxone project changes (new controls, renamed rooms), the server picks up the new structure file by itself; run `overlay-init` again and merge new entries into the overlay by hand. The overlay is read at start: restart the server after editing it.
