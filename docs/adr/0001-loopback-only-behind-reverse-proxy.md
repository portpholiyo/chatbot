# 0001. Bind to loopback only, behind a reverse proxy

- Status: Accepted
- Date: 2026-11-22

## Context
Container-published ports listen on all interfaces by default and bypass the host firewall, so a service that is "firewalled" can still be reachable directly. The service also should not terminate TLS or handle slow or hostile clients itself.

## Options considered
1. Publish the port on all interfaces and rely on the host firewall: Docker's published ports are not governed by it.
2. Terminate TLS inside the service: more code and certificate handling in the application.
3. Bind to `127.0.0.1` only and put a reverse proxy in front for TLS, caching and routing.

## Decision
Option 3. The service listens on loopback only; the proxy in front is the only public entry point.

## Consequences
- No direct network path to the service; one place to apply TLS and edge policy.
- The proxy is a dependency; if it is down the service is unreachable.
- The service must not trust network position, so it still validates, rate limits and caps everything itself.
