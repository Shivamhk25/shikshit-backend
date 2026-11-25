// Package pdf renders simple, print-ready certificates (A4, serif, navy and gold) with github.com/go-pdf/fpdf.
//
// It knows nothing about students or fees: a caller describes a Certificate (title, a few paragraphs, label/value rows,
// a signature line) and gets the bytes back. Later certificates (bonafide, character, fee) reuse it.
//
// Fonts: text the built-in serif font can draw (Windows-1252) stays in Times. Any other string is drawn in the embedded
// Noto Sans (Latin extensions, Greek, Cyrillic, the rupee sign). Characters that font lacks are drawn as U+FFFD, and
// Unsupported lists them so a caller can refuse instead of printing a placeholder.
//
// Limitation: fpdf does no complex-script shaping. Devanagari is NOT supported: vowel signs such as the short "i" are
// placed after the consonant and conjuncts are not formed, so common words come out misspelt (decision 0027).
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
	tr := p.UnicodeTranslatorFromDescriptor("") // UTF-8 in, cp1252 out, for strings that need nothing else
	// face picks the font for s and returns the text to print: Times for Windows-1252 text, else Noto Sans.
	var fontErr error
	loaded := false
	face := func(style string, size float64, s string) string {
		if needsUnicodeFont(s) {
			if !loaded { // Noto Sans is added to the file only when a string needs it
				loaded = true
				for _, f := range [][2]string{{"", "fonts/NotoSans-Regular.ttf"}, {"B", "fonts/NotoSans-Bold.ttf"}} {
					b, err := fontFS.ReadFile(f[1])
					if err != nil {
						fontErr = err
						continue
					}
					p.AddUTF8FontFromBytes("Noto", f[0], b)
				}
			}
			p.SetFont("Noto", style, size)
			return placeholder(s)
		}
		p.SetFont("Times", style, size)
		return tr(s)
	}
	p.AddPage()

	// frame
	p.SetDrawColor(gold[0], gold[1], gold[2])
	p.SetLineWidth(0.8)
	p.Rect(10, 10, 190, 277, "D")

	p.SetTextColor(navy[0], navy[1], navy[2])
	p.CellFormat(0, 10, face("B", 20, c.Institute), "", 1, "C", false, 0, "")
	p.Ln(3)
	p.CellFormat(0, 9, face("", 16, c.Title), "", 1, "C", false, 0, "")
	p.SetDrawColor(gold[0], gold[1], gold[2])
	p.Line(70, p.GetY()+1, 140, p.GetY()+1)
	p.Ln(5)

	p.SetTextColor(ink[0], ink[1], ink[2])
	if c.Number != "" {
		p.CellFormat(95, 7, face("", 11, "No. "+c.Number), "", 0, "L", false, 0, "")
		p.CellFormat(0, 7, face("", 11, "Date: "+c.IssuedOn.Format("02 Jan 2006")), "", 1, "R", false, 0, "")
	}
	p.Ln(4)
	if c.Intro != "" {
		p.MultiCell(0, 6, face("", 11, c.Intro), "", "L", false)
		p.Ln(3)
	}
	for _, r := range c.Rows {
		if r.Value == "" {
			continue
		}
		p.CellFormat(60, 7, face("B", 11, r.Label), "", 0, "L", false, 0, "")
		p.MultiCell(0, 7, face("", 11, r.Value), "", "L", false)
	}
	p.Ln(3)
	for _, n := range c.Notes {
		p.MultiCell(0, 6, face("", 11, n), "", "L", false)
		p.Ln(2)
	}
	if c.Signatory != "" {
		p.SetY(-55)
		p.Line(130, p.GetY(), 175, p.GetY())
		p.SetX(130)
		p.CellFormat(45, 7, face("", 11, c.Signatory), "", 1, "C", false, 0, "")
	}

	if fontErr != nil {
		return nil, fontErr
	}
	var buf bytes.Buffer
	if err := p.Output(&buf); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
