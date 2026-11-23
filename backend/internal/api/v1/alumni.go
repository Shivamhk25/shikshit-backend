package apiv1

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/alumni"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/audit"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/resource"
)

// AlumniService is what the alumni operations need (alumni.Service satisfies it).
type AlumniService interface {
	ListAudited(ctx context.Context, a audit.Actor, q crud.ListQuery, f alumni.Filter) (crud.Page[alumni.Alumnus], error)
	View(ctx context.Context, a audit.Actor, id string) (alumni.Alumnus, error)
}

// WithAlumni enables the /alumni operations.
func WithAlumni(s AlumniService) Option {
	return func(h *Handler) { h.alumni = s }
}

func init() {
	for op, perm := range map[string]string{"ListAlumni": "stu.alumni.view", "GetAlumnus": "stu.alumni.view"} {
		operationAccess[op] = struct{ Permission, Module string }{perm, "stu"}
		// former students' records are not for platform staff on a read-only support visit
		documentOperations[op] = true
	}
}

// alumniFail is the error answer shared by the alumni operations.
type alumniFail gen.Problem

func (f alumniFail) write(w http.ResponseWriter) error {
	w.Header().Set("Content-Type", "application/problem+json")
	w.WriteHeader(f.Status)
	return json.NewEncoder(w).Encode(f)
}
func (f alumniFail) VisitListAlumniResponse(w http.ResponseWriter) error { return f.write(w) }
func (f alumniFail) VisitGetAlumnusResponse(w http.ResponseWriter) error { return f.write(w) }

func toGenAlumnus(x alumni.Alumnus) gen.Alumnus {
	return gen.Alumnus{
		Id: mustUUID(x.ID), Name: x.Name, RegistrationCode: x.RegistrationCode, AdmissionDate: apiDatePtr(x.AdmissionDate),
		LastClassName: x.LastClassName, LastSectionName: x.LastSectionName, YearName: x.YearName,
		Reason: gen.AlumniReason(x.Reason), ReasonLabel: alumni.ReasonLabel(x.Reason), ExitDate: apiDatePtr(x.ExitDate),
		TcNumber: x.TCNumber, DestinationSchool: x.DestinationSchool, LastAttendanceDate: apiDatePtr(x.LastAttendanceDate),
	}
}

// ListAlumni implements GET /alumni.
func (h *Handler) ListAlumni(ctx context.Context, req gen.ListAlumniRequestObject) (gen.ListAlumniResponseObject, error) {
	if h.alumni == nil {
		return nil, errNotConfigured
	}
	q, qerr := listQuery(req.Params.Page, req.Params.PageSize, req.Params.Sort, req.Params.Q, alumni.SortKeys, alumni.DefaultSort)
	if qerr != nil {
		return alumniFail(validationProblem(qerr)), nil
	}
	var f alumni.Filter
	if req.Params.Reason != nil {
		f.Reason = string(*req.Params.Reason)
		var fe crud.FieldErrors
		oneOf(&fe, "reason", f.Reason, alumni.ReasonGraduated, alumni.ReasonTransferred, alumni.ReasonOther)
		if len(fe) > 0 {
			return alumniFail(validationProblem(fe)), nil
		}
	}
	f.ClassID, f.YearID = derefString(uuidStrPtr(req.Params.ClassId)), derefString(uuidStrPtr(req.Params.YearId))
	p, err := h.alumni.ListAudited(ctx, actorFrom(ctx), q, f)
	if err != nil {
		return nil, err
	}
	out := gen.AlumniPage{Items: make([]gen.Alumnus, len(p.Items)), Page: gen.PageInfo{Number: p.Page, Size: p.Size, Total: p.Total}}
	for i, it := range p.Items {
		out.Items[i] = toGenAlumnus(it)
	}
	return gen.ListAlumni200JSONResponse(out), nil
}

// GetAlumnus implements GET /alumni/{studentId}.
func (h *Handler) GetAlumnus(ctx context.Context, req gen.GetAlumnusRequestObject) (gen.GetAlumnusResponseObject, error) {
	if h.alumni == nil {
		return nil, errNotConfigured
	}
	x, err := h.alumni.View(ctx, actorFrom(ctx), req.StudentId.String())
	if errors.Is(err, resource.ErrNotFound) {
		return alumniFail(problemBody(http.StatusNotFound, "not_found", "")), nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetAlumnus200JSONResponse(toGenAlumnus(x)), nil
}
