// Package input resolves where the text to analyse comes from: positional
// arguments, one or more files, or stdin.
//
// The precedence is fixed and the same for every command, so a pipeline author
// never has to remember which flag a given subcommand honors:
//
//  1. --file <path>   (repeatable; a path, or "-" for stdin)
//  2. positional args (joined with a space — one document)
//  3. stdin, when it is a pipe or redirect
//
// A terminal stdin with no args is an error rather than a hang: the CLI never
// silently waits for a human to type.
//
// Decoding happens here too. A file that a decoder in internal/doc claims — a
// PDF, a DOCX, an EPUB — is turned into markdown before any command sees it,
// for the same reason State.LoadInput strips markup exactly once: readability,
// lint, and diff must not be able to disagree about what the document is. A
// decoder returns markdown rather than prose, so the strip pass that already
// runs over every document is what turns a heading into a terminated sentence.
package input

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/KLIXPERT-io/text-cli/internal/doc"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// DefaultMaxBytes caps a single document at 10 MiB. The Cloud Natural Language
// API rejects far smaller payloads, and an accidental `text entities < video.mp4`
// should fail fast rather than stream a gigabyte into memory.
const DefaultMaxBytes int64 = 10 << 20

// DefaultMaxFileBytes caps the bytes read from one *container* at 100 MiB.
//
// It is a second, larger ceiling because it bounds a different thing.
// --max-bytes bounds the text that gets analysed, and for plain text the two
// are the same number: the reader stops early and the document is marked
// truncated. A container cannot be cut that way — half a zip has no central
// directory and half a PDF has no xref table, so truncating one before decoding
// turns "your file is too big" into "this file is corrupt". So the whole
// container is read, refused outright above this ceiling, and --max-bytes is
// applied to the text the decoder produced.
const DefaultMaxFileBytes int64 = 100 << 20

// FromText is the --from value meaning "do not decode, these bytes are text".
// It mirrors doc.FormatText so a caller that only wants to opt out need not
// import the decoder registry.
const FromText = doc.FormatText

// FromAuto is the --from default: name the decoder from the file's extension,
// falling back to sniffing the bytes.
const FromAuto = "auto"

// sniffBytes is how much of an input is inspected before deciding whether it is
// a document or a stream of text. It is large enough for every magic number and
// for a PDF's header comment, and small enough that a text file keeps its
// streaming read: the peeked bytes stay in the bufio.Reader and are never
// copied twice.
const sniffBytes = 8192

// Format names the input encoding.
type Format string

const (
	// FormatText treats the whole input as one document.
	FormatText Format = "text"
	// FormatJSONL reads one JSON object per line and pulls the text out of a
	// named field, so batches keep their ids and any sidecar fields.
	FormatJSONL Format = "jsonl"
	// FormatLines treats every non-empty line as its own document.
	FormatLines Format = "lines"
)

// Options configures a Load call. Zero values are sensible: text format, the
// "text" field for JSONL, auto-detected document format, and the default size
// caps.
type Options struct {
	Args []string
	// Files are the paths to read, in order. "-" means stdin.
	Files []string
	// From selects the document decoder: "" or "auto" to detect it, "text" to
	// refuse decoding, or a registered name from internal/doc.
	From         string
	Format       Format
	TextField    string
	IDField      string
	MaxBytes     int64
	MaxFileBytes int64
	// Stdin is injectable for tests; nil means os.Stdin.
	Stdin io.Reader
	// StdinIsPipe overrides TTY detection in tests. nil means detect.
	StdinIsPipe *bool
}

// Item is one input document.
type Item struct {
	// ID identifies the document in batch output. Defaults to the zero-based
	// index as a string when the input carries no id.
	ID string `json:"id"`
	// Text is the document content.
	Text string `json:"text"`
	// Fields carries the other JSONL fields through to the output, so
	// `text flesch --input-format jsonl` can be joined back to its source.
	// A decoded document adds its provenance here — "file", "format",
	// "title" — the way a fetched page carries "url".
	Fields map[string]any `json:"fields,omitempty"`
	// Truncated marks a document cut short by MaxBytes.
	Truncated bool `json:"truncated,omitempty"`
}

// Load resolves the input source and returns one or more documents.
//
// Several files fail fast: the first unreadable, oversized, or empty one aborts
// the run. A batch that silently skipped a file would report on fewer documents
// than the user asked about, and nothing in the output would say so.
func Load(opts Options) ([]Item, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
	}
	if opts.MaxFileBytes <= 0 {
		opts.MaxFileBytes = DefaultMaxFileBytes
	}
	if opts.Format == "" {
		opts.Format = FormatText
	}
	if opts.TextField == "" {
		opts.TextField = "text"
	}
	if opts.IDField == "" {
		opts.IDField = "id"
	}
	switch opts.Format {
	case FormatText, FormatLines, FormatJSONL:
	default:
		return nil, errs.Newf(errs.CodeInvalidArgs, "unknown input format: %q", opts.Format).
			WithHint("Use --input-format text, lines, or jsonl.")
	}

	sources, err := opts.sources()
	if err != nil {
		return nil, err
	}

	var items []Item
	for _, src := range sources {
		got, err := opts.loadSource(src, len(sources) > 1, len(items))
		if err != nil {
			return nil, err
		}
		items = append(items, got...)
	}
	return items, nil
}

// source is one resolved input: a file, stdin, or the joined arguments.
type source struct {
	// path is the path the user gave, empty for stdin and for arguments.
	path  string
	stdin bool
	args  bool
}

// id names the source in batch output. A path is more useful than an index
// when three files are scored in one run; "stdin" reads better than "-".
func (s source) id() string {
	if s.path != "" {
		return s.path
	}
	if s.stdin {
		return "stdin"
	}
	return "0"
}

// sources picks the input sources per the documented precedence.
func (o Options) sources() ([]source, error) {
	if len(o.Files) > 0 {
		out := make([]source, 0, len(o.Files))
		for _, f := range o.Files {
			if f == "-" {
				out = append(out, source{stdin: true})
				continue
			}
			out = append(out, source{path: f})
		}
		return out, nil
	}
	if len(o.Args) > 0 {
		return []source{{args: true}}, nil
	}
	if !o.stdinIsPipe() {
		return nil, errs.New(errs.CodeEmptyInput, "no input").
			WithHint(`Pipe text in ("cat file.md | text ..."), pass --file <path>, or give the text as an argument.`)
	}
	return []source{{stdin: true}}, nil
}

// open returns a reader for one source, plus its closer when it owns a file.
func (o Options) open(src source) (io.Reader, io.Closer, error) {
	switch {
	case src.args:
		return strings.NewReader(strings.Join(o.Args, " ")), nil, nil
	case src.stdin:
		return o.stdin(), nil, nil
	}
	f, err := os.Open(src.path)
	if err != nil {
		if os.IsNotExist(err) {
			// The path is in the message because with several files "no such
			// file" alone does not say which one failed.
			return nil, nil, errs.Newf(errs.CodeNotFound, "no such file: %s", src.path)
		}
		return nil, nil, errs.New(errs.CodeInvalidArgs, err.Error())
	}
	return f, f, nil
}

func (o Options) stdin() io.Reader {
	if o.Stdin != nil {
		return o.Stdin
	}
	return os.Stdin
}

func (o Options) stdinIsPipe() bool {
	if o.StdinIsPipe != nil {
		return *o.StdinIsPipe
	}
	if o.Stdin != nil {
		return true
	}
	return StdinIsPipe()
}

// StdinIsPipe reports whether stdin is a pipe or a redirected file rather than
// a terminal.
func StdinIsPipe() bool {
	fi, err := os.Stdin.Stat()
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeCharDevice == 0
}

// loadSource reads one file, stdin, or the arguments into items.
//
// start is the number of items already loaded, so per-line ids keep counting
// across a batch of files instead of restarting at 0 in every one of them and
// colliding.
func (o Options) loadSource(src source, multi bool, start int) ([]Item, error) {
	r, closer, err := o.open(src)
	if err != nil {
		return nil, err
	}
	if closer != nil {
		defer closer.Close()
	}

	d, plain, err := o.resolve(src, r)
	if err != nil {
		return nil, err
	}
	if d != nil {
		// A decoded document is markdown in memory; the line and JSONL readers
		// below work on it exactly as they would on the file's own bytes.
		plain = strings.NewReader(d.Content)
	}

	var items []Item
	switch o.Format {
	case FormatText:
		items, err = o.loadWhole(d, plain, src)
	case FormatLines:
		items, err = o.loadLines(plain, start)
	case FormatJSONL:
		items, err = o.loadJSONL(plain, start)
	}
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, emptyErr(src.path)
	}
	o.decorate(items, src, d, multi)
	return items, nil
}

// resolve decides whether one source is a document to decode or bytes to
// stream, and returns exactly one of the two.
//
// Plain text keeps the streaming read — including its --max-bytes truncation —
// because that is the common case and the one that must not regress. Only an
// input that looks like a container is pulled fully into memory, which is also
// the only way to decode one.
func (o Options) resolve(src source, r io.Reader) (*doc.Document, io.Reader, error) {
	from := strings.ToLower(strings.TrimSpace(o.From))
	switch from {
	case FromText:
		// The explicit escape hatch: the user named the format, so the bytes go
		// through unsniffed and undecoded. It also waives the invalid-UTF-8 half
		// of the binary refusal, which is the point of the flag — a latin-1 file
		// of German prose is invalid UTF-8 and is real text somebody has a reason
		// to measure.
		//
		// It does not waive the NUL-byte half. "Treat this as text" is a claim
		// about an encoding, not a licence to tokenize a zip: without this check
		// `--from text --file report.docx` reports a reading ease for the
		// compressed bytes and exits 0, which is the exact failure this package
		// exists to prevent.
		br := bufio.NewReaderSize(r, sniffBytes)
		head, err := br.Peek(sniffBytes)
		if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
			return nil, nil, readErr(err)
		}
		if doc.HasNulByte(head) {
			return nil, nil, doc.UnsupportedErr(src.path, head)
		}
		return nil, br, nil
	case "":
		from = FromAuto
	}
	if from != FromAuto {
		// A named decoder never sniffs: forcing --from pdf on a file with the
		// wrong extension is exactly what the flag is for.
		data, err := o.readWhole(r, src.path)
		if err != nil {
			return nil, nil, err
		}
		d, err := doc.Decode(from, src.path, data)
		if err != nil {
			return nil, nil, err
		}
		return d, nil, nil
	}

	br := bufio.NewReaderSize(r, sniffBytes)
	head, err := br.Peek(sniffBytes)
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, bufio.ErrBufferFull) {
		return nil, nil, readErr(err)
	}
	// LooksBinary is part of the test, not just ForFile: a zip-based format
	// cannot be recognised from a prefix — its central directory is at the end
	// of the file — so an input that is binary but unclaimed is read in full and
	// offered to the registry again with everything it needs. If it is still
	// unclaimed after that, it is genuinely not a document.
	if doc.ForFile(src.path, head) == doc.FormatText && !doc.LooksBinary(head) {
		return nil, br, nil
	}
	data, err := o.readWhole(br, src.path)
	if err != nil {
		return nil, nil, err
	}
	d, err := doc.Decode(FromAuto, src.path, data)
	if err != nil {
		return nil, nil, err
	}
	if d != nil {
		return d, nil, nil
	}
	if doc.LooksBinary(data) {
		// Nothing claimed it and it is not text. Refusing is the point: the
		// reading ease of a JPEG is a number with no meaning attached to a file
		// the user believed was analysed.
		return nil, nil, doc.UnsupportedErr(src.path, data)
	}
	return nil, bytes.NewReader(data), nil
}

// readWhole reads one container in full, refusing anything above the file
// ceiling. See DefaultMaxFileBytes for why this limit is not --max-bytes.
func (o Options) readWhole(r io.Reader, path string) ([]byte, error) {
	max := o.MaxFileBytes
	if max <= 0 {
		max = DefaultMaxFileBytes
	}
	// One byte past the ceiling distinguishes "exactly at the limit" from
	// "truncated here", which for a container is the difference between a
	// clear error and a confusing decode failure.
	data, err := io.ReadAll(io.LimitReader(r, max+1))
	if err != nil {
		return nil, readErr(err)
	}
	if int64(len(data)) > max {
		where := "the input"
		if path != "" {
			where = path
		}
		return nil, errs.Newf(errs.CodeInvalidArgs, "%s is larger than the %d-byte document limit", where, max).
			WithHint("A document is decoded whole and cannot be streamed. Split it, or export the text and pipe that in.")
	}
	return data, nil
}

func (o Options) loadWhole(d *doc.Document, r io.Reader, src source) ([]Item, error) {
	var (
		text      string
		truncated bool
		err       error
	)
	if d != nil {
		// The container could not be truncated before decoding, so --max-bytes
		// applies to the text it produced — on a rune boundary, because a cut
		// mid-sequence hands the tokenizer an invalid byte.
		text, truncated = CapText(d.Content, o.MaxBytes)
	} else if text, truncated, err = readCapped(r, o.MaxBytes); err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, emptyErr(src.path)
	}
	return []Item{{ID: "0", Text: text, Truncated: truncated}}, nil
}

func (o Options) loadLines(r io.Reader, start int) ([]Item, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), int(o.MaxBytes))
	var items []Item
	for sc.Scan() {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		items = append(items, Item{ID: strconv.Itoa(start + len(items)), Text: line})
	}
	if err := sc.Err(); err != nil {
		return nil, errs.New(errs.CodeInvalidArgs, "read input: "+err.Error())
	}
	return items, nil
}

func (o Options) loadJSONL(r io.Reader, start int) ([]Item, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), int(o.MaxBytes))
	var items []Item
	for lineNo := 1; sc.Scan(); lineNo++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var obj map[string]any
		if err := json.Unmarshal([]byte(line), &obj); err != nil {
			return nil, errs.Newf(errs.CodeInvalidArgs, "line %d is not a JSON object: %s", lineNo, err.Error()).
				WithHint(`Each line must be a JSON object, e.g. {"id":"a","text":"..."}. Use --input-format lines for plain text.`)
		}
		raw, ok := obj[o.TextField]
		if !ok {
			return nil, errs.Newf(errs.CodeInvalidArgs, "line %d has no %q field", lineNo, o.TextField).
				WithHint("Point --text-field at the field holding the text.")
		}
		text, ok := raw.(string)
		if !ok {
			return nil, errs.Newf(errs.CodeInvalidArgs, "line %d: field %q is not a string", lineNo, o.TextField)
		}
		id := strconv.Itoa(start + len(items))
		if v, ok := obj[o.IDField]; ok {
			id = fmt.Sprintf("%v", v)
		}
		fields := make(map[string]any, len(obj))
		for k, v := range obj {
			if k == o.TextField {
				continue
			}
			fields[k] = v
		}
		if len(fields) == 0 {
			fields = nil
		}
		items = append(items, Item{ID: id, Text: text, Fields: fields})
	}
	if err := sc.Err(); err != nil {
		return nil, errs.New(errs.CodeInvalidArgs, "read input: "+err.Error()).
			WithHint("A single JSONL line may not exceed the --max-bytes limit.")
	}
	return items, nil
}

// decorate stamps identity and provenance onto one source's items.
//
// The id rule is backwards compatibility, not taste: a single document has
// always been item "0" and must stay "0", because that string is in every
// existing consumer's JSON. Only a batch of files needs a real id, and there
// the path is the only one that cannot collide.
func (o Options) decorate(items []Item, src source, d *doc.Document, multi bool) {
	for i := range items {
		if multi && o.Format == FormatText {
			items[i].ID = src.id()
		}
		if multi && src.path != "" {
			setField(&items[i], "file", src.path)
		}
		if d != nil {
			setField(&items[i], "format", d.Format)
			if d.Title != "" {
				setField(&items[i], "title", d.Title)
			}
		}
	}
}

// setField adds provenance without overwriting a field the input already
// carried: a JSONL row with its own "file" column means something to whoever
// wrote it, and this package is not entitled to redefine it.
func setField(it *Item, key string, val any) {
	if val == nil || val == "" {
		return
	}
	if it.Fields == nil {
		it.Fields = map[string]any{}
	}
	if _, exists := it.Fields[key]; exists {
		return
	}
	it.Fields[key] = val
}

// readCapped reads at most max bytes and reports whether it stopped early.
func readCapped(r io.Reader, max int64) (string, bool, error) {
	var sb strings.Builder
	n, err := io.Copy(&sb, io.LimitReader(r, max))
	if err != nil {
		return "", false, readErr(err)
	}
	truncated := false
	if n == max {
		// Peek one more byte: hitting the limit exactly is not truncation.
		var b [1]byte
		if k, _ := r.Read(b[:]); k > 0 {
			truncated = true
		}
	}
	return sb.String(), truncated, nil
}

// CapText applies a byte budget to text that is already in memory — a decoded
// document, or a fetched page — and reports whether it had to cut.
//
// It cuts on a rune boundary: text truncated mid-UTF-8-sequence would hand the
// tokenizer an invalid byte, and the resulting word count would be wrong in a
// way nothing downstream could detect.
func CapText(s string, max int64) (string, bool) {
	if max <= 0 || int64(len(s)) <= max {
		return s, false
	}
	cut := int(max)
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut], true
}

func readErr(err error) *errs.E {
	return errs.New(errs.CodeInvalidArgs, "read input: "+err.Error())
}

// emptyErr names the file when there is one: in a batch, "input contained no
// text" without a path leaves the user to bisect their own command line.
func emptyErr(path string) *errs.E {
	msg := "input contained no text"
	if path != "" {
		msg = path + " contained no text"
	}
	return errs.New(errs.CodeEmptyInput, msg).
		WithHint("Check the file or the upstream command in your pipeline.")
}
