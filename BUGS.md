# BUGS.md

| ID | Symptom | Repro | Severity | Status |
|----|---------|-------|----------|--------|
| B1 | Placeholder for first bug | - | - | - |
| B2 | `internal/tenant` (still wired in `cmd/api/main.go`) takes the institute from the client-supplied `X-Institute-ID` header before the JWT or host, so a caller can choose their tenant. Replaced by `internal/platform/tenancy` (host only + RLS-bound transactions, FND-04). Switch the router over and delete the old middleware when auth lands (FND-05/06). | Send `X-Institute-ID: <other tenant>` to any `RequireTenant` route | High | Fixed: the old server (`cmd/api`, `internal/tenant`, `internal/auth`, `internal/hub`) was removed; the only server is `cmd/shikshit-api` |
| B3 | Existing Go files are not gofmt-formatted (`gofmt -l .` lists 11). Left untouched to avoid unrelated churn; format in a dedicated commit. | `gofmt -l .` | Low | Fixed: the unformatted files were the old server's and are gone |
