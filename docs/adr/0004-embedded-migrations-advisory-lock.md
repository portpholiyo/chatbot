# 0004. Embedded migrations applied under an advisory lock

- Status: Accepted
- Date: 2026-11-22

## Context
The service ships as a single container. A separate migration tool or job adds moving parts, and two instances starting together could race on schema changes.

## Decision
Embed numbered SQL files in the binary (`go:embed`) and apply them on boot inside a PostgreSQL advisory lock. Applied files are immutable; changes are new files.

## Consequences
- One artifact to deploy; schema always matches the binary.
- Concurrent starts serialize safely.
- No down-migrations; rollback is a forward fix or a restore from backup.
