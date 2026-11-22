// Package appearance implements the institute's look and each user's light/dark choice (FND-11, decision 0005).
//
// The institute picks an accent colour and a design preset (classic, modern, contrast); each user picks only light,
// dark or system. Clients build the theme with @shikshit/shared createTheme(mode, accent, preset), which keeps any
// accent readable, so a free colour is safe.
//
// Rules enforced here (each has a test):
//   - the accent is #RRGGBB and the preset one of Presets, checked here and again in the database function;
//   - an institute session can change only its own institute, and only brand.primary and brand.preset;
//   - a change bumps every user's access version, so open apps refetch and repaint.
package appearance

import (
	"context"
	"errors"
	"regexp"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/tenancy"
)

// Presets are the design presets, in the order clients show them.
var Presets = []string{"classic", "modern", "contrast"}

// Modes are the theme modes a user may choose.
var Modes = []string{"light", "dark", "system"}

// Defaults used when the institute or user has not chosen.
const (
	DefaultAccent = "#1F3A5F"
	DefaultPreset = "classic"
	DefaultMode   = "system"
)

var hexColour = regexp.MustCompile(`^#[0-9A-Fa-f]{6}$`)

// Appearance is the institute's look.
type Appearance struct {
	Accent string `json:"accent"`
	Preset string `json:"preset"`
}

// Preferences are one user's personal settings.
type Preferences struct {
	ThemeMode string `json:"themeMode"`
}

// FromBrand reads the look out of tenants.brand, filling the defaults.
func FromBrand(brand map[string]any) Appearance {
	a := Appearance{Accent: DefaultAccent, Preset: DefaultPreset}
	if s, ok := brand["primary"].(string); ok && hexColour.MatchString(s) {
		a.Accent = strings.ToUpper(s)
	}
	if s, ok := brand["preset"].(string); ok && contains(Presets, s) {
		a.Preset = s
	}
	return a
}

// Normalise trims and checks an appearance.
func Normalise(in Appearance) (Appearance, error) {
	var errs crud.FieldErrors
	in.Accent, in.Preset = strings.ToUpper(strings.TrimSpace(in.Accent)), strings.ToLower(strings.TrimSpace(in.Preset))
	if !hexColour.MatchString(in.Accent) {
		errs.Add("accent", "invalid", "Choose a colour, written as #RRGGBB.")
	}
	errs.Choice("preset", in.Preset, Presets...)
	return in, errs.OrNil()
}

// Service reads and writes appearance settings.
type Service struct{ DB tenancy.Beginner }

// Get returns the institute's look.
func (s Service) Get(ctx context.Context) (Appearance, error) {
	var out Appearance
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var brand map[string]any
		if err := tx.QueryRow(ctx, `SELECT brand FROM tenants WHERE id = app_current_tenant() AND deleted_at IS NULL`).Scan(&brand); err != nil {
			return err
		}
		out = FromBrand(brand)
		return nil
	})
	return out, err
}

// Set changes the institute's look and bumps every user's access version so open apps repaint.
func (s Service) Set(ctx context.Context, a audit.Actor, in Appearance) (Appearance, error) {
	in, err := Normalise(in)
	if err != nil {
		return Appearance{}, err
	}
	var out Appearance
	err = tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var brand map[string]any
		if err := tx.QueryRow(ctx, `SELECT brand FROM tenants WHERE id = app_current_tenant() AND deleted_at IS NULL`).Scan(&brand); err != nil {
			return err
		}
		before := FromBrand(brand)
		if err := tx.QueryRow(ctx, `SELECT set_tenant_appearance($1::text, $2::text)`, in.Accent, in.Preset).Scan(&brand); err != nil {
			return err
		}
		out = FromBrand(brand)
		if before == out {
			return nil
		}
		if _, err := tx.Exec(ctx, `UPDATE users SET access_version = access_version + 1 WHERE deleted_at IS NULL`); err != nil {
			return err
		}
		return audit.Record(ctx, tx, a, audit.Entry{Action: "appearance.update", ResourceType: "institute", Before: before, After: out})
	})
	return out, err
}

// GetPreferences returns a user's preferences, with defaults for anything not chosen.
func (s Service) GetPreferences(ctx context.Context, userID string) (Preferences, error) {
	out := Preferences{ThemeMode: DefaultMode}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		var mode *string
		err := tx.QueryRow(ctx, `SELECT preferences->>'themeMode' FROM users WHERE id = $1::uuid AND deleted_at IS NULL`, userID).Scan(&mode)
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		if err != nil {
			return err
		}
		if mode != nil && contains(Modes, *mode) {
			out.ThemeMode = *mode
		}
		return nil
	})
	return out, err
}

// SetPreferences stores a user's preferences. Only the user themselves calls this (GET/PUT /me/preferences).
func (s Service) SetPreferences(ctx context.Context, userID string, in Preferences) (Preferences, error) {
	var errs crud.FieldErrors
	in.ThemeMode = strings.ToLower(strings.TrimSpace(in.ThemeMode))
	errs.Choice("themeMode", in.ThemeMode, Modes...)
	if err := errs.OrNil(); err != nil {
		return Preferences{}, err
	}
	err := tenancy.WithTenantTx(ctx, s.DB, func(ctx context.Context, tx pgx.Tx) error {
		_, err := tx.Exec(ctx, `UPDATE users SET preferences = preferences || jsonb_build_object('themeMode', $2::text)
			WHERE id = $1::uuid AND deleted_at IS NULL`, userID, in.ThemeMode)
		return err
	})
	return in, err
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
