package pdf_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/pdf"
)

func sample() pdf.Certificate {
	return pdf.Certificate{Institute: "Alpha Public School", Title: "Transfer Certificate", Number: "TC-000001",
		IssuedOn: time.Date(2026, 3, 31, 0, 0, 0, 0, time.UTC), Rows: []pdf.Row{{"Name", "Asha Rao"}, {"Empty", ""}}, Signatory: "Principal"}
}

func TestRenderGivesAPDFWithTheTextAndIsRepeatable(t *testing.T) {
	a, err := pdf.Render(sample())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.HasPrefix(a, []byte("%PDF-")) || !bytes.HasSuffix(bytes.TrimSpace(a), []byte("%%EOF")) || len(a) < 500 {
		t.Fatalf("not a PDF: %d bytes", len(a))
	}
	for _, s := range []string{"TC-000001", "Asha Rao", "Alpha Public School"} {
		if !bytes.Contains(a, []byte(s)) {
			t.Errorf("%q missing", s)
		}
	}
	if b, _ := pdf.Render(sample()); !bytes.Equal(a, b) {
		t.Error("the same input gave different bytes")
	}
}

func TestRenderRefusesNoTitleAndSurvivesOtherScripts(t *testing.T) {
	if _, err := pdf.Render(pdf.Certificate{}); err == nil {
		t.Error("no title accepted")
	}
	c := sample()
	c.Rows = []pdf.Row{{"Name", "आशा Rao é"}}
	if b, err := pdf.Render(c); err != nil || !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Errorf("non-Latin text: %v", err)
	}
}
