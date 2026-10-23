package auth

import (
	"fmt"
	"sync"

	"github.com/casbin/casbin/v2"
	"github.com/casbin/casbin/v2/model"
	redisadapter "github.com/casbin/redis-adapter/v2"
	"github.com/pie360/backend/internal/shared/config"
	"github.com/pie360/backend/internal/shared/logger"
)

// EnforcerWrapper wraps Casbin enforcer with additional functionality
type EnforcerWrapper struct {
	enforcer *casbin.SyncedEnforcer
	mu       sync.RWMutex
	logger   *logger.Logger
	config   *config.CasbinConfig
}

// NewEnforcer creates a new Casbin enforcer with Redis adapter
func NewEnforcer(cfg *config.Config, log *logger.Logger, _ interface{}) (*EnforcerWrapper, error) {
	// Load model from file
	m, err := model.NewModelFromFile(cfg.Casbin.ModelPath)
	if err != nil {
		return nil, fmt.Errorf("failed to load Casbin model: %w", err)
	}

	// Extract Redis connection info from config
	redisAddr := fmt.Sprintf("%s:%d", cfg.Redis.Host, cfg.Redis.Port)
	redisPassword := cfg.Redis.Password

	// Create Redis adapter
	adapter := redisadapter.NewAdpaterWithOption(
		redisadapter.WithAddress(redisAddr),
		redisadapter.WithPassword(redisPassword),
		redisadapter.WithNetwork("tcp"),
	)

	// Create synced enforcer
	enforcer, err := casbin.NewSyncedEnforcer(m, adapter)
	if err != nil {
		return nil, fmt.Errorf("failed to create enforcer: %w", err)
	}

	// Load policy from Redis
	if err := enforcer.LoadPolicy(); err != nil {
		return nil, fmt.Errorf("failed to load policy: %w", err)
	}

	// Enable auto-save
	enforcer.EnableAutoSave(true)

	wrapper := &EnforcerWrapper{
		enforcer: enforcer,
		logger:   log.WithComponent("casbin"),
		config:   &cfg.Casbin,
	}

	// Load default policies if empty
	if err := wrapper.loadDefaultPolicies(); err != nil {
		return nil, fmt.Errorf("failed to load default policies: %w", err)
	}

	return wrapper, nil
}

// Enforce checks if a request should be allowed
func (e *EnforcerWrapper) Enforce(sub, obj, act string) (bool, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enforcer.Enforce(sub, obj, act)
}

// EnforceWithContext checks with additional context
func (e *EnforcerWrapper) EnforceWithContext(ctx map[string]interface{}, sub, obj, act string) (bool, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	
	// Build request with context
	request := []interface{}{sub, obj, act}
	for k, v := range ctx {
		request = append(request, k, v)
	}
	
	return e.enforcer.Enforce(request...)
}

// AddPolicy adds a new policy rule
func (e *EnforcerWrapper) AddPolicy(sub, obj, act string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enforcer.AddPolicy(sub, obj, act)
}

// RemovePolicy removes a policy rule
func (e *EnforcerWrapper) RemovePolicy(sub, obj, act string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enforcer.RemovePolicy(sub, obj, act)
}

// AddRoleForUser assigns a role to a user
func (e *EnforcerWrapper) AddRoleForUser(user, role, domain string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if domain != "" {
		return e.enforcer.AddRoleForUserInDomain(user, role, domain)
	}
	return e.enforcer.AddRoleForUser(user, role)
}

// DeleteRoleForUser removes a role from a user
func (e *EnforcerWrapper) DeleteRoleForUser(user, role, domain string) (bool, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if domain != "" {
		return e.enforcer.DeleteRoleForUserInDomain(user, role, domain)
	}
	return e.enforcer.DeleteRoleForUser(user, role)
}

// GetRolesForUser gets all roles for a user
func (e *EnforcerWrapper) GetRolesForUser(user, domain string) ([]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if domain != "" {
		return e.enforcer.GetRolesForUserInDomain(user, domain), nil
	}
	roles, err := e.enforcer.GetRolesForUser(user)
	if err != nil {
		return nil, err
	}
	return roles, nil
}

// GetUsersForRole gets all users with a role
func (e *EnforcerWrapper) GetUsersForRole(role, domain string) ([]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	if domain != "" {
		return e.enforcer.GetUsersForRoleInDomain(role, domain), nil
	}
	users, err := e.enforcer.GetUsersForRole(role)
	if err != nil {
		return nil, err
	}
	return users, nil
}

// GetPolicy returns all policy rules
func (e *EnforcerWrapper) GetPolicy() ([][]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enforcer.GetPolicy(), nil
}

// GetGroupingPolicy returns all role assignments
func (e *EnforcerWrapper) GetGroupingPolicy() ([][]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enforcer.GetGroupingPolicy(), nil
}

// LoadPolicy reloads policy from storage
func (e *EnforcerWrapper) LoadPolicy() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enforcer.LoadPolicy()
}

// SavePolicy saves policy to storage
func (e *EnforcerWrapper) SavePolicy() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.enforcer.SavePolicy()
}

// loadDefaultPolicies loads default RBAC policies if none exist
func (e *EnforcerWrapper) loadDefaultPolicies() error {
	policies := e.enforcer.GetPolicy()
	if len(policies) > 0 {
		return nil // Policies already loaded
	}

	defaultPolicies := [][]string{
		// Super admin - full access
		{"super_admin", "*", "*"},
		
		// Institute admin - manage institute
		{"institute_admin", "institute", "read"},
		{"institute_admin", "institute", "write"},
		{"institute_admin", "users", "read"},
		{"institute_admin", "users", "write"},
		{"institute_admin", "roles", "read"},
		{"institute_admin", "roles", "write"},
		{"institute_admin", "academic", "read"},
		{"institute_admin", "academic", "write"},
		{"institute_admin", "finance", "read"},
		{"institute_admin", "finance", "write"},
		{"institute_admin", "hr", "read"},
		{"institute_admin", "hr", "write"},
		{"institute_admin", "communication", "read"},
		{"institute_admin", "communication", "write"},
		{"institute_admin", "analytics", "read"},
		{"institute_admin", "analytics", "write"},
		{"institute_admin", "settings", "read"},
		{"institute_admin", "settings", "write"},
		
		// Principal - academic leadership
		{"principal", "institute", "read"},
		{"principal", "users", "read"},
		{"principal", "academic", "read"},
		{"principal", "academic", "write"},
		{"principal", "attendance", "read"},
		{"principal", "attendance", "write"},
		{"principal", "grades", "read"},
		{"principal", "grades", "write"},
		{"principal", "analytics", "read"},
		{"principal", "communication", "read"},
		{"principal", "communication", "write"},
		
		// Teacher - class management
		{"teacher", "classes", "read"},
		{"teacher", "classes", "write"},
		{"teacher", "students", "read"},
		{"teacher", "assignments", "read"},
		{"teacher", "assignments", "write"},
		{"teacher", "submissions", "read"},
		{"teacher", "submissions", "write"},
		{"teacher", "grades", "read"},
		{"teacher", "grades", "write"},
		{"teacher", "attendance", "read"},
		{"teacher", "attendance", "write"},
		{"teacher", "communication", "read"},
		{"teacher", "communication", "write"},
		
		// Student - own data
		{"student", "classes", "read"},
		{"student", "assignments", "read"},
		{"student", "submissions", "read"},
		{"student", "submissions", "write"},
		{"student", "grades", "read"},
		{"student", "attendance", "read"},
		{"student", "communication", "read"},
		{"student", "profile", "read"},
		{"student", "profile", "write"},
		
		// Parent - child's data
		{"parent", "children:grades", "read"},
		{"parent", "children:attendance", "read"},
		{"parent", "children:assignments", "read"},
		{"parent", "communication", "read"},
		{"parent", "communication", "write"},
		
		// Accountant - finance
		{"accountant", "finance", "read"},
		{"accountant", "finance", "write"},
		{"accountant", "fees", "read"},
		{"accountant", "fees", "write"},
		{"accountant", "invoices", "read"},
		{"accountant", "invoices", "write"},
		{"accountant", "payments", "read"},
		{"accountant", "payments", "write"},
		{"accountant", "expenses", "read"},
		{"accountant", "expenses", "write"},
		{"accountant", "payroll", "read"},
		{"accountant", "payroll", "write"},
		
		// Registrar - enrollment and records
		{"registrar", "students", "read"},
		{"registrar", "students", "write"},
		{"registrar", "enrollment", "read"},
		{"registrar", "enrollment", "write"},
		{"registrar", "grades", "read"},
		{"registrar", "grades", "write"},
		{"registrar", "transcripts", "read"},
		{"registrar", "transcripts", "write"},
		{"registrar", "attendance", "read"},
		
		// HR - staff management
		{"hr", "staff", "read"},
		{"hr", "staff", "write"},
		{"hr", "contracts", "read"},
		{"hr", "contracts", "write"},
		{"hr", "leave", "read"},
		{"hr", "leave", "write"},
		{"hr", "payroll", "read"},
		{"hr", "performance", "read"},
		{"hr", "performance", "write"},
		{"hr", "recruitment", "read"},
		{"hr", "recruitment", "write"},
	}

	for _, policy := range defaultPolicies {
		if _, err := e.enforcer.AddPolicy(policy[0], policy[1], policy[2]); err != nil {
			e.logger.Warn().Err(err).Strs("policy", policy).Msg("Failed to add default policy")
		}
	}

	// Role hierarchy (inheritance)
	roleHierarchy := [][]string{
		{"institute_admin", "principal"},
		{"institute_admin", "teacher"},
		{"institute_admin", "accountant"},
		{"institute_admin", "registrar"},
		{"institute_admin", "hr"},
		{"principal", "teacher"},
	}

	for _, hierarchy := range roleHierarchy {
		if _, err := e.enforcer.AddRoleForUser(hierarchy[0], hierarchy[1]); err != nil {
			e.logger.Warn().Err(err).Strs("hierarchy", hierarchy).Msg("Failed to add role hierarchy")
		}
	}

	return e.enforcer.SavePolicy()
}

// CheckPermission checks if a user has permission for a resource/action
func (e *EnforcerWrapper) CheckPermission(userRole, resource, action, instituteID string) (bool, error) {
	// Check with institute context
	return e.EnforceWithContext(
		map[string]interface{}{"institute": instituteID},
		userRole, resource, action,
	)
}

// GetPermissionsForRole returns all permissions for a role
func (e *EnforcerWrapper) GetPermissionsForRole(role string) ([][]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.enforcer.GetFilteredPolicy(0, role), nil
}

// BuildRoleHierarchy builds the complete role hierarchy
func (e *EnforcerWrapper) BuildRoleHierarchy() (map[string][]string, error) {
	e.mu.RLock()
	defer e.mu.RUnlock()
	
	groupingPolicy := e.enforcer.GetGroupingPolicy()
	hierarchy := make(map[string][]string)
	
	for _, g := range groupingPolicy {
		if len(g) >= 2 {
			hierarchy[g[0]] = append(hierarchy[g[0]], g[1])
		}
	}
	
	return hierarchy, nil
}