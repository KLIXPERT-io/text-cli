// Package input resolves where the text to analyse comes from: positional
// arguments, a file, or stdin.
//
// The precedence is fixed and the same for every command, so a pipeline author
// never has to remember which flag a given subcommand honors:
//
//  1. --file <path>   (a path, or "-" for stdin)
//  2. positional args (joined with a space — one document)
//  3. stdin, when it is a pipe or redirect
//
// A terminal stdin with no args is an error rather than a hang: the CLI never
// silently waits for a human to type.
package input

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// DefaultMaxBytes caps a single document at 10 MiB. The Cloud Natural Language
// API rejects far smaller payloads, and an accidental `text entities < video.mp4`
// should fail fast rather than stream a gigabyte into memory.
const DefaultMaxBytes int64 = 10 << 20

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
// "text" field for JSONL, and the default size cap.
type Options struct {
	Args      []string
	File      string
	Format    Format
	TextField string
	IDField   string
	MaxBytes  int64
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
	Fields map[string]any `json:"fields,omitempty"`
	// Truncated marks a document cut short by MaxBytes.
	Truncated bool `json:"truncated,omitempty"`
}

// Load resolves the input source and returns one or more documents.
func Load(opts Options) ([]Item, error) {
	if opts.MaxBytes <= 0 {
		opts.MaxBytes = DefaultMaxBytes
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

	r, err := opts.reader()
	if err != nil {
		return nil, err
	}
	if c, ok := r.(io.Closer); ok && opts.File != "" && opts.File != "-" {
		defer c.Close()
	}

	switch opts.Format {
	case FormatText:
		return loadWhole(r, opts)
	case FormatLines:
		return loadLines(r, opts)
	case FormatJSONL:
		return loadJSONL(r, opts)
	default:
		return nil, errs.Newf(errs.CodeInvalidArgs, "unknown input format: %q", opts.Format).
			WithHint("Use --input-format text, lines, or jsonl.")
	}
}

// reader picks the source per the documented precedence.
func (o Options) reader() (io.Reader, error) {
	if o.File != "" && o.File != "-" {
		f, err := os.Open(o.File)
		if err != nil {
			if os.IsNotExist(err) {
				return nil, errs.Newf(errs.CodeNotFound, "no such file: %s", o.File)
			}
			return nil, errs.New(errs.CodeInvalidArgs, err.Error())
		}
		return f, nil
	}
	if o.File == "" && len(o.Args) > 0 {
		return strings.NewReader(strings.Join(o.Args, " ")), nil
	}
	if !o.stdinIsPipe() {
		return nil, errs.New(errs.CodeEmptyInput, "no input").
			WithHint(`Pipe text in ("cat file.md | text ..."), pass --file <path>, or give the text as an argument.`)
	}
	return o.stdin(), nil
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

func loadWhole(r io.Reader, opts Options) ([]Item, error) {
	text, truncated, err := readCapped(r, opts.MaxBytes)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(text) == "" {
		return nil, emptyErr()
	}
	return []Item{{ID: "0", Text: text, Truncated: truncated}}, nil
}

func loadLines(r io.Reader, opts Options) ([]Item, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), int(opts.MaxBytes))
	var items []Item
	for i := 0; sc.Scan(); i++ {
		line := sc.Text()
		if strings.TrimSpace(line) == "" {
			continue
		}
		items = append(items, Item{ID: strconv.Itoa(len(items)), Text: line})
	}
	if err := sc.Err(); err != nil {
		return nil, errs.New(errs.CodeInvalidArgs, "read input: "+err.Error())
	}
	if len(items) == 0 {
		return nil, emptyErr()
	}
	return items, nil
}

func loadJSONL(r io.Reader, opts Options) ([]Item, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), int(opts.MaxBytes))
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
		raw, ok := obj[opts.TextField]
		if !ok {
			return nil, errs.Newf(errs.CodeInvalidArgs, "line %d has no %q field", lineNo, opts.TextField).
				WithHint("Point --text-field at the field holding the text.")
		}
		text, ok := raw.(string)
		if !ok {
			return nil, errs.Newf(errs.CodeInvalidArgs, "line %d: field %q is not a string", lineNo, opts.TextField)
		}
		id := strconv.Itoa(len(items))
		if v, ok := obj[opts.IDField]; ok {
			id = fmt.Sprintf("%v", v)
		}
		fields := make(map[string]any, len(obj))
		for k, v := range obj {
			if k == opts.TextField {
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
	if len(items) == 0 {
		return nil, emptyErr()
	}
	return items, nil
}

// readCapped reads at most max bytes and reports whether it stopped early.
func readCapped(r io.Reader, max int64) (string, bool, error) {
	var sb strings.Builder
	n, err := io.Copy(&sb, io.LimitReader(r, max))
	if err != nil {
		return "", false, errs.New(errs.CodeInvalidArgs, "read input: "+err.Error())
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

func emptyErr() *errs.E {
	return errs.New(errs.CodeEmptyInput, "input contained no text").
		WithHint("Check the file or the upstream command in your pipeline.")
}
