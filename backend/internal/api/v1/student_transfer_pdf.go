package apiv1

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/api/gen"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/modules/stu/transfers"
	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/pdf"
)

func (f transferFail) VisitGetStudentTransferCertificatePdfResponse(w http.ResponseWriter) error {
	return f.write(w)
}

// GetStudentTransferCertificatePdf implements GET /student-transfers/{transferId}/certificate.pdf. It reads through the
// same audited service call as the JSON certificate, so permission, tenant scope, the completed-only rule and the
// audit entry are the same. The file name is the TC number (never the student's name) and nothing is logged.
func (h *Handler) GetStudentTransferCertificatePdf(ctx context.Context, req gen.GetStudentTransferCertificatePdfRequestObject) (gen.GetStudentTransferCertificatePdfResponseObject, error) {
	if err := h.transfersReady(); err != nil {
		return nil, err
	}
	c, err := h.studentTransfers.Certificate(ctx, actorFrom(ctx), req.TransferId.String())
	if f, ok := failTransfer(err); ok {
		return f, nil
	}
	if err != nil {
		return nil, err
	}
	data, err := pdf.Render(transferCertificateDoc(c))
	if err != nil {
		return nil, err
	}
	return gen.GetStudentTransferCertificatePdf200ApplicationpdfResponse{
		Body: bytes.NewReader(data), ContentLength: int64(len(data)),
		Headers: gen.GetStudentTransferCertificatePdf200ResponseHeaders{ContentDisposition: `attachment; filename="transfer-certificate-` + fileSafe(c.TCNumber) + `.pdf"`},
	}, nil
}

// fileSafe keeps letters, digits, dash and underscore so a number can never break the header.
func fileSafe(s string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return -1
	}, s)
}

func transferCertificateDoc(c transfers.Certificate) pdf.Certificate {
	day := func(s string) string {
		if t, err := time.Parse("2006-01-02", s); err == nil {
			return t.Format("02 Jan 2006")
		}
		return s
	}
	dayPtr := func(s *string) string {
		if s == nil {
			return ""
		}
		return day(*s)
	}
	issued, _ := time.Parse("2006-01-02", c.IssuedOn)
	class := derefString(c.ClassName)
	if sec := derefString(c.SectionName); class != "" && sec != "" {
		class += " - " + sec
	}
	var guardians []string
	for _, g := range c.Guardians {
		guardians = append(guardians, fmt.Sprintf("%s (%s)", g.Name, g.Relation))
	}
	return pdf.Certificate{
		Institute: c.SchoolName, Title: "Transfer Certificate", Number: c.TCNumber, IssuedOn: issued,
		Intro: "This is to certify that the student named below was on the rolls of this school and is leaving it.",
		Rows: []pdf.Row{
			{Label: "Name of student", Value: c.Student.FullName},
			{Label: "Registration no.", Value: c.Student.RegistrationCode},
			{Label: "Date of birth", Value: day(c.Student.DateOfBirth)},
			{Label: "Gender", Value: c.Student.Gender},
			{Label: "Date of admission", Value: dayPtr(c.Student.AdmissionDate)},
			{Label: "Parent / guardian", Value: strings.Join(guardians, ", ")},
			{Label: "Class last studied", Value: class},
			{Label: "Academic year", Value: derefString(c.YearName)},
			{Label: "Last day of attendance", Value: dayPtr(c.LastAttendanceDate)},
			{Label: "Reason for leaving", Value: c.ReasonLabel},
			{Label: "Remarks", Value: derefString(c.ReasonNote)},
			{Label: "Moving to", Value: derefString(c.DestinationSchool)},
		},
		Signatory: "Principal",
	}
}
