# Security (FND-26, first slice)

Shikshit holds children's data. This page says what protects it today, how to check it, and what is still open.
Nothing here is a claim that was not tested; open items are listed at the end.

## Threats we design against

| Threat | Main defence |
|---|---|
| One school reads another school's data | Row-level security on every table, every request in a tenant-bound transaction |
| A staff member sees more than their role allows | Access engine with scopes (a narrower scope never sees institute-wide data), contract rule on every operation |
| Password guessing and credential stuffing | argon2id hashes, account lockout, per-address rate limits (below), two-step sign-in (required for Admin in production) |
| Stolen or replayed session | Short access tokens, rotating refresh tokens with reuse detection, web refresh token in an httpOnly cookie |
| Cross-site request forgery | `SameSite=Strict` cookie, `X-Client: web` required to use the cookie on refresh, Origin check on cookie-bearing writes |
| Data leaking through logs, caches or errors | Errors are problem+json with stable codes, internals never returned, `Cache-Control: no-store` on every response |
| Abuse of a public endpoint | Body size limits, per-address rate limits, upload size limit |
| Vulnerable dependency | `make security` (govulncheck, gosec, npm audit) |

## Enforced now

**Rate limiting** (`internal/platform/ratelimit`, wired in `internal/platform/server/security.go`)

- Token bucket per client address, in process, standard library only.
- A strict bucket (default 20 per minute, burst 10) shared by all operations that check a secret: sign-in, two-step
  verify and setup confirm, invite accept, support-visit redeem, for institute users and for platform users.
  Everything else shares a general bucket (default 1200 per minute, burst 200). `/livez` and CORS preflights are not
  counted.
- The client address comes from the server's IP extractor: the TCP peer, or the proxy-appended `X-Forwarded-For` entry
  only when `SHIKSHIT_TRUST_FORWARDED_HOST=true`. IPv6 clients are grouped by /64.
- Refused requests get `429`, `Retry-After` (whole seconds) and `application/problem+json` with code `rate_limited`.
  The limiter runs after CORS, so the web app can read the 429.
- Memory is bounded by `SHIKSHIT_RATE_LIMIT_MAX_KEYS` (least recently used key is dropped; idle keys are swept).
- Settings: `SHIKSHIT_RATE_LIMIT_ENABLED`, `_GENERAL_PER_MIN`, `_STRICT_PER_MIN`, `_MAX_KEYS`. Disabling is refused in
  production.
- **Each API instance counts on its own.** With N instances a client can make up to N times the limit. A shared
  (Redis) limiter is the follow-up. Behind a proxy without `SHIKSHIT_TRUST_FORWARDED_HOST=true`, all clients share the
  proxy's address and one bucket, so set it.

**Response headers** (every response, including errors and 429): `X-Content-Type-Options: nosniff`,
`Referrer-Policy: no-referrer`, `Cache-Control: no-store` and `Pragma: no-cache`, a CSP of
`default-src 'none'; frame-ancestors 'none'; base-uri 'none'; form-action 'none'`, `X-Frame-Options: DENY`,
`Cross-Origin-Opener-Policy: same-origin`, `Cross-Origin-Resource-Policy: same-site`, a `Permissions-Policy` that turns
off camera, microphone, geolocation, payment and USB, and `X-Permitted-Cross-Domain-Policies: none`.
`Strict-Transport-Security: max-age=31536000; includeSubDomains` is sent only when `SHIKSHIT_ENV=production` (it would
pin `localhost` otherwise). Preload is deliberately not set: it is hard to undo, so it is a founder decision.

**Cookies and CSRF**

- Refresh cookies are `HttpOnly`, `SameSite=Strict`, `Secure` in production with the `__Secure-` prefix, and limited
  to the auth path (`/api/v1/auth`, `/api/v1/platform/auth`).
- The cookie is used for refresh only when the request carries `X-Client: web`; a cross-site form cannot add it.
- New: a state-changing request that carries a cookie and an `Origin` that is neither the API's own host nor listed in
  `SHIKSHIT_ALLOWED_ORIGINS` is refused with `403 origin_not_allowed`. This covers sibling subdomains (other schools'
  pages are the same *site*, so `SameSite` does not stop them) and `logout`, which has no `X-Client` check. Mobile apps
  send no cookies and no `Origin`.
- CORS allows only exact origins; a wildcard is refused at start-up.

**Other**: request bodies are limited to 1 MB (11 MB for the import upload); server timeouts are set; secrets come from
the environment only (production refuses a missing MFA key, storage keys, or insecure storage/SMTP settings); start-up
errors never echo secrets; authenticator secrets are encrypted at rest with `SHIKSHIT_MFA_KEY`.

## How to run the scans

```sh
make security        # govulncheck + gosec (high severity and confidence) + npm audit --audit-level=high
```

Tools run through `go run <tool>@<pinned version>` and are not in `go.mod`. They need network access. CI should call
this target from a `ci/` branch workflow. Frontend, mobile and shared: `npm audit --omit=dev` in each repo.

## Known scan findings (2026-11-23, updated for A-16)

A-16 moved the backend to Go 1.27 (`go.mod` `go 1.27.0`; images `golang:1.27.4-alpine3.23` and `alpine:3.23`) and took
Echo 4.16.6, pgx 5.13.0, excelize 2.11.1, minio-go 7.4.1, x/net 0.62.0, x/text 0.43.0 and klauspost/compress 1.20.2.
govulncheck v1.1.4 went from 21 or more findings to the ones below. Run it with Go 1.27.4 or newer: an older patch
release also reports the `net/http` finding GO-2026-6859, which 1.27.4 fixes.

| Finding | State | Why it is open |
|---|---|---|
| GO-2026-6875 Echo reverse proxy forwards a client `X-Real-IP` upstream | Open, no fixed version exists | We do not use Echo's proxy middleware. govulncheck reports it because the module is imported. Re-check on each Echo release. |
| GO-2026-6872 Echo rate limiter bypass through `X-Forwarded-For` | Open, no fixed version exists | We use our own limiter (`internal/platform/ratelimit`) and our own IP extractor, not Echo's `RateLimiter` middleware. |
| `golang.org/x/crypto` fix only in an unreleased pseudo-version | Open, code does not call it | We take released versions only. Bump when a release carries the fix. |

The contract tooling has one `zod` advisory that needs a major version; it is unchanged.

## Pen-test checklist

Run against a staging copy, with two institutes and users in each role. Record result and date next to each line.

Authentication
- [ ] Sign-in with wrong password 25 times from one address returns 429 with `Retry-After`; from another address still works.
- [ ] Wrong password for one user repeatedly locks that user (423) and an Admin can unlock.
- [ ] Same message and similar timing for unknown user, wrong password and disabled account.
- [ ] Two-step code cannot be replayed within its window; repeated wrong codes are refused (and by the strict rate limit).
- [ ] Refresh token reuse ends the whole session family. Logout invalidates the refresh token server-side.
- [ ] Access token of institute A is refused by institute B's host.
- [ ] Invite and reset links are single use, expire, and are not guessable.
- [ ] Support-visit token cannot write and every read appears in the institute's audit trail.

Authorisation and tenancy
- [ ] For every operation, call with each role and with a narrower scope; compare with the permission table.
- [ ] Change an `id` in a path to another institute's record: 404, never 403 with content.
- [ ] Teacher scoped to one class-section cannot list or open other sections' students.
- [ ] Parent sees only linked children (and not a link marked "no access").
- [ ] With the database role used by the API, try to read another tenant's rows without the tenant setting: zero rows.
- [ ] Bulk import and export cannot reach beyond the user's scope.

Input and output
- [ ] Oversized body (over 1 MB, over 11 MB on upload) gets 413.
- [ ] SQL metacharacters, very long strings, unicode tricks and null bytes in every text field.
- [ ] Upload: wrong type, wrong extension, a spreadsheet with formulas, zip bomb, huge row count.
- [ ] Response never contains password hashes, tokens, MFA secrets or storage keys.
- [ ] Error bodies carry a code and request id only; no stack traces or SQL.

Transport and browser
- [ ] HSTS present in production; no mixed content; cookies carry `Secure`, `HttpOnly`, `SameSite=Strict`.
- [ ] Cross-origin request from an unlisted origin gets no CORS headers; a cookie-bearing POST from it gets 403.
- [ ] `Cache-Control: no-store` on every JSON response; the CDN does not cache the API.

Operations
- [ ] No secrets in the repository or its history (`git log -p` scan), container image or logs.
- [ ] `make security` is clean or every finding has an owner and a date.
- [ ] Backups restore; the restore does not cross tenants.

## Not done yet

| Item | Status |
|---|---|
| PII encryption at rest (column-level for sensitive student and guardian fields) | Not started. Needs a key-management decision from the founder. Database and disk encryption belong to the managed Postgres. |
| Secrets in a vault | Secrets are read from environment variables only and are not in the repository. A vault (cloud secret manager) injecting them at deploy is not set up. |
| Image scanning (Trivy) | Not set up. Needs CI (`ci/` branch). |
| CI jobs for `make security` | Not added in this change by rule; commands are in the Makefile. |
| Redis-backed rate limits shared across instances | Not started. |
| Per-account (not only per-address) throttling on sign-in | Account lockout exists; a per-user-name limiter in front of it is not added. |
| `429` in the OpenAPI contract | The response is not yet declared per operation; clients treat it as the standard problem shape. |
| Web CSP-compatible build | Web repo not changed in this task. Needs a nonce or hash policy once the frontend sets CSP on its host. |
| Go and Docker base-image upgrade | Needed for several dependency fixes (see PR). |
