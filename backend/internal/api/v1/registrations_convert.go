package apiv1

import (
	"context"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/registrations"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/authz"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

// EnquiryRegistrar turns an enquiry into a registration (registrations.Service satisfies it). WithStudentRegistrations
// accepts a service that also has this method; a service without it leaves "register an enquiry" unconfigured.
type EnquiryRegistrar interface {
	CreateFromEnquiry(ctx context.Context, actor audit.Actor, enquiryID string, fill registrations.Fill) (registrations.Registration, error)
}

// RegisterEnquiry implements POST /enquiries/{enquiryId}/register. The route's own permission is the right to create
// registrations; the caller must also be able to see enquiries, tenant-wide, as the enquiries screen requires.
func (h *Handler) RegisterEnquiry(ctx context.Context, req gen.RegisterEnquiryRequestObject) (gen.RegisterEnquiryResponseObject, error) {
	if h.studentRegistrations == nil {
		return nil, errNotConfigured
	}
	reg, ok := h.studentRegistrations.(EnquiryRegistrar)
	if !ok {
		return nil, errNotConfigured
	}
	if a, ok := AccessFrom(ctx); !ok || !a.Effective.Can("adm.enquiry.view") || !contains(a.Effective.Scopes["adm.enquiry"], authz.ScopeTenant) {
		return gen.RegisterEnquiry403ApplicationProblemPlusJSONResponse{ForbiddenApplicationProblemPlusJSONResponse: gen.ForbiddenApplicationProblemPlusJSONResponse(
			problemBody(http.StatusForbidden, "permission_denied", "You do not have permission to do this."))}, nil
	}
	var fill registrations.Fill
	if b := req.Body; b != nil {
		fill.DateOfBirth, fill.Gender = domainDatePtr(b.DateOfBirth), b.Gender
		fill.ClassID, fill.YearID = uuidStrPtr(b.ClassId), uuidStrPtr(b.YearId)
	}
	out, err := reg.CreateFromEnquiry(ctx, actorFrom(ctx), req.EnquiryId.String(), fill)
	var fe crud.FieldErrors
	switch {
	case errors.As(err, &fe):
		return gen.RegisterEnquiry400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	case errors.Is(err, resource.ErrNotFound):
		return gen.RegisterEnquiry404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: gen.NotFoundApplicationProblemPlusJSONResponse(problemBody(http.StatusNotFound, "not_found", ""))}, nil
	case errors.Is(err, registrations.ErrAlreadyConverted):
		return gen.RegisterEnquiry409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(
			problemBody(http.StatusConflict, "already_converted", "This enquiry already has a registration."))}, nil
	case errors.Is(err, registrations.ErrEnquiryClosed):
		return gen.RegisterEnquiry409ApplicationProblemPlusJSONResponse{ConflictApplicationProblemPlusJSONResponse: gen.ConflictApplicationProblemPlusJSONResponse(
			problemBody(http.StatusConflict, "enquiry_closed", "This enquiry is closed. Reopen it before registering."))}, nil
	case err != nil:
		return nil, err
	}
	return gen.RegisterEnquiry201JSONResponse(toGenStudentRegistration(out)), nil
}
