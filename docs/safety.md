# Safety

The server moves real things in an inhabited home, so what agents may change is decided in code from the private overlay, not by tool descriptions.

## What may be changed

A device may be changed only when all of these hold (`home.Device.Writable`):

1. its kind is `light`, `blind`, `climate`, `switch`, `option` or `value`. Smoke and water alarms, the intercom, presence detectors, texts and meters, central (group) controllers and unknown control types are never changed, whatever the overlay says;
2. the overlay marks it `safety: control`. Without an overlay entry a device is read-only;
3. it needs no visualisation password (`isSecured`);
4. a `value` device has a `range` in the overlay.

The Miniserver state can refuse more at the moment of the call: a control locked by the Miniserver (`jLocked` with `locked` other than 0), a blind that is locked or in a safety shutdown (only `stop` is sent then), a switch held on by logic. Writes are refused altogether while the Miniserver is disconnected, and when the Miniserver user is an administrator (`LOXONE_ALLOW_ADMIN=1` overrides): the token carries all of the user's rights, so the server expects a user limited to what it may control.

## Targets and scopes

`set_lights` and `set_blinds` accept a room, a group controller or the literal `all`; they expand to the writable devices of that kind and report the others as `skipped`. Group controllers are never commanded: each member gets its own validated command. A name of a room that is also the name of a device in that room means the room (for example a room's main light named like the room); a room name that is also a device name elsewhere is an error. Climate targets resolve to a room's controller.

## The write path

`server.execute` is the only code that sends control commands:

1. resolve the target; build every device's command from validated parts (numbers checked against ranges and formatted by code, mood and option ids taken from the Miniserver's own lists). If any command cannot be built, nothing is sent;
2. skip devices known to be in the requested state already (`no_change`); a device whose state is unknown always gets the command;
3. send the commands (each bounded to 15 s; an unanswered command closes the connection), then wait up to 3 s for each device to publish a new state (`confirmed`);
4. write one audit record per device and answer with the state before and after.

No text from the caller reaches a command path: the UUID comes from the map, the command from a fixed set or a number formatted by code.

## Audit record

One JSON line on stderr per device and write, `msg` `write`: `tool`, `client` (the MCP client name from `initialize`; behind a gateway that is the gateway, whose own audit names the token), `target`, `device`, `command`, `outcome` (`ok`, `unconfirmed`, `no_change`, `error`), `error`, `before`, `after`. Refused writes are logged as `write refused` with the reasons. The password, the token and hashes are never logged.

## Names from the Miniserver

Room, device, mood and option names come from the Miniserver and the overlay. They are cleaned before use (control and format characters removed, length capped) and only ever appear as JSON string values in answers; tool descriptions are static.
