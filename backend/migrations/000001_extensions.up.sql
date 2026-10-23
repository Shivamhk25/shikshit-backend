-- 000001_extensions.up.sql
-- Enable required PostgreSQL extensions

-- UUID generation
CREATE EXTENSION IF NOT EXISTS "uuid-ossp";
CREATE EXTENSION IF NOT EXISTS "pgcrypto";

-- Case-insensitive text for emails
CREATE EXTENSION IF NOT EXISTS "citext";

-- Trigram for fuzzy search
CREATE EXTENSION IF NOT EXISTS "pg_trgm";

-- GIN index support for JSONB
CREATE EXTENSION IF NOT EXISTS "btree_gin";

-- Row Level Security (built-in 16+)
-- No extension needed

-- Audit logging
CREATE EXTENSION IF NOT EXISTS "pgaudit";

-- Vector embeddings for AI features
CREATE EXTENSION IF NOT EXISTS "vector";

-- Time-series data (attendance, metrics)
CREATE EXTENSION IF NOT EXISTS "timescaledb";

-- Comments
COMMENT ON EXTENSION "uuid-ossp" IS 'UUID generation functions';
COMMENT ON EXTENSION "pgcrypto" IS 'Cryptographic functions';
COMMENT ON EXTENSION "citext" IS 'Case-insensitive character string type';
COMMENT ON EXTENSION "pg_trgm" IS 'Trigram matching for fuzzy search';
COMMENT ON EXTENSION "btree_gin" IS 'GIN operator classes for btree';
COMMENT ON EXTENSION "pgaudit" IS 'Audit logging';
COMMENT ON EXTENSION "vector" IS 'Vector similarity search';
COMMENT ON EXTENSION "timescaledb" IS 'Time-series data management';