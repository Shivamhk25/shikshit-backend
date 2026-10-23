package logger

import (
	"io"
	"os"
	"time"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/pkgerrors"
)

// Logger wraps zerolog.Logger
type Logger struct {
	*zerolog.Logger
}

// Config holds logger configuration
type Config struct {
	Level      string
	Format     string // "console" or "json"
	Output     string // "stdout", "stderr", or file path
	TimeFormat string
	NoColor    bool
	Caller     bool
}

// New creates a new logger from config
func New(cfg Config) *Logger {
	zerolog.ErrorStackMarshaler = pkgerrors.MarshalStack
	zerolog.TimeFieldFormat = cfg.TimeFormat
	if cfg.TimeFormat == "" {
		zerolog.TimeFieldFormat = time.RFC3339Nano
	}

	var output io.Writer
	switch cfg.Output {
	case "stdout":
		output = os.Stdout
	case "stderr":
		output = os.Stderr
	default:
		// File output would be handled by lumberjack in production
		output = os.Stdout
	}

	var logger zerolog.Logger
	if cfg.Format == "console" {
		logger = zerolog.New(zerolog.ConsoleWriter{
			Out:        output,
			TimeFormat: cfg.TimeFormat,
			NoColor:    cfg.NoColor,
		})
	} else {
		logger = zerolog.New(output)
	}

	level, err := zerolog.ParseLevel(cfg.Level)
	if err != nil {
		level = zerolog.InfoLevel
	}
	logger = logger.Level(level).With().Timestamp().Logger()

	if cfg.Caller {
		logger = logger.With().Caller().Logger()
	}

	return &Logger{Logger: &logger}
}

// NewDefault creates a default development logger
func NewDefault() *Logger {
	return New(Config{
		Level:      "debug",
		Format:     "console",
		Output:     "stdout",
		TimeFormat: time.RFC3339,
		Caller:     true,
	})
}

// NewProduction creates a production JSON logger
func NewProduction() *Logger {
	return New(Config{
		Level:      "info",
		Format:     "json",
		Output:     "stdout",
		TimeFormat: time.RFC3339Nano,
		Caller:     false,
	})
}

// WithContext adds context fields to the logger
func (l *Logger) WithContext(key string, value interface{}) *Logger {
	cloned := l.Logger.With().Interface(key, value).Logger()
	return &Logger{Logger: &cloned}
}

// WithFields adds multiple fields to the logger
func (l *Logger) WithFields(fields map[string]interface{}) *Logger {
	ctx := l.Logger.With()
	for k, v := range fields {
		ctx = ctx.Interface(k, v)
	}
	cloned := ctx.Logger()
	return &Logger{Logger: &cloned}
}

// WithError adds an error to the logger context
func (l *Logger) WithError(err error) *Logger {
	if err == nil {
		return l
	}
	cloned := l.Logger.With().Err(err).Logger()
	return &Logger{Logger: &cloned}
}

// WithComponent adds a component name to the logger
func (l *Logger) WithComponent(component string) *Logger {
	cloned := l.Logger.With().Str("component", component).Logger()
	return &Logger{Logger: &cloned}
}

// WithRequestID adds a request ID to the logger
func (l *Logger) WithRequestID(requestID string) *Logger {
	cloned := l.Logger.With().Str("request_id", requestID).Logger()
	return &Logger{Logger: &cloned}
}

// WithTenant adds tenant/institute ID to the logger
func (l *Logger) WithTenant(tenantID string) *Logger {
	cloned := l.Logger.With().Str("institute_id", tenantID).Logger()
	return &Logger{Logger: &cloned}
}

// WithUser adds user ID to the logger
func (l *Logger) WithUser(userID string) *Logger {
	cloned := l.Logger.With().Str("user_id", userID).Logger()
	return &Logger{Logger: &cloned}
}