package importer

import "testing"

func TestNormalizeVCard(t *testing.T) {
	const pre, post = "BEGIN:VCARD\r\nVERSION:2.1\r\n", "END:VCARD\r\n"
	tests := []struct{ name, in, want, note string }{
		{"clean utf-8 untouched", pre + "FN:Jürgen Müller\r\n" + post, pre + "FN:Jürgen Müller\r\n" + post, ""},
		{"raw latin-1", pre + "FN:J\xfcrgen \x80\r\n" + post, pre + "FN:Jürgen €\r\n" + post, "1 line(s) converted to UTF-8"},
		{"quoted-printable latin-1",
			pre + "N;CHARSET=ISO-8859-1;ENCODING=QUOTED-PRINTABLE:M=FCller;J=FCrgen\r\n" + post,
			pre + "N;CHARSET=UTF-8:Müller;Jürgen\r\n" + post, "1 line(s) converted to UTF-8"},
		{"quoted-printable utf-8 with soft break",
			pre + "FN;ENCODING=QUOTED-PRINTABLE;CHARSET=UTF-8:J=C3=BCr=\r\ngen\r\n" + post,
			pre + "FN;CHARSET=UTF-8:Jürgen\r\n" + post, "1 line(s) converted to UTF-8"},
		{"qp keeps encoded separators escaped",
			pre + "ORG;ENCODING=QUOTED-PRINTABLE:A=3BB;C\r\n" + post,
			pre + "ORG:A\\;B;C\r\n" + post, "1 line(s) converted to UTF-8"},
		{"mislabelled utf-8 keeps text",
			pre + "FN;CHARSET=ISO-8859-1:Jürgen\r\n" + post,
			pre + "FN;CHARSET=UTF-8:Jürgen\r\n" + post, "1 line(s) converted to UTF-8"},
		{"lf line endings preserved", "BEGIN:VCARD\nFN:\xe9\nEND:VCARD\n", "BEGIN:VCARD\nFN:é\nEND:VCARD\n", "1 line(s) converted to UTF-8"},
	}
	for _, tc := range tests {
		got, note := normalizeVCard([]byte(tc.in))
		if string(got) != tc.want || note != tc.note {
			t.Errorf("%s:\n got %q (%q)\nwant %q (%q)", tc.name, got, note, tc.want, tc.note)
		}
	}
}
