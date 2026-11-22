# chatbot

A small, production-minded Go service behind the portfolio assistant on [portpholiyo.com](https://portpholiyo.com). Visitors can ask questions **only about the site owner**; everything else is refused. Answers stream over Server-Sent Events and every exchange is stored in PostgreSQL.

> This is the public showcase of a privately run project. The knowledge file in this repo is a trimmed sample; some production features live only in the private repo.

**Start here:** [Architecture](docs/architecture.md) · [Design decisions (ADRs)](docs/adr/README.md)

## Highlights
- **Two-stage LLM pipeline:** a cheap topic guard (fails closed) before the answer call, so off-topic traffic costs almost nothing.
- **Defence in depth:** strict input validation, per-IP token bucket, global daily cap, concurrency limit, server-side history (client history is never trusted).
- **Privacy by construction:** raw IPs are never stored (HMAC only), secrets only via `*_FILE`.
- **Operable:** health endpoint, structured request logs, graceful shutdown, distroless non-root image, CI with `-race`.
- **Testable:** the LLM and the store are interfaces; the suite runs with no network and no real model.

## API
`POST /api/chat {session_id, message}` → SSE events `delta`, `done`, `error`. `GET /healthz`. `GET /admin/messages` (bearer token, disabled unless configured).

## Develop
```
go vet ./... && go test -race ./...
docker build -t chatbot .
```
Configuration is environment-based; see `internal/config/config.go` for every variable and default. Secrets are read from files (`DB_PASSWORD_FILE`, `ANTHROPIC_API_KEY_FILE`, `IP_SALT_FILE`, optional `ADMIN_TOKEN_FILE`).

## License
MIT, see [LICENSE](LICENSE).
