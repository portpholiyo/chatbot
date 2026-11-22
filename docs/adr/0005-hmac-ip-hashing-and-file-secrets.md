# 0005. HMAC-hashed IPs and file-based secrets

- Status: Accepted
- Date: 2026-11-22

## Context
Abuse analysis needs to correlate requests from one client, but storing raw IPs is unnecessary personal data. Secrets in environment variables leak through process listings, crash dumps and `docker inspect`.

## Decision
- Store only `HMAC(secret salt, IP)`; never log or persist raw addresses.
- Read every secret from a file path given by a `*_FILE` variable (Docker secrets), never from plain env or the repository.

## Consequences
- Requests can be correlated without keeping addresses; rotating the salt unlinks history.
- Secrets have file permissions and stay out of config dumps.
- Local runs need small secret files instead of exported variables.
