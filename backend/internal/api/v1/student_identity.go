package apiv1

import (
	"context"
	"errors"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/identity"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

// contactsPermission unmasks guardian phone numbers and emails on the identity card.
const contactsPermission = "stu.student_contacts.view"

// StudentIdentityService is what the student identity operations need (identity.Service satisfies it).
type StudentIdentityService interface {
	Search(ctx context.Context, a audit.Actor, q string, limit int) ([]identity.Match, error)
	Get(ctx context.Context, a audit.Actor, id string, contacts bool) (identity.Card, error)
}

// WithStudentIdentity enables the /student-identity operations (STU-01).
func WithStudentIdentity(s StudentIdentityService) Option {
	return func(h *Handler) { h.studentIdentity = s }
}

func init() {
	for _, op := range []string{"SearchStudentIdentity", "GetStudentIdentity"} {
		operationAccess[op] = struct{ Permission, Module string }{"stu.student_identity.view", "stu"}
	}
}

// SearchStudentIdentity implements GET /student-identity.
func (h *Handler) SearchStudentIdentity(ctx context.Context, req gen.SearchStudentIdentityRequestObject) (gen.SearchStudentIdentityResponseObject, error) {
	if h.studentIdentity == nil {
		return nil, errNotConfigured
	}
	limit := 0
	if req.Params.Limit != nil {
		limit = *req.Params.Limit
	}
	found, err := h.studentIdentity.Search(ctx, actorFrom(ctx), req.Params.Q, limit)
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return gen.SearchStudentIdentity400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if err != nil {
		return nil, err
	}
	items := make([]gen.StudentIdentityMatch, len(found))
	for i, m := range found {
		items[i] = gen.StudentIdentityMatch{Id: mustUUID(m.ID), RegistrationCode: m.RegistrationCode, FullName: m.FullName,
			Status: gen.StudentIdentityMatchStatus(m.Status), ClassName: m.ClassName, SectionName: m.SectionName, HasPhoto: m.HasPhoto}
	}
	return gen.SearchStudentIdentity200JSONResponse{Items: items}, nil
}

// GetStudentIdentity implements GET /student-identity/{studentId}. Contact details are returned only to a caller who
// also holds stu.student_contacts.view.
func (h *Handler) GetStudentIdentity(ctx context.Context, req gen.GetStudentIdentityRequestObject) (gen.GetStudentIdentityResponseObject, error) {
	if h.studentIdentity == nil {
		return nil, errNotConfigured
	}
	contacts := false
	if a, ok := AccessFrom(ctx); ok {
		contacts = a.Effective.Can(contactsPermission)
	}
	c, err := h.studentIdentity.Get(ctx, actorFrom(ctx), req.StudentId.String(), contacts)
	if errors.Is(err, resource.ErrNotFound) {
		return gen.GetStudentIdentity404ApplicationProblemPlusJSONResponse{NotFoundApplicationProblemPlusJSONResponse: photoNotFound()}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetStudentIdentity200JSONResponse(toGenIdentityCard(c)), nil
}

func identityDate(s string) openapi_types.Date {
	t, _ := time.Parse(crud.DateLayout, s)
	return openapi_types.Date{Time: t}
}

func identityDatePtr(s *string) *openapi_types.Date {
	if s == nil {
		return nil
	}
	d := identityDate(*s)
	return &d
}

func toGenIdentityCard(c identity.Card) gen.StudentIdentityCard {
	out := gen.StudentIdentityCard{Id: mustUUID(c.ID), RegistrationCode: c.RegistrationCode, FullName: c.FullName,
		Status: gen.StudentIdentityCardStatus(c.Status), Gender: gen.StudentIdentityCardGender(c.Gender), DateOfBirth: identityDate(c.DateOfBirth),
		HasPhoto: c.HasPhoto, ContactsVisible: c.ContactsVisible, Guardians: make([]gen.StudentIdentityGuardian, len(c.Guardians))}
	if e := c.Enrollment; e != nil {
		out.Enrollment = &gen.StudentIdentityEnrollment{AcademicYear: e.AcademicYear, ClassName: e.ClassName, SectionName: e.SectionName, RollNumber: e.RollNumber}
	}
	for i, g := range c.Guardians {
		out.Guardians[i] = gen.StudentIdentityGuardian{Id: mustUUID(g.ID), Name: g.Name, Relation: g.Relation, IsPrimary: g.IsPrimary,
			IsLegalCustody: g.IsLegalCustody, CanPickup: g.CanPickup, Phone: g.Phone, Email: g.Email}
	}
	i := c.IDCard
	out.IdCard = gen.StudentIdCardPreview{InstituteName: i.InstituteName, StudentName: i.StudentName, RegistrationCode: i.RegistrationCode,
		ClassSection: i.ClassSection, RollNumber: i.RollNumber, DateOfBirth: identityDate(i.DateOfBirth), AcademicYear: i.AcademicYear,
		ValidUntil: identityDatePtr(i.ValidUntil), PrimaryGuardianName: i.PrimaryGuardianName, EmergencyPhone: i.EmergencyPhone}
	return out
}
