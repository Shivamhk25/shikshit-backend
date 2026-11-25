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

func TestRenderRefusesNoTitle(t *testing.T) {
	if _, err := pdf.Render(pdf.Certificate{}); err == nil {
		t.Error("no title accepted")
	}
}

func TestRenderEmbedsNotoSansForTextOutsideWindows1252(t *testing.T) {
	c := sample()
	c.Rows = []pdf.Row{{"Fee", "₹ 12,500 – Łukasz Żółć Ωmega Привет"}}
	b, err := pdf.Render(c)
	if err != nil || !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatalf("render: %v", err)
	}
	if !bytes.Contains(b, []byte("utf8noto")) {
		t.Error("the Unicode font was not used")
	}
	if got := pdf.Unsupported(c); len(got) != 0 {
		t.Errorf("Noto Sans should draw all of it, unsupported: %q", string(got))
	}
	if b2, _ := pdf.Render(c); !bytes.Equal(b, b2) {
		t.Error("the same input gave different bytes")
	}
}

func TestLatinOnlyStaysInTimes(t *testing.T) {
	b, _ := pdf.Render(sample())
	if bytes.Contains(b, []byte("utf8noto")) {
		t.Error("Latin-only certificate pulled in the Unicode font")
	}
}

func TestDevanagariIsReportedNotSilentlyPrinted(t *testing.T) {
	c := sample()
	c.Rows = []pdf.Row{{"Name", "आशा Rao"}}
	b, err := pdf.Render(c)
	if err != nil || !bytes.HasPrefix(b, []byte("%PDF-")) {
		t.Fatalf("render: %v", err)
	}
	got := pdf.Unsupported(c)
	if string(got) != "आशा" {
		t.Errorf("unsupported = %q", string(got))
	}
	if len(pdf.Unsupported(sample())) != 0 {
		t.Error("Latin reported as unsupported")
	}
}
