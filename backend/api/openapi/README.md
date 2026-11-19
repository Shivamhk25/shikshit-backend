# API contract (OpenAPI)

The single source of truth for the backend, web and mobile. **Change the spec first**, then generate.

```
backend/api/openapi/
  openapi.yaml            root: info, security, paths ($ref into modules/)
  components/common.yaml  shared schemas (Problem, PageInfo), parameters, headers, responses
  modules/<module>.yaml   one file per product module (platform.yaml today)
  redocly.yaml            lint rules (access annotation is mandatory)
  openapi.bundled.yaml    GENERATED single-file spec consumed by every generator
../oapi-codegen.yaml      Go generator config
../../internal/api/gen    GENERATED Go models + Echo routes + strict server interface
../../internal/api/v1     hand-written handlers implementing the generated interface
../../../packages/api-client  TS client + GENERATED types (src/generated/schema.d.ts) and Zod schemas (zod.ts)
```

## Commands (from the repo root)

| Command | What it does |
|---|---|
| `make api-lint` | Lint the contract |
| `make api-gen` | Lint → bundle → generate Go + TypeScript + Zod |
| `make api-check` | Regenerate and fail if anything differs from what is committed (used in CI) |
| `make mock` | Serve the contract with example responses on `http://127.0.0.1:4010` (no `/api/v1` prefix) |
| `make web-mock` | Web dev server pointed at the mock (run `make mock` first) |

Mobile against the mock: create the client with `baseUrl: 'http://<your-machine-ip>:4010'`.

## Rules for every operation (enforced by `make api-lint`)

- Exactly one access declaration: `x-permission: module.resource.action`, or `x-authenticated: true`, or `x-public: true`. See `docs/plan/01-access-model.md` §7.
- `operationId` (unique), `summary`, at least one tag.
- `x-parity` is one of `Both | Companion | Web-only | Platform` (`docs/plan/08-web-mobile-sync.md`).
- Errors use `application/problem+json` (`Problem`) with a stable `code`.
- Changes within `/api/v1` are additive only. Removing/renaming a field or making an input required needs a new API version.
- Money, posting and send operations require the `Idempotency-Key` header.

## Adding an operation

1. Add it to `modules/<module>.yaml` (and the `paths` map in `openapi.yaml`).
2. `make api-gen`.
3. `go build ./...` now fails until `internal/api/v1` implements the new method. That is intentional: the handler cannot drift from the contract.
4. Implement the handler, add the permission-matrix test, commit the spec **and** the regenerated files.

## Tool versions (pinned)

`@redocly/cli 1.34.24`, `@stoplight/prism-cli 5.14.2`, `oapi-codegen v2.4.1`, `openapi-typescript 7.13.0`, `orval 7.21.0`, `openapi-fetch 0.13.8`, `zod 3.25.76`. Newer majors of some of these need Node 22+, which is why they are pinned for Node 20.
