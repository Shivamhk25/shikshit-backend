// Package pdf renders simple, print-ready certificates (A4, serif, navy and gold) with github.com/go-pdf/fpdf.
//
// It knows nothing about students or fees: a caller describes a Certificate (title, a few paragraphs, label/value rows,
// a signature line) and gets the bytes back. Later certificates (bonafide, character, fee) reuse it.
//
// Limitation: the built-in PDF fonts cover Latin text only (Windows-1252). Characters outside it, such as Devanagari,
// print as "?" until a Unicode font is embedded; see decision 0033.
package pdf

import (
	"bytes"
	"errors"
	"time"

	"github.com/go-pdf/fpdf"
)

// Row is one "label: value" line of a certificate.
type Row struct{ Label, Value string }

// Certificate is everything the renderer needs. Empty fields are skipped.
type Certificate struct {
	Institute string // the school's name, shown at the top
	Title     string // for example "Transfer Certificate"
	Number    string // the certificate number, shown under the title
	IssuedOn  time.Time
	Intro     string // a sentence before the rows
	Rows      []Row
	Notes     []string // paragraphs after the rows
	Signatory string   // the label under the signature line, for example "Principal"
}

var (
	navy = [3]int{0x1B, 0x2A, 0x4A}
	gold = [3]int{0xB0, 0x8D, 0x3C}
	ink  = [3]int{0x22, 0x22, 0x22}
)

// Render returns the PDF bytes of c. The same input always gives the same bytes.
func Render(c Certificate) ([]byte, error) {
	if c.Title == "" {
		return nil, errors.New("pdf: a certificate needs a title")
	}
	p := fpdf.New("P", "mm", "A4", "")
	p.SetCompression(false) // small documents; keeps the output reproducible and greppable in tests
	p.SetCreationDate(c.IssuedOn.UTC())
	p.SetCatalogSort(true)
	p.SetTitle(c.Title, false)
	p.SetCreator("Shikshit", false)
	p.SetMargins(25, 22, 25)
	p.SetAutoPageBreak(true, 20)
	tr := p.UnicodeTranslatorFromDescriptor("") // UTF-8 in, cp1252 out; unknown runes become "?"
	p.AddPage()

	// frame
	p.SetDrawColor(gold[0], gold[1], gold[2])
	p.SetLineWidth(0.8)
	p.Rect(10, 10, 190, 277, "D")

	p.SetTextColor(navy[0], navy[1], navy[2])
	p.SetFont("Times", "B", 20)
	p.CellFormat(0, 10, tr(c.Institute), "", 1, "C", false, 0, "")
	p.Ln(3)
	p.SetFont("Times", "", 16)
	p.CellFormat(0, 9, tr(c.Title), "", 1, "C", false, 0, "")
	p.SetDrawColor(gold[0], gold[1], gold[2])
	p.Line(70, p.GetY()+1, 140, p.GetY()+1)
	p.Ln(5)

	p.SetTextColor(ink[0], ink[1], ink[2])
	p.SetFont("Times", "", 11)
	if c.Number != "" {
		p.CellFormat(95, 7, tr("No. "+c.Number), "", 0, "L", false, 0, "")
		p.CellFormat(0, 7, tr("Date: "+c.IssuedOn.Format("02 Jan 2006")), "", 1, "R", false, 0, "")
	}
	p.Ln(4)
	if c.Intro != "" {
		p.MultiCell(0, 6, tr(c.Intro), "", "L", false)
		p.Ln(3)
	}
	for _, r := range c.Rows {
		if r.Value == "" {
			continue
		}
		p.SetFont("Times", "B", 11)
		p.CellFormat(60, 7, tr(r.Label), "", 0, "L", false, 0, "")
		p.SetFont("Times", "", 11)
		p.MultiCell(0, 7, tr(r.Value), "", "L", false)
	}
	p.Ln(3)
	for _, n := range c.Notes {
		p.MultiCell(0, 6, tr(n), "", "L", false)
		p.Ln(2)
	}
	if c.Signatory != "" {
		p.SetY(-55)
		p.Line(130, p.GetY(), 175, p.GetY())
		p.SetX(130)
		p.CellFormat(45, 7, tr(c.Signatory), "", 1, "C", false, 0, "")
	}

	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
