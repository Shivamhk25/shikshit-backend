# Legacy migrations (never applied)

These nine files are the original schema drafted before the product plan was finalised. They were **never applied to any database** and cannot run on a stock Postgres image (they require `pgaudit`, `vector` and `timescaledb`), so they are kept here for reference only.

They are **not** executed by any tool: migration runners read `backend/migrations/*.up.sql` non-recursively.

They are being replaced module by module. Each product module redesigns its own tables (with tenant-scoped RLS, the shared role/permission model and academic-year scoping) when its first work item starts, and may borrow column ideas from here. Delete a file when its module has been rebuilt.

| Legacy file | Replaced by |
|---|---|
| `000001_extensions` | `000001_extensions` (only extensions available in stock Postgres) |
| `000002_institutes` | `000002_tenancy` (`tenants`) |
| `000003_users_auth` | `000003_identity`, `000004_access` (no role enum; roles are data) |
| `000004_academic` | ACA module (not yet) |
| `000005_finance` | FEE / HRP modules (not yet) |
| `000006_hr` | HRP module (not yet) |
| `000007_communication` | COM module (not yet) |
| `000008_analytics` | RPT / DSH modules (not yet) |
| `000009_audit` | `000005_audit` |
