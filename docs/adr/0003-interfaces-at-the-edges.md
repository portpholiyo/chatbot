# 0003. Interfaces at the edges (LLM, store) for testability

- Status: Accepted
- Date: 2026-11-22

## Context
The two external dependencies, the LLM API and PostgreSQL, are slow, costly or stateful. Tests that need them are flaky and cannot run in CI without secrets.

## Decision
Define small interfaces (`llm.Client`, the store used by the server) at the consuming side and inject them in `main`. Tests supply fakes and exercise the real HTTP handlers, limits, SSE framing and refusal paths.

## Consequences
- `go test -race ./...` runs offline in seconds, with no secrets.
- Fakes must be kept honest; real end-to-end is verified separately against an isolated stack.
