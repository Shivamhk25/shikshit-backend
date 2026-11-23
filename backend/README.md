# Shikshit backend

The Go API behind Shikshit: one multi-tenant server (`cmd/shikshit-api`) on PostgreSQL 16 with row-level security,
a contract-first OpenAPI (`api/openapi`) and a Postgres job queue for imports.

## Run it

From the repository root:

```sh
make install     # npm tooling and Go modules
make infra-up    # Postgres, PgBouncer, Redis, MinIO and Mailhog in Docker
make migrate-up  # apply migrations
make api         # run the API on :8080
```

Configuration is read from `SHIKSHIT_*` environment variables (`internal/platform/server/config.go`): at least
`SHIKSHIT_DATABASE_URL` and `SHIKSHIT_JWT_SECRET`. The local Docker database user is `shikshit` with the development
password `shikshit_dev_password`; the API must connect as the non-superuser role created in `docker/initdb`.

Two-step sign-in (FND-05): Admins must set up an authenticator app on their next sign-in. Production also needs
`SHIKSHIT_MFA_KEY`, 32 random bytes in base64 (`openssl rand -base64 32`), which encrypts the authenticator secrets.
Keep it in the secrets store with the JWT secret; losing it means every user sets up two-step sign-in again. In
development a key is derived from the JWT secret, and `SHIKSHIT_MFA_REQUIRED_FOR_ADMINS=false` skips the Admin rule
(refused in production).

Files (FND-17): uploads go straight to S3-compatible storage with signed links. Locally the compose file runs MinIO
(console at http://localhost:9001, user and password `minioadmin`). Create a bucket named `shikshit` there once, then
start the API with `SHIKSHIT_STORAGE_ENDPOINT=localhost:9000 SHIKSHIT_STORAGE_BUCKET=shikshit
SHIKSHIT_STORAGE_ACCESS_KEY=minioadmin SHIKSHIT_STORAGE_SECRET_KEY=minioadmin SHIKSHIT_STORAGE_SECURE=false`. Without
these, file operations fail. Production needs all four keys (from the secrets store) and HTTPS, and the bucket must
allow browser PUT and GET from the web app's origins (CORS) and must not be public.
Uploads that are never confirmed are cleaned up: once an hour the API removes `pending` files older than one hour
from storage and marks them deleted (only when storage is configured).

Email (FND-19): invite and password-reset links are emailed through any SMTP server. Locally the compose file runs
MailHog (inbox at http://localhost:8025): start the API with `SHIKSHIT_SMTP_HOST=localhost SHIKSHIT_SMTP_PORT=1025
SHIKSHIT_SMTP_TLS=none SHIKSHIT_SMTP_FROM=no-reply@shikshit.test`. Links then point at `SHIKSHIT_WEB_ORIGIN` (default
http://localhost:5173). Without `SHIKSHIT_SMTP_HOST` nothing is emailed and the admin shares the link by hand. In
production use the provider's SMTP host, port 587 (STARTTLS) or 465 (TLS), `SHIKSHIT_SMTP_USERNAME` and
`SHIKSHIT_SMTP_PASSWORD` from the secrets store, and a sender address on a domain with SPF and DKIM set up; TLS
`none` is refused there. Queued emails are sealed with a key derived from `SHIKSHIT_MFA_KEY`, so changing that key
drops emails still in the queue.

## Layout

| Path | What lives there |
|---|---|
| `cmd/shikshit-api` | the server binary |
| `cmd/seed` | seeds a development institute |
| `api/openapi` | the contract; `make api-gen` regenerates Go handlers and the TypeScript client |
| `internal/platform` | tenancy, identity, access (authz), audit, the resource engine, the import engine, jobs, scope |
| `internal/modules` | the product modules (`aca`, `stu`, `iam`) and their importers |
| `internal/api/v1` | HTTP handlers, the permission table and tests |
| `migrations` | SQL migrations, applied in order |
| `scripts/gen_resources.py` | generates contract, handlers and mappings for master-data resources |

## Test

```sh
cd backend && go test -race ./...   # embedded PostgreSQL, no Docker needed
make api-check                      # the generated code matches the contract
```
