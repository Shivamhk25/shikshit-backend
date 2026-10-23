package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

// Config holds all application configuration
type Config struct {
	App        AppConfig        `mapstructure:"app"`
	Database   DatabaseConfig   `mapstructure:"database"`
	Redis      RedisConfig      `mapstructure:"redis"`
	JWT        JWTConfig        `mapstructure:"jwt"`
	Cookie     CookieConfig     `mapstructure:"cookie"`
	Casbin     CasbinConfig     `mapstructure:"casbin"`
	WebSocket  WebSocketConfig  `mapstructure:"websocket"`
	CORS       CORSConfig       `mapstructure:"cors"`
	Logging    LoggingConfig    `mapstructure:"logging"`
	Email      EmailConfig      `mapstructure:"email"`
	Storage    StorageConfig    `mapstructure:"storage"`
	RateLimit  RateLimitConfig  `mapstructure:"rate_limit"`
	Metrics    MetricsConfig    `mapstructure:"metrics"`
	Tracing    TracingConfig    `mapstructure:"tracing"`
}

// AppConfig holds application-level settings
type AppConfig struct {
	Name                   string        `mapstructure:"name" env:"APP_NAME"`
	Version                string        `mapstructure:"version" env:"APP_VERSION"`
	Env                    string        `mapstructure:"env" env:"APP_ENV"`
	Host                   string        `mapstructure:"host" env:"APP_HOST"`
	Port                   int           `mapstructure:"port" env:"APP_PORT"`
	ReadTimeout            time.Duration `mapstructure:"read_timeout" env:"APP_READ_TIMEOUT"`
	WriteTimeout           time.Duration `mapstructure:"write_timeout" env:"APP_WRITE_TIMEOUT"`
	IdleTimeout            time.Duration `mapstructure:"idle_timeout" env:"APP_IDLE_TIMEOUT"`
	ShutdownGracePeriod    time.Duration `mapstructure:"shutdown_grace_period" env:"APP_SHUTDOWN_GRACE_PERIOD"`
}

// DatabaseConfig holds database connection settings
type DatabaseConfig struct {
	Host            string        `mapstructure:"host" env:"DB_HOST"`
	Port            int           `mapstructure:"port" env:"DB_PORT"`
	User            string        `mapstructure:"user" env:"DB_USER"`
	Password        string        `mapstructure:"password" env:"DB_PASSWORD"`
	Name            string        `mapstructure:"name" env:"DB_NAME"`
	SSLMode         string        `mapstructure:"ssl_mode" env:"DB_SSL_MODE"`
	MaxConns        int32         `mapstructure:"max_conns" env:"DB_MAX_CONNS"`
	MinConns        int32         `mapstructure:"min_conns" env:"DB_MIN_CONNS"`
	MaxConnLifetime time.Duration `mapstructure:"max_conn_lifetime" env:"DB_MAX_CONN_LIFETIME"`
	MaxConnIdleTime time.Duration `mapstructure:"max_conn_idle_time" env:"DB_MAX_CONN_IDLE_TIME"`
	HealthCheckPeriod time.Duration `mapstructure:"health_check_period" env:"DB_HEALTH_CHECK_PERIOD"`
	PgBouncer       PgBouncerConfig `mapstructure:"pgbouncer"`
}

// PgBouncerConfig holds PgBouncer connection settings
type PgBouncerConfig struct {
	Host string `mapstructure:"host" env:"PGBOUNCER_HOST"`
	Port int    `mapstructure:"port" env:"PGBOUNCER_PORT"`
}

// DSN returns the database connection string
func (d *DatabaseConfig) DSN() string {
	host := d.Host
	port := d.Port
	if d.PgBouncer.Host != "" && d.PgBouncer.Port > 0 {
		host = d.PgBouncer.Host
		port = d.PgBouncer.Port
	}
	return fmt.Sprintf("postgres://%s:%s@%s:%d/%s?sslmode=%s",
		d.User, d.Password, host, port, d.Name, d.SSLMode)
}

// RedisConfig holds Redis connection settings
type RedisConfig struct {
	Host            string        `mapstructure:"host" env:"REDIS_HOST"`
	Port            int           `mapstructure:"port" env:"REDIS_PORT"`
	Password        string        `mapstructure:"password" env:"REDIS_PASSWORD"`
	DB              int           `mapstructure:"db" env:"REDIS_DB"`
	PoolSize        int           `mapstructure:"pool_size" env:"REDIS_POOL_SIZE"`
	MinIdleConns    int           `mapstructure:"min_idle_conns" env:"REDIS_MIN_IDLE_CONNS"`
	DialTimeout     time.Duration `mapstructure:"dial_timeout" env:"REDIS_DIAL_TIMEOUT"`
	ReadTimeout     time.Duration `mapstructure:"read_timeout" env:"REDIS_READ_TIMEOUT"`
	WriteTimeout    time.Duration `mapstructure:"write_timeout" env:"REDIS_WRITE_TIMEOUT"`
	Sentinel        SentinelConfig `mapstructure:"sentinel"`
}

// SentinelConfig holds Redis Sentinel settings
type SentinelConfig struct {
	Enabled     bool     `mapstructure:"enabled" env:"REDIS_SENTINEL_ENABLED"`
	MasterName  string   `mapstructure:"master_name" env:"REDIS_SENTINEL_MASTER"`
	Addresses   []string `mapstructure:"addresses" env:"REDIS_SENTINEL_ADDRESSES"`
}

// Addr returns the Redis address
func (r *RedisConfig) Addr() string {
	if r.Sentinel.Enabled {
		return ""
	}
	return fmt.Sprintf("%s:%d", r.Host, r.Port)
}

// JWTConfig holds JWT settings
type JWTConfig struct {
	PrivateKeyPath string        `mapstructure:"private_key_path" env:"JWT_PRIVATE_KEY_PATH"`
	PublicKeyPath  string        `mapstructure:"public_key_path" env:"JWT_PUBLIC_KEY_PATH"`
	AccessTokenTTL time.Duration `mapstructure:"access_token_ttl" env:"JWT_ACCESS_TOKEN_TTL"`
	RefreshTokenTTL time.Duration `mapstructure:"refresh_token_ttl" env:"JWT_REFRESH_TOKEN_TTL"`
	Issuer         string        `mapstructure:"issuer" env:"JWT_ISSUER"`
	Audience       string        `mapstructure:"audience" env:"JWT_AUDIENCE"`
	Algorithm      string        `mapstructure:"algorithm" env:"JWT_ALGORITHM"`
}

// CookieConfig holds cookie settings
type CookieConfig struct {
	Name     string        `mapstructure:"name" env:"COOKIE_NAME"`
	Domain   string        `mapstructure:"domain" env:"COOKIE_DOMAIN"`
	Path     string        `mapstructure:"path" env:"COOKIE_PATH"`
	Secure   bool          `mapstructure:"secure" env:"COOKIE_SECURE"`
	HTTPOnly bool          `mapstructure:"http_only" env:"COOKIE_HTTP_ONLY"`
	SameSite string        `mapstructure:"same_site" env:"COOKIE_SAME_SITE"`
	MaxAge   time.Duration `mapstructure:"max_age" env:"COOKIE_MAX_AGE"`
}

// CasbinConfig holds Casbin RBAC settings
type CasbinConfig struct {
	ModelPath    string `mapstructure:"model_path" env:"CASBIN_MODEL_PATH"`
	PolicyPath   string `mapstructure:"policy_path" env:"CASBIN_POLICY_PATH"`
	RedisEnabled bool   `mapstructure:"redis_enabled" env:"CASBIN_REDIS_ENABLED"`
	RedisKey     string `mapstructure:"redis_key" env:"CASBIN_REDIS_KEY"`
}

// WebSocketConfig holds WebSocket hub settings
type WebSocketConfig struct {
	ReadBufferSize  int           `mapstructure:"read_buffer_size" env:"WS_READ_BUFFER_SIZE"`
	WriteBufferSize int           `mapstructure:"write_buffer_size" env:"WS_WRITE_BUFFER_SIZE"`
	PingPeriod      time.Duration `mapstructure:"ping_period" env:"WS_PING_PERIOD"`
	PongWait        time.Duration `mapstructure:"pong_wait" env:"WS_PONG_WAIT"`
	WriteWait       time.Duration `mapstructure:"write_wait" env:"WS_WRITE_WAIT"`
	MaxMessageSize  int64         `mapstructure:"max_message_size" env:"WS_MAX_MESSAGE_SIZE"`
	RateLimit       WSRateLimitConfig `mapstructure:"rate_limit"`
}

// WSRateLimitConfig holds WebSocket rate limit settings
type WSRateLimitConfig struct {
	Enabled            bool `mapstructure:"enabled" env:"WS_RATE_LIMIT_ENABLED"`
	RequestsPerSecond  int  `mapstructure:"requests_per_second" env:"WS_RATE_LIMIT_RPS"`
	Burst              int  `mapstructure:"burst" env:"WS_RATE_LIMIT_BURST"`
}

// CORSConfig holds CORS settings
type CORSConfig struct {
	AllowedOrigins   []string `mapstructure:"allowed_origins" env:"CORS_ALLOWED_ORIGINS"`
	AllowedMethods   []string `mapstructure:"allowed_methods" env:"CORS_ALLOWED_METHODS"`
	AllowedHeaders   []string `mapstructure:"allowed_headers" env:"CORS_ALLOWED_HEADERS"`
	AllowCredentials bool     `mapstructure:"allow_credentials" env:"CORS_ALLOW_CREDENTIALS"`
	MaxAge           int      `mapstructure:"max_age" env:"CORS_MAX_AGE"`
}

// LoggingConfig holds logging settings
type LoggingConfig struct {
	Level  string `mapstructure:"level" env:"LOG_LEVEL"`
	Format string `mapstructure:"format" env:"LOG_FORMAT"`
	Output string `mapstructure:"output" env:"LOG_OUTPUT"`
	File   FileLogConfig `mapstructure:"file"`
}

// FileLogConfig holds file logging settings
type FileLogConfig struct {
	Enabled      bool   `mapstructure:"enabled" env:"LOG_FILE_ENABLED"`
	Path         string `mapstructure:"path" env:"LOG_FILE_PATH"`
	MaxSizeMB    int    `mapstructure:"max_size_mb" env:"LOG_FILE_MAX_SIZE"`
	MaxBackups   int    `mapstructure:"max_backups" env:"LOG_FILE_MAX_BACKUPS"`
	MaxAgeDays   int    `mapstructure:"max_age_days" env:"LOG_FILE_MAX_AGE"`
	Compress     bool   `mapstructure:"compress" env:"LOG_FILE_COMPRESS"`
}

// EmailConfig holds email settings
type EmailConfig struct {
	Host         string `mapstructure:"host" env:"EMAIL_HOST"`
	Port         int    `mapstructure:"port" env:"EMAIL_PORT"`
	Username     string `mapstructure:"username" env:"EMAIL_USERNAME"`
	Password     string `mapstructure:"password" env:"EMAIL_PASSWORD"`
	FromAddress  string `mapstructure:"from_address" env:"EMAIL_FROM_ADDRESS"`
	FromName     string `mapstructure:"from_name" env:"EMAIL_FROM_NAME"`
	UseTLS       bool   `mapstructure:"use_tls" env:"EMAIL_USE_TLS"`
}

// StorageConfig holds storage settings
type StorageConfig struct {
	Provider   string `mapstructure:"provider" env:"STORAGE_PROVIDER"`
	Endpoint   string `mapstructure:"endpoint" env:"STORAGE_ENDPOINT"`
	AccessKey  string `mapstructure:"access_key" env:"STORAGE_ACCESS_KEY"`
	SecretKey  string `mapstructure:"secret_key" env:"STORAGE_SECRET_KEY"`
	Bucket     string `mapstructure:"bucket" env:"STORAGE_BUCKET"`
	Region     string `mapstructure:"region" env:"STORAGE_REGION"`
	UseSSL     bool   `mapstructure:"use_ssl" env:"STORAGE_USE_SSL"`
	PublicURL  string `mapstructure:"public_url" env:"STORAGE_PUBLIC_URL"`
}

// RateLimitConfig holds API rate limiting settings
type RateLimitConfig struct {
	Enabled        bool              `mapstructure:"enabled" env:"RATE_LIMIT_ENABLED"`
	RequestsPerMin int               `mapstructure:"requests_per_minute" env:"RATE_LIMIT_RPM"`
	Burst          int               `mapstructure:"burst" env:"RATE_LIMIT_BURST"`
	Endpoints      map[string]string `mapstructure:"endpoints"`
}

// MetricsConfig holds metrics settings
type MetricsConfig struct {
	Enabled bool   `mapstructure:"enabled" env:"METRICS_ENABLED"`
	Path    string `mapstructure:"path" env:"METRICS_PATH"`
	Port    int    `mapstructure:"port" env:"METRICS_PORT"`
}

// TracingConfig holds OpenTelemetry tracing settings
type TracingConfig struct {
	Enabled      bool    `mapstructure:"enabled" env:"TRACING_ENABLED"`
	Endpoint     string  `mapstructure:"endpoint" env:"TRACING_ENDPOINT"`
	ServiceName  string  `mapstructure:"service_name" env:"TRACING_SERVICE_NAME"`
	SampleRate   float64 `mapstructure:"sample_rate" env:"TRACING_SAMPLE_RATE"`
}

// Load loads configuration from file and environment variables
func Load(configPath string) (*Config, error) {
	// Load .env file if exists
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		return nil, fmt.Errorf("failed to load .env: %w", err)
	}

	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Set defaults
	setDefaults(v)

	if err := v.ReadInConfig(); err != nil {
		return nil, fmt.Errorf("failed to read config: %w", err)
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// Also parse env tags for nested structs
	if err := env.Parse(&cfg); err != nil {
		return nil, fmt.Errorf("failed to parse env: %w", err)
	}

	return &cfg, nil
}

func setDefaults(v *viper.Viper) {
	// App defaults
	v.SetDefault("app.name", "pie360-backend")
	v.SetDefault("app.version", "1.0.0")
	v.SetDefault("app.env", "development")
	v.SetDefault("app.host", "0.0.0.0")
	v.SetDefault("app.port", 8080)
	v.SetDefault("app.read_timeout", "30s")
	v.SetDefault("app.write_timeout", "30s")
	v.SetDefault("app.idle_timeout", "120s")
	v.SetDefault("app.shutdown_grace_period", "15s")

	// Database defaults
	v.SetDefault("database.host", "localhost")
	v.SetDefault("database.port", 5432)
	v.SetDefault("database.ssl_mode", "disable")
	v.SetDefault("database.max_conns", 25)
	v.SetDefault("database.min_conns", 5)
	v.SetDefault("database.max_conn_lifetime", "1h")
	v.SetDefault("database.max_conn_idle_time", "30m")
	v.SetDefault("database.health_check_period", "1m")

	// Redis defaults
	v.SetDefault("redis.host", "localhost")
	v.SetDefault("redis.port", 6379)
	v.SetDefault("redis.db", 0)
	v.SetDefault("redis.pool_size", 50)
	v.SetDefault("redis.min_idle_conns", 10)
	v.SetDefault("redis.dial_timeout", "5s")
	v.SetDefault("redis.read_timeout", "3s")
	v.SetDefault("redis.write_timeout", "3s")

	// JWT defaults
	v.SetDefault("jwt.access_token_ttl", "15m")
	v.SetDefault("jwt.refresh_token_ttl", "168h")
	v.SetDefault("jwt.issuer", "pie360")
	v.SetDefault("jwt.audience", "pie360-frontend")
	v.SetDefault("jwt.algorithm", "RS256")

	// Cookie defaults
	v.SetDefault("cookie.name", "pie360_session")
	v.SetDefault("cookie.path", "/")
	v.SetDefault("cookie.http_only", true)
	v.SetDefault("cookie.same_site", "lax")
	v.SetDefault("cookie.max_age", "168h")

	// Casbin defaults
	v.SetDefault("casbin.model_path", "./config/rbac_model.conf")
	v.SetDefault("casbin.policy_path", "./config/rbac_policy.csv")
	v.SetDefault("casbin.redis_enabled", true)
	v.SetDefault("casbin.redis_key", "casbin:policies")

	// WebSocket defaults
	v.SetDefault("websocket.read_buffer_size", 4096)
	v.SetDefault("websocket.write_buffer_size", 4096)
	v.SetDefault("websocket.ping_period", "30s")
	v.SetDefault("websocket.pong_wait", "60s")
	v.SetDefault("websocket.write_wait", "10s")
	v.SetDefault("websocket.max_message_size", 512)
	v.SetDefault("websocket.rate_limit.enabled", true)
	v.SetDefault("websocket.rate_limit.requests_per_second", 50)
	v.SetDefault("websocket.rate_limit.burst", 100)

	// CORS defaults
	v.SetDefault("cors.allowed_origins", []string{"http://localhost:5173"})
	v.SetDefault("cors.allowed_methods", []string{"GET", "POST", "PUT", "PATCH", "DELETE", "OPTIONS"})
	v.SetDefault("cors.allowed_headers", []string{"Origin", "Content-Type", "Accept", "Authorization", "X-Requested-With", "X-Institute-ID"})
	v.SetDefault("cors.allow_credentials", true)
	v.SetDefault("cors.max_age", 86400)

	// Logging defaults
	v.SetDefault("logging.level", "debug")
	v.SetDefault("logging.format", "console")
	v.SetDefault("logging.output", "stdout")

	// Email defaults
	v.SetDefault("email.port", 2525)
	v.SetDefault("email.use_tls", true)

	// Storage defaults
	v.SetDefault("storage.provider", "minio")
	v.SetDefault("storage.region", "us-east-1")
	v.SetDefault("storage.use_ssl", false)

	// Rate limit defaults
	v.SetDefault("rate_limit.enabled", true)
	v.SetDefault("rate_limit.requests_per_minute", 100)
	v.SetDefault("rate_limit.burst", 20)

	// Metrics defaults
	v.SetDefault("metrics.enabled", true)
	v.SetDefault("metrics.path", "/metrics")
	v.SetDefault("metrics.port", 9090)

	// Tracing defaults
	v.SetDefault("tracing.enabled", false)
	v.SetDefault("tracing.sample_rate", 0.1)
}

// MustLoad loads config or panics
func MustLoad(configPath string) *Config {
	cfg, err := Load(configPath)
	if err != nil {
		panic(fmt.Sprintf("failed to load config: %v", err))
	}
	return cfg
}

// GetConfigPath returns the config file path based on environment
func GetConfigPath() string {
	env := os.Getenv("APP_ENV")
	if env == "" {
		env = "development"
	}

	// Check for explicit config path
	if path := os.Getenv("CONFIG_PATH"); path != "" {
		return path
	}

	// Look for config in standard locations
	paths := []string{
		"./config.yaml",
		"./config/config.yaml",
		"../config.yaml",
		"../config/config.yaml",
		fmt.Sprintf("./config.%s.yaml", env),
		fmt.Sprintf("./config/config.%s.yaml", env),
	}

	for _, p := range paths {
		if _, err := os.Stat(p); err == nil {
			abs, _ := filepath.Abs(p)
			return abs
		}
	}

	return "./config.yaml"
}