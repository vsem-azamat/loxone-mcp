# Architecture

```
agent ── MCP (stdio) ── internal/server ── internal/home (map, safety, views, commands)
                              │                    │
                              │              internal/state (latest value per state UUID)
                              │                    ▲
                              └── internal/loxone ─┘── WSS ── Remote Connect relay ── Miniserver
```

## Connection (`internal/loxone`)

- **Address.** `LOXONE_URL` is either a cloud DNS address (`https://connect.loxonecloud.com/<serial>` or the older `dns.loxonecloud.com`) or the Miniserver's own `https` URL. A cloud address is resolved on every connection attempt: the client requests it without following redirects and takes the `Location` of the answer, which must be an `https` host under `loxonecloud.com` (a Remote Connect relay such as `https://<ip-with-dashes>.<serial>.dyndns.loxonecloud.com:<port>/`). The relay host and port change, so they are never stored.
- **WebSocket.** `wss://<host>/ws/rfc6455` with the subprotocol `remotecontrol`. Over TLS the Miniserver (firmware 11.2 and later) needs no application-layer encryption, so commands are sent in plain text inside TLS.
- **Authentication.** A JSON Web Token with the app permission (4), which lives for weeks. With a cached token: `jdev/sys/getkey`, then `authwithtoken/<HMAC(key, token)>/<user>`. Without one, or when it is rejected: `jdev/sys/getkey2/<user>` gives a key, salt and hash algorithm; the hash `HMAC(key, user ":" UPPER(HEX(H(password ":" salt))))` goes to `jdev/sys/getjwt/<hash>/<user>/4/<client-uuid>/loxone-mcp`. The token, its expiry, the hash algorithm and a client UUID are cached in `<state dir>/token.json` (0600). When fewer than three days are left (checked at every connection and every six hours) the token is refreshed with `jdev/sys/refreshjwt`.
- **Structure file.** After authentication `jdev/sps/LoxAPPversion3` gives the structure file's `lastModified`; `data/LoxAPP3.json` is loaded only when it differs from the loaded one, and the home map is rebuilt from it.
- **States.** `jdev/sps/enablebinstatusupdate` makes the Miniserver send its full value and text event tables, then every change. Every message is preceded by an 8-byte header; value events are a 16-byte UUID and a float64, text events a UUID, an icon UUID, a length and padded text. The client is ready once the first value table (and the first text table, or 5 s) arrived.
- **Commands.** One command is in flight at a time; its answer is the next text message whose `LL.control` names the same command (`dev/...` for `jdev/...`). A command that times out closes the connection, so a late answer is never taken for another command. A non-200 code is an error. Control commands are `jdev/sps/io/<uuid>/<command>`.
- **Keepalive and reconnects.** `keepalive` every 60 s (the Miniserver drops clients silent for 5 minutes). Any failure, including the out-of-service header before an update or restart, ends the connection; the client reconnects with backoff from 1 s to 60 s, resolving the relay again. Rejected credentials (401, 403, 423, 429) and the close codes 4003 (blocked after failed logins) and 420 (no authentication in time) wait 5 minutes, so the client never feeds the Miniserver's brute-force block.

## Home map (`internal/home`)

`home.Build` turns the structure file and the overlay into rooms and devices. Every control becomes a device with a kind from its type (`LightControllerV2` → `light`, `Jalousie` → `blind`, `IRoomControllerV2` → `climate`, `Switch` → `switch`, `Radio` → `option`, `Slider` → `value`, `TextState` → `text`, central controllers → `light_group`/`blind_group`, and `smoke_alarm`, `presence`, `intercom`, `other`). The overlay gives ids, names, descriptions, aliases, tags, ranges and the safety class; controls it does not mention get a generated id (`<room>.<kind>.<name>`) and are read-only.

Names are matched after folding: lower case, no diacritics, punctuation as spaces. A device matches its id, its name, its Miniserver name, its aliases, and each of those combined with any name of its room ("bedroom left blind"). A name that matches several devices is an error that lists their ids; nothing is picked by similarity, suggestions only appear in the error.

`home.States` reads the store for a map: short summaries per kind (lights on and their moods, blind closed %, climate temperatures and override, and so on), the full detail of one device, and the command builders of the write tools. Miniserver timestamps (seconds since 2009 on the Miniserver's wall clock) are converted with the time zone offset of the Miniserver's `miniserverTime` state.

## MCP server (`internal/server`)

Nine tools on the official Go SDK; read tools carry `readOnlyHint`. Each call waits up to 10 s for the first connection. Answers are structured JSON; while the connection is down, reads answer with the last known values and say so, writes are refused. The write path is described in [Safety](safety.md).
