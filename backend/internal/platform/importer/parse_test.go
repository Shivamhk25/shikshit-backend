package importer

import (
	"bytes"
	"errors"
	"strings"
	"testing"

	"github.com/xuri/excelize/v2"

	"github.com/pie360/backend/internal/platform/crud"
)

func fileCode(t *testing.T, err error) string {
	t.Helper()
	var fe *FileError
	if !errors.As(err, &fe) {
		t.Fatalf("want FileError, got %v", err)
	}
	return fe.Code
}

func xlsx(t *testing.T, rows [][]any) []byte {
	t.Helper()
	f := excelize.NewFile()
	for i, r := range rows {
		cell, _ := excelize.CoordinatesToCellName(1, i+1)
		if err := f.SetSheetRow("Sheet1", cell, &r); err != nil {
			t.Fatal(err)
		}
	}
	var b bytes.Buffer
	if err := f.Write(&b); err != nil {
		t.Fatal(err)
	}
	return b.Bytes()
}

func TestParseCSV(t *testing.T) {
	cases := map[string]string{
		"plain":        "code,name\nA,Alpha\nB,Beta\n",
		"bom":          "\xef\xbb\xbfcode,name\nA,Alpha\nB,Beta\n",
		"semicolon":    "code;name\nA;Alpha\nB;Beta\n",
		"tab":          "code\tname\nA\tAlpha\nB\tBeta\n",
		"blank lines":  "\n\ncode,name\n\nA,Alpha\n,\nB,Beta\n\n",
		"crlf":         "code,name\r\nA,Alpha\r\nB,Beta\r\n",
		"quoted comma": "code,name\nA,\"Alpha\"\nB,Beta\n",
	}
	for name, in := range cases {
		t.Run(name, func(t *testing.T) {
			p, err := ParseFile("x.csv", []byte(in))
			if err != nil {
				t.Fatal(err)
			}
			if len(p.Header) != 2 || len(p.Rows) != 2 || p.Rows[1][1] != "Beta" {
				t.Fatalf("got %+v", p)
			}
		})
	}
}

func TestParseCSVQuotedDelimiterAndLines(t *testing.T) {
	p, err := ParseFile("x.csv", []byte("code,name\nA,\"Alpha, the first\"\n\nB,Beta\n"))
	if err != nil {
		t.Fatal(err)
	}
	if p.Rows[0][1] != "Alpha, the first" {
		t.Fatalf("quoted comma lost: %q", p.Rows[0][1])
	}
	// the blank line is skipped but line numbers still point at the file
	if p.Lines[0] != 2 || p.Lines[1] != 4 {
		t.Fatalf("lines = %v", p.Lines)
	}
}

func TestParseXLSX(t *testing.T) {
	p, err := ParseFile("x.xlsx", xlsx(t, [][]any{{"code", "name"}, {"A", "Alpha"}, {"B", "Beta"}}))
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Rows) != 2 || p.Rows[0][0] != "A" {
		t.Fatalf("got %+v", p)
	}
}

func TestParseRejectsBadFiles(t *testing.T) {
	good := "code,name\nA,Alpha\n"
	cases := []struct {
		name, file string
		data       []byte
		code       string
	}{
		{"empty", "x.csv", nil, "empty_file"},
		{"only blanks", "x.csv", []byte("\n\n,\n"), "empty_file"},
		{"header only", "x.csv", []byte("code,name\n"), "no_rows"},
		{"wrong extension", "x.txt", []byte(good), "unsupported_format"},
		{"no extension", "x", []byte(good), "unsupported_format"},
		{"csv named xlsx", "x.xlsx", []byte(good), "wrong_format"},
		{"xlsx named csv", "x.csv", xlsx(t, [][]any{{"a"}, {"b"}}), "wrong_format"},
		{"not utf8", "x.csv", []byte("code,name\nA,\xff\xfe\n"), "bad_encoding"},
		{"corrupt zip", "x.xlsx", []byte("PK\x03\x04garbage"), "invalid_xlsx"},
		{"bare quote", "x.csv", []byte("code,name\nA,\"unterminated\n"), "invalid_csv"},
		{"cell too long", "x.csv", []byte("code,name\nA," + strings.Repeat("x", MaxCellChars+1) + "\n"), "cell_too_long"},
		{"too big", "x.csv", append([]byte("code,name\nA,"), bytes.Repeat([]byte("x"), MaxFileBytes)...), "file_too_large"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := ParseFile(c.file, c.data)
			if got := fileCode(t, err); got != c.code {
				t.Fatalf("code = %s, want %s", got, c.code)
			}
		})
	}
}

func TestParseRowLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("code\n")
	for i := 0; i < MaxRows; i++ {
		b.WriteString("r\n")
	}
	if p, err := ParseFile("x.csv", []byte(b.String())); err != nil || len(p.Rows) != MaxRows {
		t.Fatalf("exactly the limit must pass: %v", err)
	}
	b.WriteString("r\n")
	_, err := ParseFile("x.csv", []byte(b.String()))
	if got := fileCode(t, err); got != "too_many_rows" {
		t.Fatalf("code = %s", got)
	}
}

func TestMapHeader(t *testing.T) {
	cols := []Column{
		{Key: "code", Label: "Code", Required: true},
		{Key: "displayOrder", Label: "Display order"},
		{Key: "name", Label: "Name", Required: true},
	}
	keys, unknown, err := MapHeader([]string{"CODE", "display_order", " Name ", "Notes", ""}, cols)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"code", "displayOrder", "name", "", ""}
	for i := range want {
		if keys[i] != want[i] {
			t.Fatalf("keys = %v", keys)
		}
	}
	if len(unknown) != 1 || unknown[0] != "Notes" {
		t.Fatalf("unknown = %v", unknown)
	}

	_, _, err = MapHeader([]string{"code"}, cols)
	var fe crud.FieldErrors
	if !errors.As(err, &fe) || fe[0].Code != "missing_columns" || !strings.Contains(fe[0].Message, "Name") {
		t.Fatalf("missing: %v", err)
	}
	_, _, err = MapHeader([]string{"code", "name", "Code"}, cols)
	if !errors.As(err, &fe) || fe[0].Code != "duplicate_columns" {
		t.Fatalf("duplicate: %v", err)
	}
}

func TestSafeCell(t *testing.T) {
	cases := map[string]string{
		"":          "",
		"plain":     "plain",
		"=SUM(A1)":  "'=SUM(A1)",
		"+1+1":      "'+1+1",
		"@cmd":      "'@cmd",
		"-cmd":      "'-cmd",
		"-5":        "-5",
		"-.5":       "-.5",
		"a=b":       "a=b",
		"\tleading": "'\tleading",
	}
	for in, want := range cases {
		if got := SafeCell(in); got != want {
			t.Errorf("SafeCell(%q) = %q, want %q", in, got, want)
		}
	}
}
