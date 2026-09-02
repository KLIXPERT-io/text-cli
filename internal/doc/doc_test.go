package doc

import (
	"bytes"
	"strings"
	"testing"
)

func TestLooksBinary(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"empty input is not binary", nil, false},
		{"ascii prose", []byte("The quick brown fox."), false},
		{"valid utf-8 german prose", []byte("Grüße aus Wien, München und Köln."), false},
		{"a nul byte anywhere in the head", append([]byte("text then "), 0, 'x'), true},
		{"a zip container", []byte("PK\x03\x04\x14\x00\x00\x00"), true},
		{
			// The case that motivates the prefix rule: a latin-1 file whose only
			// invalid byte is its last one is still not text this CLI can read,
			// and forgiving it would let a whole class of binaries through.
			name: "invalid utf-8 at the very end of a short input",
			data: []byte("hello\xfc"), want: true,
		},
		{
			// A prefix that really was cut can end mid-rune, and that is not the
			// file's fault. 8000 bytes of ASCII plus a 2-byte rune straddling the
			// cut must read as text.
			name: "a multi-byte rune split by the 8000-byte cut",
			data: append(bytes.Repeat([]byte("a"), sniffBinaryBytes-1), []byte("ü…")...),
			want: false,
		},
		{
			// Past the cut, nothing is inspected: a file that is prose for its
			// first 8 KiB is treated as prose.
			name: "binary beyond the sniffed prefix",
			data: append(bytes.Repeat([]byte("a"), sniffBinaryBytes), 0),
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := LooksBinary(tc.data); got != tc.want {
				t.Errorf("LooksBinary = %v, want %v", got, tc.want)
			}
		})
	}
}

// HasNulByte is the half of LooksBinary that --from text does not waive, so it
// must be strictly narrower: NUL yes, odd encoding no.
func TestHasNulByte(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want bool
	}{
		{"prose", []byte("Nothing to see."), false},
		{"latin-1 prose is not a nul", []byte("Gr\xfc\xdfe"), false},
		{"a zip container", []byte("PK\x03\x04\x14\x00"), true},
		{"utf-16 text, which this CLI cannot read either", []byte("h\x00e\x00l\x00"), true},
		{"beyond the sniffed prefix", append(bytes.Repeat([]byte("a"), sniffBinaryBytes), 0), false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := HasNulByte(tc.data); got != tc.want {
				t.Errorf("HasNulByte = %v, want %v", got, tc.want)
			}
			if got := HasNulByte(tc.data); got && !LooksBinary(tc.data) {
				t.Error("HasNulByte is true but LooksBinary is false: the narrower test must imply the wider one")
			}
		})
	}
}

func TestUnsupportedErr(t *testing.T) {
	tests := []struct {
		name     string
		path     string
		data     []byte
		wantMsg  string
		wantHint string
	}{
		{
			// The accident worth naming: a .doc looks like a .docx to everyone
			// except the parser, and the fix is one "Save As" away.
			name: "a pre-2007 office file is named as such",
			path: "letter.doc", data: legacyOffice,
			wantMsg: "pre-2007 Office file", wantHint: ".docx",
		},
		{
			name: "anything else names the formats that would have worked",
			path: "photo.jpg", data: []byte{0xFF, 0xD8, 0xFF},
			wantMsg: "photo.jpg is binary", wantHint: ".pdf",
		},
		{
			name: "stdin has no path to report",
			path: "", data: []byte{0xFF, 0xD8, 0xFF},
			wantMsg: "the input is binary", wantHint: "--from",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			err := UnsupportedErr(tc.path, tc.data)
			if !strings.Contains(err.Message, tc.wantMsg) {
				t.Errorf("message = %q, want it to contain %q", err.Message, tc.wantMsg)
			}
			if !strings.Contains(err.Hint, tc.wantHint) {
				t.Errorf("hint = %q, want it to contain %q", err.Hint, tc.wantHint)
			}
		})
	}
}
