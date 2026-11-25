package pdf

import (
	"embed"
	"encoding/binary"
	"sort"
)

// The embedded fonts are Noto Sans (SIL Open Font License 1.1, see fonts/OFL-NotoSans.txt), static unhinted TTFs.
// They cover Latin (with extensions), Greek, Cyrillic and symbols such as the rupee sign.
//
//go:embed fonts/NotoSans-Regular.ttf fonts/NotoSans-Bold.ttf fonts/OFL-NotoSans.txt
var fontFS embed.FS

// fontCoverage is the set of code points that Noto Sans has a glyph for, read from its cmap table.
var fontCoverage = func() map[rune]bool {
	b, err := fontFS.ReadFile("fonts/NotoSans-Regular.ttf")
	if err != nil {
		panic(err)
	}
	return cmapRunes(b)
}()

// cmapRunes lists the code points of a TrueType font's Unicode cmap (formats 4 and 12). Malformed data gives what was read.
func cmapRunes(b []byte) map[rune]bool {
	out := map[rune]bool{}
	u16 := func(o int) int {
		if o < 0 || o+2 > len(b) {
			return 0
		}
		return int(binary.BigEndian.Uint16(b[o:]))
	}
	u32 := func(o int) int {
		if o < 0 || o+4 > len(b) {
			return 0
		}
		return int(binary.BigEndian.Uint32(b[o:]))
	}
	cmap := -1
	for i, n := 0, u16(4); i < n; i++ {
		if string(b[12+16*i:12+16*i+4]) == "cmap" {
			cmap = u32(12 + 16*i + 8)
		}
	}
	if cmap < 0 {
		return out
	}
	for i, n := 0, u16(cmap+2); i < n; i++ {
		pid, eid, off := u16(cmap+4+8*i), u16(cmap+6+8*i), cmap+u32(cmap+8+8*i)
		if !(pid == 0 || (pid == 3 && (eid == 1 || eid == 10))) {
			continue
		}
		switch u16(off) {
		case 4:
			seg := u16(off+6) / 2
			end, start := off+14, off+16+2*seg
			for s := 0; s < seg; s++ {
				for c := u16(start + 2*s); c <= u16(end+2*s) && c != 0xFFFF; c++ {
					out[rune(c)] = true
				}
			}
		case 12:
			for g, n := off+16, u32(off+12); n > 0 && g+12 <= len(b); g, n = g+12, n-1 {
				for c := u32(g); c <= u32(g+4) && c < 0x110000; c++ {
					out[rune(c)] = true
				}
			}
		}
	}
	return out
}

// Unsupported returns the characters in c that no embedded font can draw (for example Devanagari), sorted, once each.
// A caller that must not print a placeholder can refuse the certificate when this is not empty.
func Unsupported(c Certificate) []rune {
	seen := map[rune]bool{}
	add := func(s string) {
		for _, r := range s {
			if r >= ' ' && !fontCoverage[r] && !isCP1252(r) {
				seen[r] = true
			}
		}
	}
	add(c.Institute)
	add(c.Title)
	add(c.Number)
	add(c.Intro)
	add(c.Signatory)
	for _, r := range c.Rows {
		add(r.Label)
		add(r.Value)
	}
	for _, n := range c.Notes {
		add(n)
	}
	out := make([]rune, 0, len(seen))
	for r := range seen {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

const cp1252Extra = "€‚ƒ„…†‡ˆ‰Š‹ŒŽ‘’“”•–—˜™š›œžŸ"

// isCP1252 reports whether the built-in PDF fonts (Windows-1252) can draw r.
func isCP1252(r rune) bool {
	if r < 0x80 || (r >= 0xA0 && r <= 0xFF) {
		return true
	}
	for _, e := range cp1252Extra {
		if e == r {
			return true
		}
	}
	return false
}

func needsUnicodeFont(s string) bool {
	for _, r := range s {
		if !isCP1252(r) {
			return true
		}
	}
	return false
}

// placeholder replaces characters without a glyph, so a gap is visible instead of a silent "?".
func placeholder(s string) string {
	out := []rune(s)
	for i, r := range out {
		if r >= ' ' && !fontCoverage[r] {
			out[i] = '\uFFFD'
		}
	}
	return string(out)
}
