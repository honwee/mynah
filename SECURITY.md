# Security Policy

## Reporting a vulnerability

Please report security issues **privately** — open a regular issue only for
non-sensitive hardening suggestions.

- Preferred: open a [private security advisory](../../security/advisories/new)
  on the repository (if the hosting platform supports it), or
- Email the maintainer address listed on the repository profile with subject
  `[SECURITY] Mynah`.

You should get an acknowledgement within 72 hours. Please include a minimal
reproduction and the deployment shape (compose / bare-process, which engines).

## Deployment model & what to know before exposing anything

Mynah has **two HTTP surfaces** with different trust levels:

| Surface | Default bind | Auth | Notes |
|---|---|---|---|
| Visitor (`/offer`, `/human`, `/interrupt_talk`, `/channel/*`) | `:8020` / `:8443` | none (channels support token / domain / CIDR) | every accepted offer consumes GPU-backed session capacity |
| Admin (`/api/v1/*`, console) | `127.0.0.1:9080` | Bearer JWT | binds loopback by default — keep it that way, or front with your own TLS + network controls |

Hardening notes:

- **Initial admin password is printed to the process log** on first start and
  the account is flagged must-change (the console forces a password change on
  first login). Treat early logs as sensitive; rotate the password immediately.
- **Visitor endpoints are rate-limited per IP** (default 30 req/min,
  `--visitor-rate-limit`, 0 disables). This bounds request abuse, not GPU
  capacity — the avatar worker serves one session at a time and published
  channels enforce `max_concurrent`. For a public internet deployment, put
  cored behind your own gateway/WAF and use **published channels** (token /
  allowed-domain / CIDR) rather than exposing the raw `/offer` route.
- **JWT secret** is generated on first start and persisted in the `settings`
  table; admin tokens expire after 24h.
- **Uploads** (training video, voice reference, motion video) are size-capped,
  streamed to disk (never buffered in RAM), and require an explicit `consent`
  field; see `docs/compliance.md`.
- TLS: cored self-signs `tls/cored.{crt,key}` if absent — fine for LAN
  testing, replace with real certificates for anything user-facing (mic
  capture requires a secure origin anyway).

## Supported versions

Security fixes land on `main`. Pin a tagged release and track the changelog.
