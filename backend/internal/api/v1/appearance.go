package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/iam/appearance"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// AppearanceService is the subset of appearance.Service used by the handler.
type AppearanceService interface {
	Get(ctx context.Context) (appearance.Appearance, error)
	Set(ctx context.Context, a audit.Actor, in appearance.Appearance) (appearance.Appearance, error)
	GetPreferences(ctx context.Context, userID string) (appearance.Preferences, error)
	SetPreferences(ctx context.Context, userID string, in appearance.Preferences) (appearance.Preferences, error)
}

// WithAppearance enables the appearance and preference operations. Without it they answer 501.
func WithAppearance(s AppearanceService) Option { return func(h *Handler) { h.appearance = s } }

func toGenAppearance(a appearance.Appearance) gen.Appearance {
	return gen.Appearance{Accent: a.Accent, Preset: gen.AppearancePreset(a.Preset)}
}

// GetInstituteAppearance implements GET /institute/appearance.
func (h *Handler) GetInstituteAppearance(ctx context.Context, _ gen.GetInstituteAppearanceRequestObject) (gen.GetInstituteAppearanceResponseObject, error) {
	if h.appearance == nil {
		return nil, errNotConfigured
	}
	a, err := h.appearance.Get(ctx)
	if err != nil {
		return nil, err
	}
	return gen.GetInstituteAppearance200JSONResponse(toGenAppearance(a)), nil
}

// SetInstituteAppearance implements PUT /institute/appearance.
func (h *Handler) SetInstituteAppearance(ctx context.Context, req gen.SetInstituteAppearanceRequestObject) (gen.SetInstituteAppearanceResponseObject, error) {
	if h.appearance == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.SetInstituteAppearance400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	a, err := h.appearance.Set(ctx, actorFrom(ctx), appearance.Appearance{Accent: req.Body.Accent, Preset: string(req.Body.Preset)})
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.SetInstituteAppearance400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case err != nil:
		return nil, err
	}
	return gen.SetInstituteAppearance200JSONResponse(toGenAppearance(a)), nil
}

// GetMyPreferences implements GET /me/preferences.
func (h *Handler) GetMyPreferences(ctx context.Context, _ gen.GetMyPreferencesRequestObject) (gen.GetMyPreferencesResponseObject, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || h.appearance == nil {
		return nil, errNotConfigured
	}
	pref, err := h.appearance.GetPreferences(ctx, p.UserID)
	if err != nil {
		return nil, err
	}
	return gen.GetMyPreferences200JSONResponse{ThemeMode: gen.PreferencesThemeMode(pref.ThemeMode)}, nil
}

// SetMyPreferences implements PUT /me/preferences.
func (h *Handler) SetMyPreferences(ctx context.Context, req gen.SetMyPreferencesRequestObject) (gen.SetMyPreferencesResponseObject, error) {
	p, ok := PrincipalFrom(ctx)
	if !ok || h.appearance == nil {
		return nil, errNotConfigured
	}
	if req.Body == nil {
		return gen.SetMyPreferences400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(problemBody(http.StatusBadRequest, "validation_failed", "A request body is required."))}, nil
	}
	pref, err := h.appearance.SetPreferences(ctx, p.UserID, appearance.Preferences{ThemeMode: string(req.Body.ThemeMode)})
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.SetMyPreferences400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case err != nil:
		return nil, err
	}
	return gen.SetMyPreferences200JSONResponse{ThemeMode: gen.PreferencesThemeMode(pref.ThemeMode)}, nil
}
