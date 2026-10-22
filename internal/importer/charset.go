package importer

import (
	"bytes"
	"fmt"
	"strings"
	"unicode/utf8"
)

// cp1252High maps bytes 0x80-0x9F of Windows-1252 to Unicode; the rest of
// Windows-1252 is identical to ISO-8859-1. Undefined bytes map to themselves.
var cp1252High = [32]rune{
	0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
	0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
}

func fromCP1252(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		switch {
		case c < 0x80:
			sb.WriteByte(c)
		case c < 0xA0:
			sb.WriteRune(cp1252High[c-0x80])
		default:
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}

// toUTF8 returns b as UTF-8. Valid UTF-8 is kept even if a legacy charset is
// declared (mislabelled UTF-8 is common, and genuine Latin-1 text with
// accented letters is almost never valid UTF-8); anything else is assumed to
// be Windows-1252, the usual origin of stray 8-bit bytes in old address books.
func toUTF8(b []byte) string {
	if utf8.Valid(b) {
		return string(b)
	}
	return fromCP1252(b)
}

// decodeQP decodes a quoted-printable value. Encoded structural characters
// (; , \) stay escaped so they do not turn into separators. Malformed escapes
// are kept literally.
func decodeQP(s string) []byte {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if s[i] == '=' && i+2 < len(s) && unhex(s[i+1]) >= 0 && unhex(s[i+2]) >= 0 {
			c := byte(unhex(s[i+1])<<4 | unhex(s[i+2]))
			if c == ';' || c == ',' || c == '\\' {
				out = append(out, '\\')
			}
			out = append(out, c)
			i += 2
			continue
		}
		out = append(out, s[i])
	}
	return out
}

func unhex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	}
	return -1
}

// normalizeVCard rewrites the content lines of a vCard that are not clean
// UTF-8 text: lines with non-UTF-8 bytes, a legacy CHARSET parameter, or
// quoted-printable encoding are converted to plain UTF-8 with CHARSET=UTF-8
// (if a charset was declared). All other bytes are kept untouched. It returns
// the new data and a short description of what was changed ("" if nothing).
func normalizeVCard(data []byte) ([]byte, string) {
	lines := bytes.SplitAfter(data, []byte("\n"))
	var out bytes.Buffer
	var fixed int
	for i := 0; i < len(lines); {
		j := i + 1
		qp := isQPStart(lines[i])
		for j < len(lines) && len(lines[j]) > 0 {
			prev := lines[j-1]
			folded := lines[j][0] == ' ' || lines[j][0] == '\t'
			soft := qp && bytes.HasSuffix(bytes.TrimRight(prev, "\r\n"), []byte("="))
			if !folded && !soft {
				break
			}
			j++
		}
		group := lines[i:j]
		i = j
		if fixedLine, ok := fixGroup(group, qp); ok {
			out.WriteString(fixedLine)
			fixed++
			continue
		}
		for _, l := range group {
			out.Write(l)
		}
	}
	if fixed == 0 {
		return data, ""
	}
	return out.Bytes(), fmt.Sprintf("%d line(s) converted to UTF-8", fixed)
}

func isQPStart(line []byte) bool {
	head, _, ok := splitContentLine(string(line))
	if !ok {
		return false
	}
	_, qp, _ := parseParams(head)
	return qp
}

// splitContentLine splits "NAME;PARAMS:value" at the first colon outside
// double quotes.
func splitContentLine(s string) (head, value string, ok bool) {
	inQ := false
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '"':
			inQ = !inQ
		case ':':
			if !inQ {
				return s[:i], s[i+1:], true
			}
		}
	}
	return "", "", false
}

// parseParams returns the head's parts with ENCODING=QUOTED-PRINTABLE
// removed, whether it was present, and the declared charset ("" if none).
func parseParams(head string) (parts []string, qp bool, charset string) {
	for i, p := range strings.Split(head, ";") {
		if i > 0 {
			u := strings.ToUpper(p)
			switch {
			case u == "ENCODING=QUOTED-PRINTABLE" || u == "QUOTED-PRINTABLE":
				qp = true
				continue
			case strings.HasPrefix(u, "CHARSET="):
				charset = p[len("CHARSET="):]
			}
		}
		parts = append(parts, p)
	}
	return parts, qp, charset
}

// fixGroup converts one logical content line (with its folded continuation
// lines) if needed. ok is false if the line is already fine.
func fixGroup(group [][]byte, qp bool) (string, bool) {
	var raw []byte
	for _, l := range group {
		raw = append(raw, l...)
	}
	eol := "\n"
	if bytes.HasSuffix(raw, []byte("\r\n")) {
		eol = "\r\n"
	}
	flat := unfold(group, qp)
	head, value, ok := splitContentLine(string(flat))
	if !ok {
		if utf8.Valid(flat) {
			return "", false
		}
		return fromCP1252(raw), true
	}
	parts, _, charset := parseParams(head)
	legacy := charset != "" && !strings.EqualFold(charset, "utf-8")
	if !qp && !legacy && utf8.Valid(flat) {
		return "", false
	}
	v := []byte(value)
	if qp {
		v = decodeQP(value)
		if bytes.ContainsAny(v, "\r\n") {
			return "", false // cannot be represented on one line; keep as is
		}
	}
	text := toUTF8(v)
	for i, p := range parts {
		if i > 0 && strings.HasPrefix(strings.ToUpper(p), "CHARSET=") {
			parts[i] = "CHARSET=UTF-8"
		}
	}
	return strings.Join(parts, ";") + ":" + text + eol, true
}

// unfold joins the physical lines of a group into one line without its
// terminator, undoing RFC 2425 folding and quoted-printable soft breaks.
func unfold(group [][]byte, qp bool) []byte {
	var b []byte
	for i, l := range group {
		l = bytes.TrimRight(l, "\r\n")
		if i > 0 {
			if qp && bytes.HasSuffix(b, []byte("=")) {
				b = b[:len(b)-1]
			} else if len(l) > 0 && (l[0] == ' ' || l[0] == '\t') {
				l = l[1:]
			}
		}
		b = append(b, l...)
	}
	return b
}
