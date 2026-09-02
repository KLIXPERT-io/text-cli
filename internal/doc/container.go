package doc

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"io"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// Four of the six formats in this package are a zip of XML: DOCX, PPTX, ODT
// and EPUB all put their prose in entries inside a container. The container
// handling is identical for all of them and the XML inside is not, so the zip
// half lives here once and each decoder brings only its own vocabulary.

// zipMagic is the local file header every zip starts with. Sniffing it is not
// enough to name a format — all four zip-based decoders would claim it — so
// each of them looks inside before saying yes.
var zipMagic = []byte{'P', 'K', 0x03, 0x04}

// isZip reports whether data begins with a zip local file header.
func isZip(data []byte) bool { return bytes.HasPrefix(data, zipMagic) }

// openZip reads the container. A truncated archive is reported as invalid
// args rather than as a decode failure: in practice it means the file was cut
// short by --max-bytes or by a failed download, and the fix is the user's.
func openZip(format string, data []byte) (*zip.Reader, error) {
	if !isZip(data) {
		return nil, errs.Newf(errs.CodeInvalidArgs, "not a %s file: it is not a zip container", format).
			WithHint("Every " + format + " file is a zip archive. Check the file is what its name says, or use --from to name the real format.")
	}
	r, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errs.Newf(errs.CodeInvalidArgs, "%s container is unreadable: %s", format, err.Error()).
			WithHint("The file may be truncated or password-protected. Try opening it in an editor first.")
	}
	return r, nil
}

// zipMaxText bounds the bytes one decoder will inflate out of one container.
//
// The container ceiling in internal/input bounds the *compressed* file, and
// deflate reaches roughly 1000:1 on the runs of one byte a bomb is built from —
// so a 2 MB .docx that passes every check above expands to two gigabytes and
// takes the process down with a fatal out-of-memory, which is not an error a
// caller can catch or a user can act on. The budget is on total bytes rather
// than on a number of entries because an entry count is not pathological: a
// reference work really does ship several thousand chapters. Bytes have no such
// ambiguity — the XML of a novel is a few megabytes and nothing honest
// approaches this — and exceeding it is an error rather than a quiet
// truncation, because a score over half a document is a wrong number, not a
// partial one.
const zipMaxText = 64 << 20

// zipNewBudget returns a fresh decompression budget for one Decode call.
//
// It is per call, not per entry: a container holding a thousand entries that are
// each just under the limit would otherwise cost a thousand times the limit, and
// the thing worth bounding is what one document can make this process inflate.
func zipNewBudget() *int64 {
	b := int64(zipMaxText)
	return &b
}

// zipEntry reads one entry by exact name against a budget of its own.
//
// It is for the incidental single reads — a mimetype, a core.xml — where there
// is no document-wide budget to share. A decoder reading the parts that carry
// the prose uses zipRead so that all of them are charged against one budget.
//
// Entry names are matched case-sensitively because the formats specify them
// that way; a writer that shipped "Word/document.xml" is broken, and guessing
// would only hide that from the error message.
func zipEntry(r *zip.Reader, name string) ([]byte, bool, error) {
	return zipRead(r, name, zipNewBudget())
}

// zipRead reads one entry and charges it against a decompression budget.
//
// The declared size is checked before the entry is inflated, so a bomb is
// refused rather than expanded into memory and then rejected. It is checked
// again afterwards against the bytes actually produced, because the declared
// size is a number in the archive and a crafted one does not honour it — which
// is why the reader itself is bounded rather than trusted.
func zipRead(r *zip.Reader, name string, budget *int64) ([]byte, bool, error) {
	for _, f := range r.File {
		if f.Name != name {
			continue
		}
		if *budget < 0 || f.UncompressedSize64 > uint64(*budget) {
			return nil, true, zipTooLargeErr(name)
		}
		rc, err := f.Open()
		if err != nil {
			return nil, true, errs.Newf(errs.CodeInvalidArgs, "cannot read %s: %s", name, err.Error()).
				WithHint("The archive entry is damaged. Open the file in its own editor and save it again.")
		}
		defer rc.Close()
		// One byte past the budget so that an entry landing exactly on it is
		// read rather than refused, and one that overruns is caught here instead
		// of after it has been allocated.
		b, err := io.ReadAll(io.LimitReader(rc, *budget+1))
		if err != nil {
			return nil, true, errs.Newf(errs.CodeInvalidArgs, "cannot read %s: %s", name, err.Error()).
				WithHint("The archive entry is damaged. Open the file in its own editor and save it again.")
		}
		if int64(len(b)) > *budget {
			return nil, true, zipTooLargeErr(name)
		}
		*budget -= int64(len(b))
		return b, true, nil
	}
	return nil, false, nil
}

func zipTooLargeErr(name string) *errs.E {
	return errs.Newf(errs.CodeInvalidArgs, "%s expands to more than %d MiB of text", name, zipMaxText>>20).
		WithHint("Split the document, or export the text and analyse that instead.")
}

// zipHas reports whether an entry exists, without reading it. Used by Sniff,
// which must stay cheap and must not inflate a bomb to answer.
func zipHas(r *zip.Reader, name string) bool {
	for _, f := range r.File {
		if f.Name == name {
			return true
		}
	}
	return false
}

// zipNames lists entry names with a prefix and suffix, in the archive's own
// order. PPTX slide order and EPUB spine order both matter, and both are
// recovered from a manifest rather than from this — but a decoder that has no
// manifest to read gets a deterministic order here rather than a map's.
func zipNames(r *zip.Reader, prefix, suffix string) []string {
	var out []string
	for _, f := range r.File {
		if strings.HasPrefix(f.Name, prefix) && strings.HasSuffix(f.Name, suffix) {
			out = append(out, f.Name)
		}
	}
	return out
}

// newXMLDecoder returns a decoder that does not give up on a legacy encoding.
//
// OOXML and ODF are UTF-8 by specification, but EPUB carries whatever XHTML an
// author had, and encoding/xml refuses any charset it does not know rather
// than degrading. Passing the bytes through for a single-byte charset gets the
// ASCII right and mangles only the accented characters, which is much better
// than refusing to read the book.
func newXMLDecoder(data []byte) *xml.Decoder {
	d := xml.NewDecoder(bytes.NewReader(data))
	d.Strict = false
	d.CharsetReader = func(charset string, input io.Reader) (io.Reader, error) {
		return input, nil
	}
	return d
}

// localName returns an XML element's name without its namespace prefix.
// Namespaces in these formats are declared with prefixes that differ between
// writers (w:, a:, text:), so every walker matches on the local name.
func localName(n xml.Name) string { return n.Local }
