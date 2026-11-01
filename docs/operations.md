# Operations

## Running under an MCP gateway

The server is a stdio process; a gateway such as mcp-gate runs it and exposes it over HTTP. A stdio server entry needs the command (`loxone-mcp serve`) and the environment: `LOXONE_URL`, `LOXONE_USER`, and the password through `LOXONE_PASSWORD` (from the gateway's secret environment) or `LOXONE_PASSWORD_FILE`. It also needs `HOME` (or `XDG_CONFIG_HOME` and `XDG_STATE_HOME`) to find the overlay and the state directory. Only one `serve` may use a state directory at a time.

Logs, including the audit records, go to stderr as JSON lines; a gateway that captures stderr puts them into its journal.

## The token

The first start acquires a token with the password and caches it in `<state dir>/token.json`; later starts and reconnects use the token, and it is refreshed before it expires. The token appears in the user's token list on the Miniserver under the client name `loxone-mcp`. `loxone-mcp logout` revokes it; deleting the file alone leaves it valid on the Miniserver until it expires. After a password change the cached token keeps working until it expires or is revoked; the next login uses the new password.

When the Miniserver rejects the credentials or blocks the client after failed logins, the server waits 5 minutes between attempts.

## Refreshing the map

The server reloads the structure file whenever its `lastModified` changes (checked on every connection; the Miniserver restarts after a configuration change, which reconnects). New controls appear read-only with generated ids. To name them, run `loxone-mcp overlay-init`, copy the new entries into the overlay, edit them, and restart the server.

## Checks

- `loxone-mcp check` connects with the same environment and prints the map with each device's current state and whether it is writable. It changes nothing.
- A live write test moves something in the home: ask the people living there first, use one reversible action (a light on and back off), and compare the answer's `before` and `after` with the Loxone app.
