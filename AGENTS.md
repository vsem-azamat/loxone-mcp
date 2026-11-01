# loxone-mcp Agent Instructions

The working contract for AI agents and contributors. Facts about how the server works live in [`docs/`](docs/README.md).

## What this is

An MCP server (stdio, Go, `github.com/modelcontextprotocol/go-sdk`) that lets agents read and control one Loxone Miniserver. It changes real things in an inhabited home, so the safety rules in [docs/safety.md](docs/safety.md) are the core of the project: a change that weakens them needs an explicit reason in the PR.

## Rules

- Everything in the repository is written in English and describes the current state: no plans, decision logs, status notes or scratch files.
- Never commit anything that identifies a real installation: Miniserver serials, Remote Connect hosts, real UUIDs, room or device names, overlays, structure files, tokens or passwords. Tests use the anonymised fixture in `internal/fakems/testdata/` and the fake Miniserver; examples use made-up data. `make repo` checks the obvious cases.
- Tests never call a real Miniserver. Live tests are manual; a live write moves something in someone's home and needs the owner's consent first.
- The password, the token and hashes never reach logs or error messages. Commands are built from validated parts only; no caller text goes into a command path.
- `AGENTS.md` is the only agent instruction file; do not add `CLAUDE.md` or tool-specific copies.
- No AI attribution in commits or PRs.

## Order of work

1. Read the docs of the area you change.
2. Write a failing test against the fixture or the fake Miniserver, then the change.
3. Update `docs/` when behaviour, tools, configuration or the safety rules change.
4. `make check` (and `make release-check` when the build or the release config changes) before pushing.

Branch from `main`, conventional commit messages (`feat:`, `fix:`, `docs:`, `chore:`, `test:`, `refactor:`), PR with green CI and an independent review (a fresh session or subagent), squash merge. Releases are `v*` tags on `main`.

## Map

| Path | Contents |
| --- | --- |
| `cmd/loxone-mcp/` | The binary: subcommands, environment, state-directory lock |
| `internal/loxone/` | Miniserver client: cloud DNS, WebSocket, token authentication and cache, structure file, event tables, keepalive, reconnects, commands |
| `internal/structure/` | Parsing of the structure file (LoxAPP3.json) |
| `internal/state/` | Store of the latest value of every state UUID |
| `internal/home/` | The home map: overlay, device kinds, safety rules, name resolution, state views, command builders, overlay generator |
| `internal/server/` | The MCP tools and the single write path with its audit record |
| `internal/fakems/` | Fake Miniserver and anonymised structure file for tests |
| `examples/home.yaml` | Example overlay with made-up data |
| `docs/` | How it works, configuration, tools, safety, operations |
| `scripts/repo-check.sh` | Repository policy run by `make repo` |
| `tools/`, `tools/lint/` | Pinned development tools, run with `go tool` |
| `.github/workflows/` | CI (`make check` jobs, release snapshot) and the tag-driven release |
