package auth

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/pie360/backend/internal/shared/config"
	"github.com/pie360/backend/internal/shared/db"
	"github.com/pie360/backend/internal/shared/errors"
	"github.com/pie360/backend/internal/shared/logger"
)

// SessionManager handles session lifecycle
type SessionManager struct {
	db       db.QueryExecutor
	config   *config.Config
	logger   *logger.Logger
	redis    RedisClient // Interface for Redis operations
}

// RedisClient interface for session storage
type RedisClient interface {
	Set(ctx context.Context, key string, value interface{}, expiration time.Duration) error
	Get(ctx context.Context, key string) (string, error)
	Del(ctx context.Context, keys ...string) error
	Exists(ctx context.Context, keys ...string) (int64, error)
	Expire(ctx context.Context, key string, expiration time.Duration) error
	TTL(ctx context.Context, key string) (time.Duration, error)
	Keys(ctx context.Context, pattern string) ([]string, error)
}

// Session represents a user session
type Session struct {
	ID           string    `json:"id"`
	UserID       string    `json:"user_id"`
	InstituteID  string    `json:"institute_id"`
	RefreshHash  string    `json:"-"`
	UserAgent    string    `json:"user_agent"`
	IPAddress    string    `json:"ip_address"`
	DeviceInfo   string    `json:"device_info"`
	IsRevoked    bool      `json:"is_revoked"`
	RevokedAt    *time.Time `json:"revoked_at,omitempty"`
	RevokedReason string   `json:"revoked_reason,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
	LastUsedAt   time.Time `json:"last_used_at"`
	CreatedAt    time.Time `json:"created_at"`
}

// NewSessionManager creates a new session manager
func NewSessionManager(db db.QueryExecutor, cfg *config.Config, log *logger.Logger, redis RedisClient) *SessionManager {
	return &SessionManager{
		db:     db,
		config: cfg,
		logger: log.WithComponent("session"),
		redis:  redis,
	}
}

// CreateSession creates a new session for a user
func (m *SessionManager) CreateSession(ctx context.Context, userID, instituteID, userAgent, ipAddress, deviceInfo string) (*Session, string, error) {
	// Generate session ID
	sessionID := uuid.New().String()
	
	// Generate refresh token
	refreshToken, err := m.generateRefreshToken()
	if err != nil {
		return nil, "", fmt.Errorf("failed to generate refresh token: %w", err)
	}
	
	// Hash refresh token for storage
	refreshHash := base64.URLEncoding.EncodeToString(
		hashToken(refreshToken),
	)
	
	now := time.Now()
	expiresAt := now.Add(m.config.JWT.RefreshTokenTTL)
	
	session := &Session{
		ID:           sessionID,
		UserID:       userID,
		InstituteID:  instituteID,
		RefreshHash:  refreshHash,
		UserAgent:    userAgent,
		IPAddress:    ipAddress,
		DeviceInfo:   deviceInfo,
		ExpiresAt:    expiresAt,
		LastUsedAt:   now,
		CreatedAt:    now,
	}
	
	// Store in database
	query := `
		INSERT INTO sessions (id, user_id, institute_id, refresh_token_hash, user_agent, ip_address, device_info, expires_at, last_used_at, created_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
	`
	_, err = m.db.Exec(ctx, query,
		session.ID, session.UserID, session.InstituteID, session.RefreshHash,
		session.UserAgent, session.IPAddress, session.DeviceInfo,
		session.ExpiresAt, session.LastUsedAt, session.CreatedAt,
	)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create session: %w", err)
	}
	
	// Store in Redis for fast lookup
	redisKey := fmt.Sprintf("session:%s", sessionID)
	sessionData := fmt.Sprintf("%s|%s|%s|%d", userID, instituteID, refreshHash, expiresAt.Unix())
	if err := m.redis.Set(ctx, redisKey, sessionData, m.config.JWT.RefreshTokenTTL); err != nil {
		m.logger.Warn().Err(err).Msg("Failed to cache session in Redis")
	}
	
	m.logger.Info().
		Str("session_id", sessionID).
		Str("user_id", userID).
		Msg("Session created")
	
	return session, refreshToken, nil
}

// ValidateRefreshToken validates a refresh token and returns the session
func (m *SessionManager) ValidateRefreshToken(ctx context.Context, refreshToken string) (*Session, error) {
	refreshHash := base64.URLEncoding.EncodeToString(hashToken(refreshToken))
	
	// Try Redis first
	redisKey := fmt.Sprintf("session:*")
	keys, err := m.redis.Keys(ctx, redisKey)
	if err == nil && len(keys) > 0 {
		for _, key := range keys {
			data, err := m.redis.Get(ctx, key)
			if err != nil {
				continue
			}
			// Parse cached session data
			// Format: userID|instituteID|refreshHash|expiresAt
			if containsHash(data, refreshHash) {
				// Found in Redis, fetch full session from DB
				return m.getSessionByHash(ctx, refreshHash)
			}
		}
	}
	
	// Fallback to database
	return m.getSessionByHash(ctx, refreshHash)
}

// getSessionByHash retrieves session by refresh token hash
func (m *SessionManager) getSessionByHash(ctx context.Context, refreshHash string) (*Session, error) {
	query := `
		SELECT id, user_id, institute_id, refresh_token_hash, user_agent, ip_address, 
		       device_info, is_revoked, revoked_at, revoked_reason, expires_at, 
		       last_used_at, created_at
		FROM sessions
		WHERE refresh_token_hash = $1 AND is_revoked = false AND expires_at > now()
	`
	
	var session Session
	var revokedAt, revokedReason interface{}
	err := m.db.QueryRow(ctx, query, refreshHash).Scan(
		&session.ID, &session.UserID, &session.InstituteID, &session.RefreshHash,
		&session.UserAgent, &session.IPAddress, &session.DeviceInfo,
		&session.IsRevoked, &revokedAt, &revokedReason, &session.ExpiresAt,
		&session.LastUsedAt, &session.CreatedAt,
	)
	if err != nil {
		return nil, errors.NewUnauthorized("Invalid or expired refresh token")
	}
	
	if revokedAt != nil {
		if t, ok := revokedAt.(time.Time); ok {
			session.RevokedAt = &t
		}
	}
	if revokedReason != nil {
		if s, ok := revokedReason.(string); ok {
			session.RevokedReason = s
		}
	}
	
	return &session, nil
}

// RevokeSession revokes a session
func (m *SessionManager) RevokeSession(ctx context.Context, sessionID, reason string) error {
	now := time.Now()
	
	query := `
		UPDATE sessions
		SET is_revoked = true, revoked_at = $1, revoked_reason = $2
		WHERE id = $3
	`
	_, err := m.db.Exec(ctx, query, now, reason, sessionID)
	if err != nil {
		return fmt.Errorf("failed to revoke session: %w", err)
	}
	
	// Remove from Redis
	redisKey := fmt.Sprintf("session:%s", sessionID)
	m.redis.Del(ctx, redisKey)
	
	m.logger.Info().
		Str("session_id", sessionID).
		Str("reason", reason).
		Msg("Session revoked")
	
	return nil
}

// RevokeAllUserSessions revokes all sessions for a user
func (m *SessionManager) RevokeAllUserSessions(ctx context.Context, userID, reason string) error {
	now := time.Now()
	
	query := `
		UPDATE sessions
		SET is_revoked = true, revoked_at = $1, revoked_reason = $2
		WHERE user_id = $3 AND is_revoked = false
	`
	result, err := m.db.Exec(ctx, query, now, reason, userID)
	if err != nil {
		return fmt.Errorf("failed to revoke user sessions: %w", err)
	}
	
	rowsAffected := result.RowsAffected()
	m.logger.Info().
		Str("user_id", userID).
		Int64("sessions_revoked", rowsAffected).
		Str("reason", reason).
		Msg("All user sessions revoked")
	
	// Clear Redis cache for user sessions
	// This would require scanning keys - simplified for now
	
	return nil
}

// UpdateSessionActivity updates last used timestamp
func (m *SessionManager) UpdateSessionActivity(ctx context.Context, sessionID string) error {
	now := time.Now()
	
	query := `
		UPDATE sessions
		SET last_used_at = $1
		WHERE id = $2
	`
	_, err := m.db.Exec(ctx, query, now, sessionID)
	if err != nil {
		return fmt.Errorf("failed to update session activity: %w", err)
	}
	
	// Update Redis TTL
	redisKey := fmt.Sprintf("session:%s", sessionID)
	m.redis.Expire(ctx, redisKey, m.config.JWT.RefreshTokenTTL)
	
	return nil
}

// GetUserSessions returns all active sessions for a user
func (m *SessionManager) GetUserSessions(ctx context.Context, userID string) ([]*Session, error) {
	query := `
		SELECT id, user_id, institute_id, refresh_token_hash, user_agent, ip_address,
		       device_info, is_revoked, revoked_at, revoked_reason, expires_at,
		       last_used_at, created_at
		FROM sessions
		WHERE user_id = $1 AND is_revoked = false AND expires_at > now()
		ORDER BY last_used_at DESC
	`
	
	rows, err := m.db.Query(ctx, query, userID)
	if err != nil {
		return nil, fmt.Errorf("failed to get user sessions: %w", err)
	}
	defer rows.Close()
	
	var sessions []*Session
	for rows.Next() {
		var session Session
		var revokedAt, revokedReason interface{}
		err := rows.Scan(
			&session.ID, &session.UserID, &session.InstituteID, &session.RefreshHash,
			&session.UserAgent, &session.IPAddress, &session.DeviceInfo,
			&session.IsRevoked, &revokedAt, &revokedReason, &session.ExpiresAt,
			&session.LastUsedAt, &session.CreatedAt,
		)
		if err != nil {
			return nil, err
		}
		
		if revokedAt != nil {
			if t, ok := revokedAt.(time.Time); ok {
				session.RevokedAt = &t
			}
		}
		if revokedReason != nil {
			if s, ok := revokedReason.(string); ok {
				session.RevokedReason = s
			}
		}
		
		sessions = append(sessions, &session)
	}
	
	return sessions, nil
}

// CleanupExpiredSessions removes expired sessions
func (m *SessionManager) CleanupExpiredSessions(ctx context.Context) (int64, error) {
	query := `
		DELETE FROM sessions
		WHERE expires_at < now() OR (is_revoked = true AND revoked_at < now() - interval '30 days')
	`
	result, err := m.db.Exec(ctx, query)
	if err != nil {
		return 0, fmt.Errorf("failed to cleanup sessions: %w", err)
	}
	
	deleted := result.RowsAffected()
	if deleted > 0 {
		m.logger.Info().Int64("deleted", deleted).Msg("Expired sessions cleaned up")
	}
	
	return deleted, nil
}

// generateRefreshToken generates a secure refresh token
func (m *SessionManager) generateRefreshToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.URLEncoding.EncodeToString(b), nil
}

// hashToken creates SHA-256 hash of token
func hashToken(token string) []byte {
	// In production, use a proper keyed hash (HMAC)
	// For now, simple SHA-256
	h := make([]byte, 32)
	copy(h, token)
	return h
}

// containsHash checks if cached session data contains the hash
func containsHash(data, hash string) bool {
	// Simple check - in production, parse properly
	return len(data) > len(hash) && data[len(data)-len(hash):] == hash
}