package apiv1

import (
	"context"
	"errors"
	"time"

	openapi_types "github.com/oapi-codegen/runtime/types"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/adm/insights"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// AdmissionInsightsService is the subset of insights.Service used by the handler.
type AdmissionInsightsService interface {
	Get(ctx context.Context, f insights.Filter) (insights.Insights, error)
}

// WithAdmissionInsights enables GET /admission-insights. Without it the operation answers 501.
func WithAdmissionInsights(s AdmissionInsightsService) Option {
	return func(h *Handler) { h.admissionInsights = s }
}

// GetAdmissionInsights implements GET /admission-insights. The permission (adm.admission_insights.view, tenant-wide
// scope only: see scopedResources) is checked before this runs. The read is not audited: it returns counts only.
func (h *Handler) GetAdmissionInsights(ctx context.Context, req gen.GetAdmissionInsightsRequestObject) (gen.GetAdmissionInsightsResponseObject, error) {
	if h.admissionInsights == nil {
		return nil, errNotConfigured
	}
	f := insights.Filter{}
	if p := uuidStrPtr(req.Params.YearId); p != nil {
		f.YearID = *p
	}
	if p := uuidStrPtr(req.Params.ClassId); p != nil {
		f.ClassID = *p
	}
	if p := domainDatePtr(req.Params.From); p != nil {
		f.From = *p
	}
	if p := domainDatePtr(req.Params.To); p != nil {
		f.To = *p
	}
	res, err := h.admissionInsights.Get(ctx, f)
	var fe crud.FieldErrors
	if errors.As(err, &fe) {
		return gen.GetAdmissionInsights400ApplicationProblemPlusJSONResponse{BadRequestApplicationProblemPlusJSONResponse: gen.BadRequestApplicationProblemPlusJSONResponse(validationProblem(fe))}, nil
	}
	if err != nil {
		return nil, err
	}
	return gen.GetAdmissionInsights200JSONResponse(toGenInsights(res)), nil
}

func insightsDate(s string) openapi_types.Date {
	t, _ := time.Parse(crud.DateLayout, s)
	return openapi_types.Date{Time: t}
}

func insightsRatio(r insights.Ratio) gen.InsightsRatio {
	return gen.InsightsRatio{Of: r.Of, Converted: r.Converted, Rate: r.Rate}
}

func toGenInsights(r insights.Insights) gen.AdmissionInsights {
	out := gen.AdmissionInsights{AsOf: insightsDate(r.AsOf), TrendFrom: insightsDate(r.From), TrendTo: insightsDate(r.To), Trend: make([]gen.InsightsMonth, len(r.Trend))}
	e, s, o := &out.Enquiries, r.EnquiriesByStatus, r.EnquiriesBySource
	e.Total = r.EnquiriesTotal
	e.ByStatus.New, e.ByStatus.Contacted, e.ByStatus.VisitScheduled, e.ByStatus.Converted, e.ByStatus.Closed = s["new"], s["contacted"], s["visit_scheduled"], s["converted"], s["closed"]
	e.BySource.WalkIn, e.BySource.Phone, e.BySource.Website, e.BySource.Referral, e.BySource.Social, e.BySource.Other = o["walk_in"], o["phone"], o["website"], o["referral"], o["social"], o["other"]
	out.FollowUps = gen.InsightsFollowUps{Due: r.FollowUps.Due, DueToday: r.FollowUps.DueToday, Overdue: r.FollowUps.Overdue}
	g, rs := &out.Registrations, r.RegistrationsBy
	g.Total = r.RegistrationsTotal
	g.ByStatus.Submitted, g.ByStatus.UnderReview, g.ByStatus.Accepted, g.ByStatus.Rejected, g.ByStatus.Withdrawn = rs["submitted"], rs["under_review"], rs["accepted"], rs["rejected"], rs["withdrawn"]
	out.Conversion = gen.InsightsConversion{
		EnquiryToRegistration:  insightsRatio(r.Conversion.EnquiryToRegistration),
		RegistrationToAccepted: insightsRatio(r.Conversion.RegistrationToAccepted),
	}
	if a := r.Conversion.AcceptedToAdmitted; a != nil { // ADM-05 hook
		v := insightsRatio(*a)
		out.Conversion.AcceptedToAdmitted = &v
	}
	for i, m := range r.Trend {
		out.Trend[i] = gen.InsightsMonth{Month: m.Month, Enquiries: m.Enquiries, Registrations: m.Registrations}
	}
	return out
}
