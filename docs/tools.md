# Tools

All answers are JSON (structured content plus the same JSON as text). Device and room arguments accept an id, a name or an alias, optionally with the room ("bedroom left blind"); names are matched without case and diacritics. An unknown name is an error with up to five close matches; an ambiguous one is an error listing the candidates. Every answer has a `connection` object (`connected`, `since`, and `last_error` while disconnected).

## Read

| Tool | Arguments | Answer |
| --- | --- | --- |
| `home_overview` | none | `rooms[]`: `climate`, `lights`, `blinds`, `other` items with id, name and summary; `alerts[]`: smoke alarm, doorbell ringing, blocked blinds, open windows; `warning` while disconnected |
| `list_devices` | `query?` (free text over names, aliases, description, tags, kind), `room?`, `kind?`, `writable?` | `devices[]`: id, name, kind, room, writable, state; `rooms[]` |
| `get_device` | `device` | id, name, kind, room, `state`, `writable` (and `why_read_only`), `actions` (the write-tool calls it accepts), `moods`/`options`/`setpoint_range`/`range`/`recent_bells`, `raw_states`, description, aliases, tags |
| `get_meters` | none | `meters[]` of devices tagged `meter`, with their text |
| `get_intercom_bells` | `limit?` (default 10, at most 50) | per intercom: `ringing`, `recent_bells[]` (ISO 8601, newest first, unanswered rings only); `now` |

Summaries by kind: light `on`, `moods`; blind `closed_percent` (0 open, 100 closed), `slats_percent`, `moving`, `target_closed_percent`, `automatic`, `blocked`; climate `temperature_c`, `target_temperature_c`, `humidity_percent`, `co2_ppm` (only when the sensor reports), `mode` (`eco`, `comfort`, `building_protection`, `manual`, `off`), `window_open`, `override` (`reason`, `since`, `until`); `get_device` adds `climate` (`comfort_heating_c`, `comfort_cooling_c`, `comfort_offset_c`, `building_protection_min_c`/`_max_c`, `outdoor_temperature_c`, `operating_mode`); switch `on`, `locked_on`; option `selected`; value `value`, `unit`; text `text`; smoke alarm `alarm`, `level`; presence `present`, `since`; intercom `ringing`, `last_bell`.

## Write

| Tool | Arguments | Commands |
| --- | --- | --- |
| `set_lights` | `target` (light, room, light group or `all`), `action` `off`/`on`/`mood`, `mood` | `changeTo/<mood id>`; `on` is the first favourite mood that is not Off; `off` is mood 778 |
| `set_blinds` | `target` (blind, room, blind group or `all`), `action` `open`/`close`/`stop`/`shade`/`position`/`slats`/`automatic_on`/`automatic_off`, `position` (% closed), `slats` (%) | `FullUp`, `FullDown`, `stop`, `shade`, `manualPosition/<p>`, `manualLamelle/<s>`, `manualPosBlind/<p>/<s>`, `auto`, `NoAuto` |
| `set_climate` | `target` (room or climate device), `action` `temperature`/`override`/`stop_override`/`comfort_setpoint`, `temperature`, `mode` `comfort`/`eco`, `minutes` (1..1440, default 240) | `temperature`: `override/3/<until>/<t>` (manual temperature until then); `override`: `override/1` or `override/0/<until>`; `stop_override`; `comfort_setpoint`: `setComfortTemperature/<t>` (permanent) |
| `set_device` | `device`, `action` `on`/`off`/`select`/`set`, `option`, `value` | switch `on`/`off`; option `<output id>` or `reset`; value `<number>` rounded to the slider's step |

A write answers `target` (how the target was understood), `results[]` per device (`device`, `ok`, `confirmed` (absent when nothing was sent), `no_change`, `error`, `before`, `after`; the Loxone command is only in the audit record), `skipped[]` (devices of the scope that may not be changed, with the reason) and `warning`/`note` when a command failed or was not confirmed. `confirmed` means the Miniserver published a state of the device after the command; blinds keep moving for up to a minute after the answer.
