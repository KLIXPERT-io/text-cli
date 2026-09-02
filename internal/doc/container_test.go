package doc

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"errors"
	"io"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// zipBomb builds an archive whose entries are a run of one byte, so deflate
// compresses them by about a thousand to one.
//
// That ratio is the whole problem: the container ceiling in internal/input
// bounds the file on disk at 100 MiB, and a two-megabyte file that passes it
// expands to two gigabytes. Written in chunks so the fixture itself never holds
// the expanded form in memory — which is exactly what the code under test must
// also avoid.
func zipBomb(t *testing.T, size int, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	chunk := bytes.Repeat([]byte(" "), 1<<16)
	for _, name := range names {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		for written := 0; written < size; written += len(chunk) {
			n := len(chunk)
			if rest := size - written; rest < n {
				n = rest
			}
			if _, err := w.Write(chunk[:n]); err != nil {
				t.Fatalf("write %s: %v", name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// zipLieAboutSize rewrites the uncompressed size in the archive's central
// directory, which is where archive/zip reads it from.
//
// It is what turns the declared-size check into a claim rather than a fact, and
// the reason zipRead bounds the reader as well as checking the header: an
// attacker writes the number, and the only honest bound is on the bytes that
// actually come out.
func zipLieAboutSize(t *testing.T, data []byte, actual, claim uint32) []byte {
	t.Helper()
	out := append([]byte(nil), data...)
	var want [4]byte
	binary.LittleEndian.PutUint32(want[:], actual)
	patched := 0
	for i := 0; i+4 <= len(out); i++ {
		// Only the central-directory records: the reader takes the File header
		// from there, and leaving the local headers alone keeps the fixture a
		// realistic forgery rather than a rewritten archive.
		if !bytes.Equal(out[i:i+4], []byte("PK\x01\x02")) {
			continue
		}
		if i+28 <= len(out) && bytes.Equal(out[i+24:i+28], want[:]) {
			binary.LittleEndian.PutUint32(out[i+24:i+28], claim)
			patched++
		}
	}
	if patched == 0 {
		t.Fatal("no central-directory size field matched; fixture is wrong")
	}
	return out
}

func zipRequireCode(t *testing.T, err error, want errs.Code) {
	t.Helper()
	if err == nil {
		t.Fatal("want an error, got none")
	}
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is %T (%v), want *errs.E", err, err)
	}
	if e.Code != want {
		t.Fatalf("code = %q, want %q (message: %s)", e.Code, want, e.Message)
	}
	if e.Hint == "" {
		t.Fatal("error carries no hint; the caller cannot recover")
	}
}

// Every zip-based decoder inflates its parts through one budget, and the budget
// is what stands between a two-megabyte file and a fatal out-of-memory — an
// error no caller can catch, no exit code can describe and no user can act on.
func TestZipReadBudget(t *testing.T) {
	tests := []struct {
		name string
		// build returns the archive and the entry names to read, in order.
		build func(t *testing.T) ([]byte, []string)
		// wantErrAt is the index of the read that must fail, or -1 for none.
		wantErrAt int
	}{
		{
			name: "an entry inside the budget is read",
			build: func(t *testing.T) ([]byte, []string) {
				return zipBomb(t, 1<<20, "a.xml"), []string{"a.xml"}
			},
			wantErrAt: -1,
		},
		{
			name: "an entry declaring more than the budget is refused before it is inflated",
			build: func(t *testing.T) ([]byte, []string) {
				return zipBomb(t, zipMaxText+1, "word/document.xml"), []string{"word/document.xml"}
			},
			wantErrAt: 0,
		},
		{
			name: "an entry that lies about its size is caught by the bounded read",
			build: func(t *testing.T) ([]byte, []string) {
				// Declared as sixteen bytes, delivering more than the budget: the
				// header check waves it through, so the only thing standing
				// between this archive and the whole expansion in memory is that
				// the reader itself is bounded.
				const size = zipMaxText + 1
				data := zipBomb(t, size, "content.xml")
				return zipLieAboutSize(t, data, size, 16), []string{"content.xml"}
			},
			wantErrAt: 0,
		},
		{
			name: "many entries share one budget rather than getting one each",
			build: func(t *testing.T) ([]byte, []string) {
				// Six entries of 12 MiB: each is comfortably inside the budget on
				// its own, and together they exceed it. A per-entry bound would
				// read all six and inflate 72 MiB.
				names := []string{
					"ppt/slides/slide1.xml", "ppt/slides/slide2.xml", "ppt/slides/slide3.xml",
					"ppt/slides/slide4.xml", "ppt/slides/slide5.xml", "ppt/slides/slide6.xml",
				}
				return zipBomb(t, 12<<20, names...), names
			},
			wantErrAt: 5,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			data, names := tc.build(t)
			r, err := openZip("test", data)
			if err != nil {
				t.Fatalf("openZip: %v", err)
			}
			budget := zipNewBudget()
			for i, name := range names {
				b, ok, err := zipRead(r, name, budget)
				if i == tc.wantErrAt {
					zipRequireCode(t, err, errs.CodeInvalidArgs)
					return
				}
				if err != nil {
					t.Fatalf("read %d (%s): %v", i, name, err)
				}
				if !ok {
					t.Fatalf("read %d (%s): entry not found", i, name)
				}
				if len(b) == 0 {
					t.Fatalf("read %d (%s): no bytes", i, name)
				}
			}
			if tc.wantErrAt >= 0 {
				t.Fatalf("read %d entries without the expected error", len(names))
			}
		})
	}
}

// The budget reaches every zip-based decoder through its own Decode, not just
// through zipRead: a decoder that went back to reading a part unbounded would
// take the process down with it, and that is not something a caller can recover
// from or a test above this one would catch.
func TestZipDecodersRefuseABomb(t *testing.T) {
	const big = zipMaxText + 1

	tests := []struct {
		name    string
		decoder Decoder
		build   func(t *testing.T) []byte
	}{
		{
			name:    "docx body part",
			decoder: &docxDecoder{},
			build: func(t *testing.T) []byte {
				return zipBomb(t, big, docxBodyPart)
			},
		},
		{
			name:    "pptx slide part",
			decoder: &pptxDecoder{},
			build: func(t *testing.T) []byte {
				var buf bytes.Buffer
				zw := zip.NewWriter(&buf)
				w, err := zw.Create("ppt/presentation.xml")
				if err != nil {
					t.Fatalf("create presentation: %v", err)
				}
				if _, err := w.Write([]byte(pptxPresentation)); err != nil {
					t.Fatalf("write presentation: %v", err)
				}
				if err := zw.Close(); err != nil {
					t.Fatalf("close: %v", err)
				}
				// Splice the deck's manifest part in front of a bombed slide.
				bomb := zipBomb(t, big, "ppt/slides/slide1.xml")
				return zipMerge(t, buf.Bytes(), bomb)
			},
		},
		{
			name:    "odt content part",
			decoder: &odfDecoder{kind: FormatODT},
			build: func(t *testing.T) []byte {
				return zipBomb(t, big, "content.xml")
			},
		},
		{
			name:    "epub chapter",
			decoder: &epubDecoder{},
			build: func(t *testing.T) []byte {
				bomb := zipBomb(t, big, "OEBPS/chapter1.xhtml")
				return zipMerge(t, epubMinimalShell(t), bomb)
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, err := tc.decoder.Decode(tc.build(t))
			zipRequireCode(t, err, errs.CodeInvalidArgs)
		})
	}
}

// zipMerge rewrites two archives into one. Building the bombed entry with the
// same writer as the small ones would mean holding both in one zip.Writer,
// which is fine — this exists only so each fixture above can say what it needs
// without a bespoke builder.
func zipMerge(t *testing.T, archives ...[]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for _, a := range archives {
		zr, err := zip.NewReader(bytes.NewReader(a), int64(len(a)))
		if err != nil {
			t.Fatalf("read archive: %v", err)
		}
		for _, f := range zr.File {
			w, err := zw.CreateRaw(&f.FileHeader)
			if err != nil {
				t.Fatalf("create raw %s: %v", f.Name, err)
			}
			rc, err := f.OpenRaw()
			if err != nil {
				t.Fatalf("open raw %s: %v", f.Name, err)
			}
			if _, err := io.Copy(w, rc); err != nil {
				t.Fatalf("copy %s: %v", f.Name, err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close zip: %v", err)
	}
	return buf.Bytes()
}

// epubMinimalShell is a book with a container, a package document and a spine
// pointing at one chapter — everything but the chapter itself.
func epubMinimalShell(t *testing.T) []byte {
	t.Helper()
	return epubZip(t, []epubZipEntry{
		{name: "mimetype", body: epubMimetype, store: true},
		{name: "META-INF/container.xml", body: `<?xml version="1.0"?>
<container xmlns="urn:oasis:names:tc:opendocument:xmlns:container">
  <rootfiles><rootfile full-path="OEBPS/book.opf" media-type="application/oebps-package+xml"/></rootfiles>
</container>`},
		{name: "OEBPS/book.opf", body: `<?xml version="1.0"?>
<package xmlns="http://www.idpf.org/2007/opf">
  <metadata><title>Bombe</title></metadata>
  <manifest><item id="c1" href="chapter1.xhtml" media-type="application/xhtml+xml"/></manifest>
  <spine><itemref idref="c1"/></spine>
</package>`},
	})
}
