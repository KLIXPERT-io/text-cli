// Package doc turns a document file into the markdown the rest of the CLI can
// measure.
//
// It is the second missing input source. `internal/fetch` closed the gap
// between a web page and a readability score; this package closes the gap
// between a file a human was actually sent — a PDF, a Word document, a
// presentation, an EPUB — and the same score. Without it, `text lint
// --file report.pdf` reads a binary stream as prose and reports a confidently
// meaningless number, which is worse than refusing.
//
// A decoder returns **markdown, not plain text**, for exactly the reason a
// fetcher does: `State.LoadInput` runs every document through `internal/strip`,
// and that is where a heading becomes a terminated sentence rather than being
// glued to the paragraph below it. A decoder that pre-flattens its own headings
// hands the tokenizer one document-sized sentence and quietly ruins every
// per-sentence average. Emit `## Heading`, `- item`, a blank line between
// paragraphs, and let the strip pass do what it already knows how to do.
//
// Adding a format is one file plus one Register call in its init — the same
// "register, don't wire" rule the metrics, lint rules, entity providers,
// knowledge sources, fetchers, and research sources follow.
package doc

import (
	"bytes"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// FormatText is the reserved name for "this is already text, do not decode".
// It is not a registered decoder: it is what --from text means and what
// ForFile returns for a .md, .txt, or .html file.
const FormatText = "text"

// Document is one decoded file, reduced to what an analysis command needs.
//
// It is not a document model: the styles, the images, the revision history and
// the embedded objects a source format may carry are deliberately absent,
// because this type feeds a readability score and an entity extractor. What it
// carries is the prose and enough provenance to attribute it.
type Document struct {
	// Content is the document as markdown. It is the field every downstream
	// command reads. See the package comment for why it is markdown.
	Content string `json:"content"`
	// Title is the document's own metadata title, when the format records one
	// and the author filled it in. It is not the first heading: a made-up
	// title is worse than none.
	Title string `json:"title,omitempty"`
	// Format is the registry name of the decoder that read the file, echoed in
	// output so a number can be traced back to how the text was obtained.
	Format string `json:"format,omitempty"`
	// Pages is the page count for formats that paginate, and 0 for the ones
	// that do not. A DOCX has no pages until it is laid out for a printer, so
	// reporting one would be inventing a fact.
	Pages int `json:"pages,omitempty"`
}

// Decoder turns the bytes of one file into a Document.
//
// Decode receives the whole file rather than a reader because every format
// here needs random access — a zip container reads its central directory from
// the end, and a PDF reads its xref table from the end — and because the
// caller has already bounded the size. A decoder is expected to be safe for
// reuse across calls and to translate its own failures into *errs.E, because
// the caller renders the code, not the prose.
type Decoder interface {
	// Name is the stable identifier used by --from and echoed in output.
	Name() string
	// Extensions are the lower-case file extensions this decoder owns,
	// including the leading dot. They are matched before Sniff, because a name
	// is what the user typed and a magic number is only a guess.
	Extensions() []string
	// Sniff reports whether the bytes look like this format. It exists for
	// stdin, which has no name at all, and for a file named wrongly or not at
	// all. It must never claim bytes it cannot decode: a false positive turns
	// a readable text file into a decode error.
	Sniff(data []byte) bool
	// Decode reads one document. A file that is not this format at all is
	// errs.CodeInvalidArgs; a file that is this format but carries no
	// extractable text is errs.CodeEmptyInput.
	Decode(data []byte) (*Document, error)
}

var (
	mu        sync.RWMutex
	factories = map[string]func() (Decoder, error){}
)

// Register adds a decoder factory. Factories are called lazily by Open, so
// registering must stay cheap — no files, no allocation of parsers, no work.
// It panics on a duplicate name, which can only be a programming error and
// only ever at init time.
func Register(name string, factory func() (Decoder, error)) {
	mu.Lock()
	defer mu.Unlock()
	key := strings.ToLower(strings.TrimSpace(name))
	if key == "" {
		panic("doc: Register with empty name")
	}
	if key == FormatText {
		panic("doc: " + FormatText + " is reserved for undecoded input")
	}
	if _, dup := factories[key]; dup {
		panic("doc: duplicate decoder " + key)
	}
	factories[key] = factory
}

// Open constructs the named decoder.
func Open(name string) (Decoder, error) {
	key := strings.ToLower(strings.TrimSpace(name))
	mu.RLock()
	factory, ok := factories[key]
	mu.RUnlock()
	if !ok {
		return nil, errs.Newf(errs.CodeInvalidArgs, "unknown input format: %q", name).
			WithHint("Use --from " + strings.Join(FormatNames(), "|") + ", or --from auto to detect it.")
	}
	d, err := factory()
	if err != nil {
		if _, ok := err.(*errs.E); ok {
			return nil, err
		}
		return nil, errs.Newf(errs.CodeProviderUnavailable, "decoder %q: %s", key, err.Error())
	}
	return d, nil
}

// Names returns every registered decoder name, sorted.
func Names() []string {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]string, 0, len(factories))
	for k := range factories {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// FormatNames returns what --from accepts: the reserved "text" plus every
// registered decoder, in that order. It is what the flag help prints, which is
// the only place a user can discover which formats a build supports.
func FormatNames() []string {
	return append([]string{FormatText}, Names()...)
}

// Registered reports whether a decoder name is known.
func Registered(name string) bool {
	key := strings.ToLower(strings.TrimSpace(name))
	if key == FormatText {
		return true
	}
	mu.RLock()
	defer mu.RUnlock()
	_, ok := factories[key]
	return ok
}

// Extensions returns every extension any decoder claims, sorted. Used in error
// hints, so a user who fed us a .doc is told what would have worked.
func Extensions() []string {
	var out []string
	for _, name := range Names() {
		d, err := Open(name)
		if err != nil {
			continue
		}
		out = append(out, d.Extensions()...)
	}
	sort.Strings(out)
	return out
}

// ForFile names the decoder that should read a file, or FormatText if the
// bytes should be treated as text and handed straight to the strip pass.
//
// The extension wins over the magic number, because the extension is what the
// user typed and the sniff is only a guess — and because the zip-based formats
// (DOCX, PPTX, ODT, EPUB) share one magic number and are told apart only by
// looking inside. Sniffing is the fallback for stdin, which has no name, and
// for a file whose extension is missing or wrong. Iteration is in sorted name
// order so two decoders that both claim a file resolve the same way on every
// run rather than depending on map ordering.
//
// path may be empty (stdin). data may be a prefix of the file, but a short
// prefix weakens sniffing for the zip formats, which need the central
// directory at the end.
func ForFile(path string, data []byte) string {
	if ext := strings.ToLower(filepath.Ext(path)); ext != "" {
		for _, name := range Names() {
			d, err := Open(name)
			if err != nil {
				continue
			}
			for _, e := range d.Extensions() {
				if e == ext {
					return name
				}
			}
		}
	}
	for _, name := range Names() {
		d, err := Open(name)
		if err != nil {
			continue
		}
		if d.Sniff(data) {
			return name
		}
	}
	return FormatText
}

// Decode reads one file with the named decoder. A name of "" or "auto"
// resolves through ForFile; FormatText returns nil, meaning "not a document,
// use the bytes as they are".
//
// Returning (nil, nil) rather than a Document wrapping the raw bytes is
// deliberate: plain text keeps the streaming path in internal/input, including
// its --max-bytes truncation, and only a real decode pulls a whole file into
// memory.
func Decode(format, path string, data []byte) (*Document, error) {
	name := strings.ToLower(strings.TrimSpace(format))
	switch name {
	case "", "auto":
		name = ForFile(path, data)
	}
	if name == FormatText {
		return nil, nil
	}
	d, err := Open(name)
	if err != nil {
		return nil, err
	}
	out, err := d.Decode(data)
	if err != nil {
		return nil, err
	}
	if out == nil {
		return nil, errs.Newf(errs.CodeEmptyInput, "%s decoder returned nothing", name)
	}
	out.Format = d.Name()
	if strings.TrimSpace(out.Content) == "" {
		return nil, EmptyErr(d.Name(), path)
	}
	return out, nil
}

// EmptyErr is the shared error for a file that decoded cleanly but holds no
// text. It is its own code because the cause is almost always the same one —
// a scan, or a deck of images — and the fix is never "try again".
func EmptyErr(format, path string) *errs.E {
	where := "the file"
	if path != "" {
		where = path
	}
	return errs.Newf(errs.CodeEmptyInput, "%s decoded as %s but contains no text", where, format).
		WithHint("A scanned or image-only document. It needs OCR, which this CLI does not do.")
}

// ---------------------------------------------------------------------------
// Binary detection
// ---------------------------------------------------------------------------

// legacyOffice is the OLE2 compound-file magic shared by .doc, .xls and .ppt —
// the pre-2007 Office formats. They are detected but not decoded: recognising
// them is what lets the error say "convert it to .docx" instead of "this file
// is not text".
var legacyOffice = []byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}

// sniffBinaryBytes is how much of a file the binary tests read. It is a prefix
// rather than the whole file because these run on every input, including the
// text ones, and because a binary that is text for its first 8 KiB does not
// exist outside a deliberately crafted file.
const sniffBinaryBytes = 8000

// HasNulByte reports whether data contains a NUL in its first few KiB.
//
// It is the half of LooksBinary that nothing excuses. Invalid UTF-8 has an
// innocent explanation — a latin-1 file of German prose — and --from text is
// how a user says so. A NUL byte has none: no encoding this CLI can tokenize
// puts one in prose, so bytes carrying one are refused even when the user
// insisted they were text. Scoring them would produce a number, at exit 0, for
// a file that was never read.
func HasNulByte(data []byte) bool {
	return bytes.IndexByte(head(data), 0) >= 0
}

// LooksBinary reports whether data is something no tokenizer should ever see.
//
// It exists so that a file no decoder claims fails loudly. Scoring the reading
// ease of a JPEG is not a rounding error; it is a number with no meaning
// attached to a file the user believed was analysed. A NUL byte in the first
// few KiB, or invalid UTF-8, is the test: both are effectively impossible in
// the prose this CLI reads and unavoidable in the binaries it does not.
func LooksBinary(data []byte) bool {
	h := head(data)
	if len(h) == 0 {
		return false
	}
	if bytes.IndexByte(h, 0) >= 0 {
		return true
	}
	if utf8.Valid(h) {
		return false
	}
	// Only a prefix that was actually cut can end mid-rune, and only the last
	// three bytes of it can. Trimming unconditionally would forgive a file
	// whose one invalid byte happens to be its last, which is a real file and
	// not a truncation artefact.
	if len(data) <= sniffBinaryBytes {
		return true
	}
	for i := 0; i < 3 && len(h) > 0; i++ {
		h = h[:len(h)-1]
		if utf8.Valid(h) {
			return false
		}
	}
	return true
}

// head returns the prefix the binary tests look at.
func head(data []byte) []byte {
	if len(data) > sniffBinaryBytes {
		return data[:sniffBinaryBytes]
	}
	return data
}

// UnsupportedErr explains a file that is binary and that no decoder claims.
//
// The legacy-Office case is called out by name because it is the one a user
// hits by accident — a .doc from 2003 looks exactly like a .docx to everyone
// except the parser — and because the fix is one "Save As" away.
func UnsupportedErr(path string, data []byte) *errs.E {
	where := "the input"
	if path != "" {
		where = path
	}
	if bytes.HasPrefix(data, legacyOffice) {
		return errs.Newf(errs.CodeInvalidArgs, "%s is a pre-2007 Office file (.doc/.xls/.ppt)", where).
			WithHint("Open it and save as .docx, .xlsx, or .pptx, which this CLI reads.")
	}
	return errs.Newf(errs.CodeInvalidArgs, "%s is binary, not text", where).
		WithHint("Readable document formats: " + strings.Join(Extensions(), " ") + ". Use --from to force one.")
}
