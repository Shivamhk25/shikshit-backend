package importer

import (
	"bytes"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/xuri/excelize/v2"

	"github.com/Shivamhk25/shikshit-backend/backend/internal/platform/crud"
)

// FileError is a problem with the file as a whole (not one row). It is shown on the "file" field.
type FileError struct{ Code, Message string }

func (e *FileError) Error() string { return e.Code }

func fileErr(code, msg string) error { return &FileError{Code: code, Message: msg} }

// Parsed is a file read into rows of cells, header first. Line numbers are 1-based positions in the file.
type Parsed struct {
	Header []string
	Lines  []int
	Rows   [][]string
}

// ParseFile reads a CSV or XLSX file. The format is decided by the file name and confirmed by the content, so a
// renamed file is not misread. Limits are enforced here, before anything is stored.
func ParseFile(name string, data []byte) (Parsed, error) {
	if len(data) == 0 {
		return Parsed{}, fileErr("empty_file", "The file is empty.")
	}
	if len(data) > MaxFileBytes {
		return Parsed{}, fileErr("file_too_large", fmt.Sprintf("The file is larger than %d MB.", MaxFileBytes>>20))
	}
	isZip := bytes.HasPrefix(data, []byte("PK\x03\x04"))
	lower := strings.ToLower(name)
	var cells [][]string
	var nums []int // file line of each record; nil means position+1 (spreadsheets keep blank rows)
	var err error
	switch {
	case strings.HasSuffix(lower, ".xlsx") && isZip:
		cells, err = readXLSX(data)
	case strings.HasSuffix(lower, ".csv") && !isZip:
		cells, nums, err = readCSV(data)
	case strings.HasSuffix(lower, ".xlsx") || strings.HasSuffix(lower, ".csv"):
		return Parsed{}, fileErr("wrong_format", "The file content does not match its extension.")
	default:
		return Parsed{}, fileErr("unsupported_format", "Upload a .csv or .xlsx file.")
	}
	if err != nil {
		return Parsed{}, err
	}

	var p Parsed
	for i, r := range cells {
		line := i + 1
		if nums != nil {
			line = nums[i]
		}
		for _, c := range r {
			if len(c) > MaxCellChars {
				return Parsed{}, fileErr("cell_too_long", fmt.Sprintf("A cell on line %d is longer than %d characters.", line, MaxCellChars))
			}
		}
		if blank(r) {
			continue
		}
		if p.Header == nil {
			p.Header = r
			continue
		}
		if len(p.Rows) >= MaxRows {
			return Parsed{}, fileErr("too_many_rows", fmt.Sprintf("The file has more than %d rows. Split it into smaller files.", MaxRows))
		}
		p.Lines = append(p.Lines, line)
		p.Rows = append(p.Rows, r)
	}
	if p.Header == nil {
		return Parsed{}, fileErr("empty_file", "The file has no header row.")
	}
	if len(p.Rows) == 0 {
		return Parsed{}, fileErr("no_rows", "The file has a header but no data rows.")
	}
	return p, nil
}

func blank(r []string) bool {
	for _, c := range r {
		if strings.TrimSpace(c) != "" {
			return false
		}
	}
	return true
}

func readCSV(data []byte) ([][]string, []int, error) {
	data = bytes.TrimPrefix(data, []byte("\xef\xbb\xbf")) // Excel writes a byte-order mark
	if !utf8.Valid(data) {
		return nil, nil, fileErr("bad_encoding", "Save the file as UTF-8 and upload it again.")
	}
	first := data
	if i := bytes.IndexByte(data, '\n'); i >= 0 {
		first = data[:i]
	}
	r := csv.NewReader(bytes.NewReader(data))
	r.Comma = sniff(first)
	r.FieldsPerRecord = -1 // ragged rows are reported per row, not as a file failure
	r.TrimLeadingSpace = true
	var out [][]string
	var lines []int
	for {
		rec, err := r.Read()
		if errors.Is(err, io.EOF) {
			return out, lines, nil
		}
		if err != nil {
			return nil, nil, fileErr("invalid_csv", "The file is not a valid CSV file.")
		}
		line, _ := r.FieldPos(0)
		out = append(out, rec)
		lines = append(lines, line)
	}
}

// sniff picks the delimiter used by the header line: comma, semicolon or tab.
func sniff(line []byte) rune {
	best, count := ',', bytes.Count(line, []byte(","))
	for _, d := range []rune{';', '\t'} {
		if n := bytes.Count(line, []byte(string(d))); n > count {
			best, count = d, n
		}
	}
	return best
}

func readXLSX(data []byte) ([][]string, error) {
	f, err := excelize.OpenReader(bytes.NewReader(data), excelize.Options{UnzipSizeLimit: 100 << 20, UnzipXMLSizeLimit: 50 << 20})
	if err != nil {
		return nil, fileErr("invalid_xlsx", "The file is not a valid Excel workbook.")
	}
	defer f.Close()
	sheets := f.GetSheetList()
	if len(sheets) == 0 {
		return nil, fileErr("empty_file", "The workbook has no sheets.")
	}
	rows, err := f.GetRows(sheets[0])
	if err != nil {
		return nil, fileErr("invalid_xlsx", "The first sheet could not be read.")
	}
	return rows, nil
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// NormaliseHeader makes "Display order", "display_order" and "displayOrder" the same.
func NormaliseHeader(s string) string { return nonAlnum.ReplaceAllString(strings.ToLower(s), "") }

// MapHeader matches the file's headers to the entity's columns. It returns the column key for each header position
// ("" for unknown headers), plus file-level problems: missing required columns and repeated columns are errors,
// unknown columns are warnings (they are ignored).
func MapHeader(header []string, cols []Column) (keys []string, unknown []string, err error) {
	byName := map[string]string{}
	for _, c := range cols {
		byName[NormaliseHeader(c.Key)] = c.Key
		byName[NormaliseHeader(c.Label)] = c.Key
	}
	keys = make([]string, len(header))
	seen := map[string]bool{}
	var dup []string
	for i, h := range header {
		h = strings.TrimSpace(h)
		k, ok := byName[NormaliseHeader(h)]
		if !ok {
			if h != "" {
				unknown = append(unknown, h)
			}
			continue
		}
		if seen[k] {
			dup = append(dup, h)
			continue
		}
		seen[k] = true
		keys[i] = k
	}
	var fe crud.FieldErrors
	if len(dup) > 0 {
		fe.Add("file", "duplicate_columns", "These columns appear more than once: "+strings.Join(dup, ", ")+".")
	}
	var missing []string
	for _, c := range cols {
		if c.Required && !seen[c.Key] {
			missing = append(missing, c.Label)
		}
	}
	if len(missing) > 0 {
		fe.Add("file", "missing_columns", "The file is missing required columns: "+strings.Join(missing, ", ")+".")
	}
	return keys, unknown, fe.OrNil()
}
