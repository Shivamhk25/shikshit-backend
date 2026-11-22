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
