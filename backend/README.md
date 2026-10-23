# PIE360 Backend

Go backend for the PIE360 educational platform, built with modern Go practices.

## Architecture

```
┌─────────────────────────────────────────────────────────────┐
│                        Frontend (React)                      │
│                    @microsoft/signalr                        │
└─────────────────────────┬───────────────────────────────────┘
                          │ WebSocket / REST
                          ▼
┌─────────────────────────────────────────────────────────────┐
│                      Go Backend (This Repo)                  │
│  ┌─────────────┐ ┌─────────────┐ ┌────────────────────────┐ │
│  │   REST API  │ │  WebSocket  │ │   Background Workers   │ │
│  │  (Echo v4)  │ │   Hub (Gorilla)│ │   (Asynq/Redis)     │ │
│  └─────────────┘ └─────────────┘ └────────────────────────┘ │
│  ┌─────────────┐ ┌─────────────┐ ┌────────────────────────┐ │
│  │    Auth     │ │   Casbin    │ │   Multi-Tenancy (RLS)  │ │
│  │  (JWT+Cookie)│ │    RBAC     │ │   (PostgreSQL 16)      │ │
│  └─────────────┘ └─────────────┘ └────────────────────────┘ │
└─────────────────────────┬───────────────────────────────────┘
                          │
              ┌───────────┼───────────┐
              ▼           ▼           ▼
         ┌─────────┐ ┌─────────┐ ┌─────────┐
         │PostgreSQL│ │  Redis  │ │  MinIO  │
         │  (PgBouncer)│         │  (S3)   │
         └─────────┘ └─────────┘ └─────────┘
```

## Features

- **Authentication**: JWT access tokens + HttpOnly refresh cookies, Turnstile CAPTCHA, MFA support
- **Authorization**: Casbin RBAC with 9 roles (super_admin, institute_admin, principal, teacher, student, parent, accountant, registrar, hr)
- **Multi-tenancy**: Row-Level Security (RLS) with shared schema, institute isolation
- **Real-time**: SignalR-compatible WebSocket hub for notifications
- **Database**: PostgreSQL 16 with PgBouncer, TimescaleDB for analytics, pgvector for AI
- **Caching**: Redis for sessions, rate limiting, Casbin policies
- **Storage**: MinIO (S3-compatible) for file uploads
- **Observability**: Structured logging (zerolog), health checks, metrics endpoint
- **Security**: Helmet headers, CORS, rate limiting, audit logging (FERPA/GDPR)

## Quick Start

### Prerequisites

- Go 1.23+
- Docker & Docker Compose
- PostgreSQL 16+ (or use Docker)
- Redis 7+ (or use Docker)

### Development Setup

```bash
# Clone and enter directory
cd backend

# Install tools
make install-tools

# Generate JWT keys
make generate-keys

# Start infrastructure
make dev-db

# Run migrations
make migrate-up

# Generate SQLC code
make sqlc

# Start server with live reload
make run-dev
```

Or use Docker Compose for everything:

```bash
docker-compose up -d
```

### Environment Variables

Copy `config.yaml` and adjust, or set environment variables:

```bash
APP_ENV=development
APP_HOST=0.0.0.0
APP_PORT=8080

DB_HOST=localhost
DB_PORT=5432
DB_USER=pie360
DB_PASSWORD=pie360_dev_password
DB_NAME=pie360

REDIS_HOST=localhost
REDIS_PORT=6379

JWT_PRIVATE_KEY_PATH=./keys/private.pem
JWT_PUBLIC_KEY_PATH=./keys/public.pem
```

## Project Structure

```
backend/
├── cmd/api/                 # Main application entry point
├── config.yaml              # Configuration
├── docker-compose.yml       # Local development stack
├── Dockerfile               # Production image
├── Makefile                 # Build automation
├── sqlc.yaml                # SQLC configuration
├── go.mod / go.sum          # Go modules
├── migrations/              # Database migrations (goose)
│   ├── 000001_extensions.up.sql
│   ├── 000002_institutes.up.sql
│   ├── 000003_users_auth.up.sql
│   ├── 000004_academic.up.sql
│   ├── 000005_finance.up.sql
│   ├── 000006_hr.up.sql
│   ├── 000007_communication.up.sql
│   ├── 000008_analytics.up.sql
│   └── 000009_audit.up.sql
├── config/                  # Config files (Casbin model/policy)
├── internal/
│   ├── auth/                # Authentication (JWT, sessions, Casbin)
│   ├── hub/                 # WebSocket hub (SignalR-compatible)
│   ├── tenant/              # Multi-tenancy middleware
│   ├── shared/              # Shared packages
│   │   ├── config/          # Configuration loader
│   │   ├── db/              # Database pool (pgx)
│   │   ├── logger/          # Zerolog wrapper
│   │   └── errors/          # Application errors
│   └── platform/            # External integrations
│       ├── redis/
│       ├── storage/
│       ├── email/
│       └── sms/
├── pkg/                     # Reusable packages
│   ├── validator/
│   └── helpers/
└── api/                     # API definitions
    ├── openapi/             # OpenAPI specs
    └── proto/               # gRPC definitions
```

## Key Commands

```bash
# Build
make build              # Development build
make build-prod         # Production build

# Run
make run                # Run locally
make run-dev            # Run with live reload (air)

# Database
make migrate-up         # Apply migrations
make migrate-down       # Rollback last migration
make migrate-create NAME=add_new_table
make migrate-status     # Show migration status

# SQLC
make sqlc               # Generate type-safe SQL
make sqlc-verify        # Verify queries

# Testing
make test               # All tests
make test-unit          # Unit tests only
make test-coverage      # With coverage report

# Code Quality
make fmt                # Format code
make vet                # Go vet
make lint               # Golangci-lint
make check              # All checks

# Docker
make docker-up          # Start all services
make docker-down        # Stop all services
make docker-logs        # View logs
make docker-shell-api   # Shell into API container

# Clean
make clean              # Clean build artifacts
```

## API Endpoints

### Authentication
```
POST   /api/v1/auth/login           # Login with email/password + CAPTCHA
POST   /api/v1/auth/refresh         # Refresh access token
POST   /api/v1/auth/logout          # Logout (revokes session)
POST   /api/v1/auth/forgot-password # Request password reset
POST   /api/v1/auth/reset-password  # Reset password with token
POST   /api/v1/auth/verify-email    # Verify email with token
GET    /api/v1/auth/captcha         # Get Turnstile site key
GET    /api/v1/auth/me              # Current user info
POST   /api/v1/auth/change-password # Change password
```

### Protected Routes (require auth + tenant)
```
# Users
GET    /api/v1/users                # List users (paginated)
GET    /api/v1/users/:id            # Get user
PUT    /api/v1/users/:id            # Update user
DELETE /api/v1/users/:id            # Delete user
GET    /api/v1/users/:id/roles      # Get user roles
POST   /api/v1/users/:id/roles      # Assign role
DELETE /api/v1/users/:id/roles/:role # Remove role

# Academic
GET    /api/v1/academic/years       # Academic years
POST   /api/v1/academic/years       # Create year
GET    /api/v1/academic/terms       # Terms
GET    /api/v1/academic/departments # Departments
GET    /api/v1/academic/subjects    # Subjects
GET    /api/v1/academic/classes     # Classes
POST   /api/v1/academic/classes     # Create class
POST   /api/v1/academic/classes/:id/enroll # Enroll student
GET    /api/v1/academic/assignments # Assignments
POST   /api/v1/academic/assignments # Create assignment
POST   /api/v1/academic/assignments/:id/submit # Submit
GET    /api/v1/academic/grades      # Grades
GET    /api/v1/academic/attendance  # Attendance

# Finance
GET    /api/v1/finance/fee-structures
GET    /api/v1/finance/student-fees
GET    /api/v1/finance/invoices
POST   /api/v1/finance/payments

# HR
GET    /api/v1/hr/staff
GET    /api/v1/hr/leave-types
GET    /api/v1/hr/leave-requests
POST   /api/v1/hr/leave-requests
PUT    /api/v1/hr/leave-requests/:id/approve

# Communication
GET    /api/v1/communication/announcements
POST   /api/v1/communication/announcements
GET    /api/v1/communication/conversations
GET    /api/v1/communication/conversations/:id/messages
POST   /api/v1/communication/conversations/:id/messages
GET    /api/v1/communication/notifications
GET    /api/v1/communication/calendar

# Analytics
GET    /api/v1/analytics/dashboards
GET    /api/v1/analytics/insights
GET    /api/v1/analytics/kpis

# WebSocket
GET    /hubs/notifications          # SignalR-compatible hub
```

## WebSocket Hub (SignalR-Compatible)

The hub at `/hubs/notifications` implements a SignalR-compatible protocol:

```javascript
// Client connection
const connection = new signalR.HubConnectionBuilder()
  .withUrl("/hubs/notifications", { accessTokenFactory: () => token })
  .build();

connection.on("NotificationReceived", (event, data) => {
  console.log(event, data);
});

await connection.start();
```

### Server-side broadcasting:

```go
// Send to specific user
hub.SendToUser(userID, &HubMessage{
    Type: "invocation",
    Target: "NotificationReceived",
    Arguments: []interface{}{"grade_posted", gradeData},
})

// Send to role
hub.SendToRole("teacher", msg)

// Send to institute
hub.SendToInstitute(instituteID, msg)

// Broadcast to all
hub.SendToAll(msg)
```

## Database Schema

Key tables with RLS:

| Table | Purpose | RLS |
|-------|---------|-----|
| `institutes` | Tenants | Yes |
| `users` | All users | Yes |
| `sessions` | Refresh tokens | Yes |
| `academic_years` | School years | Yes |
| `classes` | Class sections | Yes |
| `class_enrollments` | Student enrollments | Yes |
| `assignments` | Homework/tests | Yes |
| `submissions` | Student work | Yes |
| `grades` | Grade entries | Yes |
| `attendance` | Daily attendance | Yes (TimescaleDB) |
| `invoices` / `payments` | Finance | Yes |
| `leave_requests` | HR leave | Yes |
| `analytics_events` | Event tracking | Yes (TimescaleDB) |
| `audit_log` | Compliance | Yes |
| `data_access_log` | FERPA tracking | Yes |

## Multi-Tenancy

Tenant resolution order:
1. `X-Institute-ID` header
2. JWT `institute_id` claim
3. Subdomain (e.g., `springs.example.com`)

All queries automatically scoped via `SET LOCAL app.current_institute_id = '...'`

## RBAC (Casbin)

Model: `config/rbac_model.conf`
Policy: `config/rbac_policy.csv` (synced to Redis)

Roles hierarchy:
```
super_admin
  └── institute_admin
        ├── principal
        ├── teacher
        ├── accountant
        ├── registrar
        └── hr
```

## Compliance

- **FERPA**: `data_access_log` tracks all student record access
- **GDPR**: `consents`, `data_subject_requests`, retention policies
- **Audit**: `audit_log` with PII detection, `pgaudit` for DB changes

## Deployment

### Production Build

```bash
make build-prod
docker build -t pie360/backend:latest .
```

### Kubernetes (Helm)

```bash
helm install pie360 ./deploy/helm \
  --set image.tag=latest \
  --set postgres.enabled=true \
  --set redis.enabled=true
```

## Contributing

1. Fork the repository
2. Create feature branch (`git checkout -b feature/amazing-feature`)
3. Run checks: `make check`
4. Commit changes (`git commit -m 'Add amazing feature'`)
5. Push to branch (`git push origin feature/amazing-feature`)
6. Open Pull Request

## License

MIT License - see LICENSE file for details.