# Architecture Decision Records

One file per significant decision: context, options, decision, consequences. Add a new ADR in the same change as the decision; never rewrite an accepted one, supersede it.

| # | Decision | Status |
|---|---|---|
| [0001](0001-loopback-only-behind-reverse-proxy.md) | Bind to loopback only, behind a reverse proxy | Accepted |
| [0002](0002-two-stage-guard-then-answer.md) | Two-stage LLM pipeline: cheap guard, then answer | Accepted |
| [0003](0003-interfaces-at-the-edges.md) | Interfaces at the edges (LLM, store) for testability | Accepted |
| [0004](0004-embedded-migrations-advisory-lock.md) | Embedded migrations applied under an advisory lock | Accepted |
| [0005](0005-hmac-ip-hashing-and-file-secrets.md) | HMAC-hashed IPs and file-based secrets | Accepted |

Template: [template.md](template.md)
