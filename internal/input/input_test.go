package input

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/KLIXPERT-io/text-cli/internal/doc"
	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

// pipe marks stdin as piped without touching the real process stdin.
func pipe(v bool) *bool { return &v }

func codeOf(t *testing.T, err error) errs.Code {
	t.Helper()
	var e *errs.E
	if !errors.As(err, &e) {
		t.Fatalf("error is not *errs.E: %v", err)
	}
	return e.Code
}

func TestLoadFromArgs(t *testing.T) {
	items, err := Load(Options{Args: []string{"Hello", "world."}, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	// Args join with a space so `text readability Hello world.` reads naturally.
	if items[0].Text != "Hello world." {
		t.Errorf("text = %q, want %q", items[0].Text, "Hello world.")
	}
	if items[0].ID != "0" {
		t.Errorf("id = %q, want %q", items[0].ID, "0")
	}
}

func TestLoadFromFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "doc.md")
	if err := os.WriteFile(p, []byte("Der Text ist kurz."), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := Load(Options{Files: []string{p}, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "Der Text ist kurz." {
		t.Errorf("text = %q", items[0].Text)
	}
}

// A file wins over positional args, per the documented precedence.
func TestFileBeatsArgs(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "doc.txt")
	if err := os.WriteFile(p, []byte("from file"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := Load(Options{Files: []string{p}, Args: []string{"from", "args"}, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "from file" {
		t.Errorf("text = %q, want the file contents", items[0].Text)
	}
}

func TestLoadFromStdin(t *testing.T) {
	items, err := Load(Options{Stdin: strings.NewReader("piped text"), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "piped text" {
		t.Errorf("text = %q", items[0].Text)
	}
}

// "-" means stdin even when a file flag is the thing carrying it.
func TestDashMeansStdin(t *testing.T) {
	items, err := Load(Options{Files: []string{"-"}, Stdin: strings.NewReader("dash stdin"), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "dash stdin" {
		t.Errorf("text = %q", items[0].Text)
	}
}

// The CLI must never block waiting for a human to type.
func TestTerminalStdinWithNoArgsIsAnError(t *testing.T) {
	_, err := Load(Options{StdinIsPipe: pipe(false)})
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if got := codeOf(t, err); got != errs.CodeEmptyInput {
		t.Errorf("code = %q, want %q", got, errs.CodeEmptyInput)
	}
}

func TestMissingFile(t *testing.T) {
	_, err := Load(Options{Files: []string{filepath.Join(t.TempDir(), "nope.txt")}, StdinIsPipe: pipe(false)})
	if got := codeOf(t, err); got != errs.CodeNotFound {
		t.Errorf("code = %q, want %q", got, errs.CodeNotFound)
	}
}

func TestWhitespaceOnlyInputIsEmpty(t *testing.T) {
	_, err := Load(Options{Stdin: strings.NewReader("   \n\t  "), StdinIsPipe: pipe(true)})
	if got := codeOf(t, err); got != errs.CodeEmptyInput {
		t.Errorf("code = %q, want %q", got, errs.CodeEmptyInput)
	}
}

func TestLoadLines(t *testing.T) {
	in := "first line\n\nsecond line\n   \nthird line\n"
	items, err := Load(Options{Format: FormatLines, Stdin: strings.NewReader(in), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != 3 {
		t.Fatalf("got %d items, want 3 (blank lines skipped)", len(items))
	}
	want := []string{"first line", "second line", "third line"}
	for i, w := range want {
		if items[i].Text != w {
			t.Errorf("item %d = %q, want %q", i, items[i].Text, w)
		}
		if items[i].ID != string(rune('0'+i)) {
			t.Errorf("item %d id = %q, want %q", i, items[i].ID, string(rune('0'+i)))
		}
	}
}

func TestLoadJSONL(t *testing.T) {
	in := `{"id":"a1","text":"Erster Absatz.","url":"/a"}
{"id":"b2","text":"Second paragraph.","url":"/b"}
`
	items, err := Load(Options{Format: FormatJSONL, Stdin: strings.NewReader(in), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].ID != "a1" || items[0].Text != "Erster Absatz." {
		t.Errorf("item 0 = %+v", items[0])
	}
	// Sidecar fields must survive so a batch can be joined back to its source.
	if got := items[0].Fields["url"]; got != "/a" {
		t.Errorf("fields[url] = %v, want /a", got)
	}
	// The text field itself must not be duplicated into Fields.
	if _, dup := items[0].Fields["text"]; dup {
		t.Error("text field leaked into Fields")
	}
}

func TestLoadJSONLCustomFields(t *testing.T) {
	in := `{"slug":"post-1","body":"Der Inhalt."}` + "\n"
	items, err := Load(Options{
		Format: FormatJSONL, TextField: "body", IDField: "slug",
		Stdin: strings.NewReader(in), StdinIsPipe: pipe(true),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].ID != "post-1" || items[0].Text != "Der Inhalt." {
		t.Errorf("item = %+v", items[0])
	}
}

// A non-string id (a JSON number) still yields a usable id.
func TestLoadJSONLNumericID(t *testing.T) {
	items, err := Load(Options{
		Format:      FormatJSONL,
		Stdin:       strings.NewReader(`{"id":42,"text":"hi"}` + "\n"),
		StdinIsPipe: pipe(true),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].ID != "42" {
		t.Errorf("id = %q, want %q", items[0].ID, "42")
	}
}

// Without an id field, ids fall back to the row index.
func TestLoadJSONLIndexFallback(t *testing.T) {
	in := `{"text":"one"}` + "\n" + `{"text":"two"}` + "\n"
	items, err := Load(Options{Format: FormatJSONL, Stdin: strings.NewReader(in), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].ID != "0" || items[1].ID != "1" {
		t.Errorf("ids = %q, %q; want 0, 1", items[0].ID, items[1].ID)
	}
}

func TestLoadJSONLErrors(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want errs.Code
	}{
		{"not json", "this is not json\n", errs.CodeInvalidArgs},
		{"missing text field", `{"id":"a"}` + "\n", errs.CodeInvalidArgs},
		{"text not a string", `{"text":123}` + "\n", errs.CodeInvalidArgs},
		{"no rows", "\n\n", errs.CodeEmptyInput},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(Options{Format: FormatJSONL, Stdin: strings.NewReader(tt.in), StdinIsPipe: pipe(true)})
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if got := codeOf(t, err); got != tt.want {
				t.Errorf("code = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestUnknownFormat(t *testing.T) {
	_, err := Load(Options{Format: "yaml", Stdin: strings.NewReader("x"), StdinIsPipe: pipe(true)})
	if got := codeOf(t, err); got != errs.CodeInvalidArgs {
		t.Errorf("code = %q, want %q", got, errs.CodeInvalidArgs)
	}
}

func TestTruncation(t *testing.T) {
	items, err := Load(Options{
		Stdin: strings.NewReader("abcdefghij"), StdinIsPipe: pipe(true), MaxBytes: 4,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "abcd" {
		t.Errorf("text = %q, want %q", items[0].Text, "abcd")
	}
	if !items[0].Truncated {
		t.Error("Truncated = false, want true")
	}
}

// Input that exactly fills the cap is not truncated — an off-by-one here would
// mark every exactly-sized document as incomplete.
func TestExactSizeIsNotTruncated(t *testing.T) {
	items, err := Load(Options{
		Stdin: strings.NewReader("abcd"), StdinIsPipe: pipe(true), MaxBytes: 4,
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Truncated {
		t.Error("Truncated = true, want false")
	}
}

func TestUTF8Preserved(t *testing.T) {
	const s = "Größe, Straße und Weiß — käme das durch?"
	items, err := Load(Options{Stdin: strings.NewReader(s), StdinIsPipe: pipe(true)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != s {
		t.Errorf("text = %q, want %q", items[0].Text, s)
	}
}

// ---------------------------------------------------------------------------
// Document decoding
// ---------------------------------------------------------------------------

// The wiring between internal/input and internal/doc is tested against a fake
// decoder rather than a real format, so these cases pin the *layer* — sniffing,
// forcing, size ceilings, provenance — and keep passing whichever real decoders
// a build happens to contain.
const fakeFormat = "fakedoc"

// The magic ends in a NUL so a fakedoc also exercises the binary path: an
// unclaimed input that looks like this must be refused, and a claimed one must
// be decoded rather than tokenized.
var fakeMagic = []byte("FAKEDOC\x00")

func init() { doc.Register(fakeFormat, func() (doc.Decoder, error) { return fakeDecoder{}, nil }) }

type fakeDecoder struct{}

func (fakeDecoder) Name() string           { return fakeFormat }
func (fakeDecoder) Extensions() []string   { return []string{".fake"} }
func (fakeDecoder) Sniff(data []byte) bool { return bytes.HasPrefix(data, fakeMagic) }
func (fakeDecoder) Decode(data []byte) (*doc.Document, error) {
	if !bytes.HasPrefix(data, fakeMagic) {
		return nil, errs.New(errs.CodeInvalidArgs, "not a "+fakeFormat+" file")
	}
	d := &doc.Document{}
	var blocks []string
	for _, line := range strings.Split(strings.TrimSpace(string(data[len(fakeMagic):])), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case line == "":
		case strings.HasPrefix(line, "title:"):
			d.Title = strings.TrimSpace(strings.TrimPrefix(line, "title:"))
		default:
			blocks = append(blocks, line)
		}
	}
	if len(blocks) > 0 {
		d.Content = strings.Join(blocks, "\n\n") + "\n"
	}
	return d, nil
}

// fakeDoc builds the bytes of a fakedoc. A block beginning with "#" is written
// through as a heading, which is what lets a test assert that a decoder's
// markdown survives the input layer instead of being flattened.
func fakeDoc(title string, blocks ...string) []byte {
	var b bytes.Buffer
	b.Write(fakeMagic)
	if title != "" {
		b.WriteString("title: " + title + "\n")
	}
	for _, bl := range blocks {
		b.WriteString(bl + "\n")
	}
	return b.Bytes()
}

func writeTemp(t *testing.T, name string, data []byte) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(p, data, 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// A decoded document must reach the caller as markdown: the strip pass
// downstream is what turns "## Rollout" into a terminated sentence, and it can
// only do that for a heading that survived the decode.
func TestDecodedDocumentKeepsMarkdown(t *testing.T) {
	p := writeTemp(t, "report.fake", fakeDoc("Quarterly Report", "## Rollout", "Es geht voran."))
	items, err := Load(Options{Files: []string{p}, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	if !strings.Contains(items[0].Text, "## Rollout") {
		t.Errorf("text = %q, want the heading to survive as a heading", items[0].Text)
	}
	// A lone document keeps id "0" — today's single-file JSON must not change.
	if items[0].ID != "0" {
		t.Errorf("id = %q, want %q", items[0].ID, "0")
	}
	if got := items[0].Fields["format"]; got != fakeFormat {
		t.Errorf("fields[format] = %v, want %q", got, fakeFormat)
	}
	if got := items[0].Fields["title"]; got != "Quarterly Report" {
		t.Errorf("fields[title] = %v", got)
	}
	// One file, so there is nothing to disambiguate and no "file" field.
	if _, ok := items[0].Fields["file"]; ok {
		t.Error("fields[file] set for a single input")
	}
}

// Plain text is untouched by the decode pass — no decoder claims it, and its
// streaming read (and its truncation) must behave exactly as before.
func TestPlainTextIsNotDecoded(t *testing.T) {
	p := writeTemp(t, "post.md", []byte("# Titel\n\nEin Satz."))
	items, err := Load(Options{Files: []string{p}, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if items[0].Text != "# Titel\n\nEin Satz." {
		t.Errorf("text = %q, want the file byte for byte", items[0].Text)
	}
	if items[0].Fields != nil {
		t.Errorf("fields = %v, want none for plain text", items[0].Fields)
	}
}

func TestMultipleFiles(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.fake")
	if err := os.WriteFile(a, []byte("Erster Text."), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, fakeDoc("B", "Zweiter Text."), 0o600); err != nil {
		t.Fatal(err)
	}

	items, err := Load(Options{Files: []string{a, b}, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	// Indices would collide across files, so several files are identified by
	// path — and carry it as a field the way a fetched page carries its URL.
	if items[0].ID != a || items[1].ID != b {
		t.Errorf("ids = %q, %q; want %q, %q", items[0].ID, items[1].ID, a, b)
	}
	if got := items[0].Fields["file"]; got != a {
		t.Errorf("fields[file] = %v, want %q", got, a)
	}
	if items[0].Text != "Erster Text." || !strings.Contains(items[1].Text, "Zweiter Text.") {
		t.Errorf("texts = %q, %q", items[0].Text, items[1].Text)
	}
	// Files are read in the order they were given, not in any sorted order.
	if !strings.Contains(items[1].Text, "Zweiter") {
		t.Error("file order not preserved")
	}
}

// Per-line ids keep counting across a batch: restarting at 0 in every file
// would hand two documents the same id in one NDJSON stream.
func TestLinesAcrossFilesKeepUniqueIDs(t *testing.T) {
	dir := t.TempDir()
	a := filepath.Join(dir, "a.txt")
	b := filepath.Join(dir, "b.txt")
	if err := os.WriteFile(a, []byte("one\ntwo\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(b, []byte("three\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	items, err := Load(Options{Files: []string{a, b}, Format: FormatLines, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := []struct{ id, text string }{{"0", "one"}, {"1", "two"}, {"2", "three"}}
	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d", len(items), len(want))
	}
	for i, w := range want {
		if items[i].ID != w.id || items[i].Text != w.text {
			t.Errorf("item %d = %+v, want %v", i, items[i], w)
		}
	}
}

// A batch fails fast, and the message names the file that failed: "no such
// file" alone leaves the user to bisect their own command line.
func TestMissingFileInBatchNamesThePath(t *testing.T) {
	dir := t.TempDir()
	ok := filepath.Join(dir, "ok.txt")
	if err := os.WriteFile(ok, []byte("fine"), 0o600); err != nil {
		t.Fatal(err)
	}
	missing := filepath.Join(dir, "gone.txt")
	_, err := Load(Options{Files: []string{ok, missing}, StdinIsPipe: pipe(false)})
	if got := codeOf(t, err); got != errs.CodeNotFound {
		t.Fatalf("code = %q, want %q", got, errs.CodeNotFound)
	}
	if !strings.Contains(err.Error(), missing) {
		t.Errorf("error = %q, want it to name %q", err.Error(), missing)
	}
}

func TestBinaryInputIsRefused(t *testing.T) {
	tests := []struct {
		name string
		data []byte
		want errs.Code
	}{
		// Scoring the reading ease of a JPEG is a number with no meaning
		// attached to a file the user believed was analysed.
		{"jpeg", append([]byte{0xFF, 0xD8, 0xFF, 0xE0}, make([]byte, 64)...), errs.CodeInvalidArgs},
		{"legacy office", append([]byte{0xD0, 0xCF, 0x11, 0xE0, 0xA1, 0xB1, 0x1A, 0xE1}, make([]byte, 64)...), errs.CodeInvalidArgs},
		// Decodes cleanly, holds no text: a scan, and OCR is not this CLI's job.
		{"document with no text", fakeDoc("Empty"), errs.CodeEmptyInput},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTemp(t, "input.bin", tc.data)
			_, err := Load(Options{Files: []string{p}, StdinIsPipe: pipe(false)})
			if err == nil {
				t.Fatal("want an error, got nil")
			}
			if got := codeOf(t, err); got != tc.want {
				t.Errorf("code = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestFrom(t *testing.T) {
	tests := []struct {
		name     string
		from     string
		file     string
		data     []byte
		wantErr  errs.Code
		wantText string
	}{
		{
			// The extension lies; --from is how a user overrules it.
			name: "forces a decoder past a wrong extension", from: fakeFormat,
			file: "notes.txt", data: fakeDoc("", "Trotzdem dekodiert."),
			wantText: "Trotzdem dekodiert.\n",
		},
		{
			// The escape hatch: the user named the format, so the bytes go
			// through undecoded and unsniffed.
			name: "text refuses to decode", from: FromText,
			file: "report.fake", data: []byte("roh und ungelesen"),
			wantText: "roh und ungelesen",
		},
		{
			// Invalid UTF-8 is what --from text is for: a latin-1 file of German
			// prose is not binary, it is text in an encoding this CLI does not
			// otherwise guess.
			name: "text waives the invalid-UTF-8 refusal", from: FromText,
			file: "latin1.txt", data: []byte("Gr\xfc\xdfe aus Wien."),
			wantText: "Gr\xfc\xdfe aus Wien.",
		},
		{
			// ...but it is a claim about an encoding, not a licence to tokenize a
			// container. Without this, --from text on a document reports a reading
			// ease for its compressed bytes and exits 0.
			name: "text still refuses a NUL byte", from: FromText,
			file: "report.fake", data: fakeDoc("", "roh"),
			wantErr: errs.CodeInvalidArgs,
		},
		{
			name: "auto sniffs a wrongly named file", from: FromAuto,
			file: "notes.txt", data: fakeDoc("", "Gesnifft."),
			wantText: "Gesnifft.\n",
		},
		{
			name: "unknown name is rejected", from: "wordperfect",
			file: "notes.txt", data: []byte("plain"),
			wantErr: errs.CodeInvalidArgs,
		},
		{
			// A file that is not the format it was forced to be.
			name: "forced decoder that cannot read the file", from: fakeFormat,
			file: "notes.txt", data: []byte("not a fakedoc at all"),
			wantErr: errs.CodeInvalidArgs,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := writeTemp(t, tc.file, tc.data)
			items, err := Load(Options{Files: []string{p}, From: tc.from, StdinIsPipe: pipe(false)})
			if tc.wantErr != "" {
				if err == nil {
					t.Fatal("want an error, got nil")
				}
				if got := codeOf(t, err); got != tc.wantErr {
					t.Errorf("code = %q, want %q", got, tc.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if items[0].Text != tc.wantText {
				t.Errorf("text = %q, want %q", items[0].Text, tc.wantText)
			}
		})
	}
}

// Stdin has no filename, so it is sniffed: `cat report.pdf | text readability`
// must work.
func TestStdinSniffsADocument(t *testing.T) {
	items, err := Load(Options{
		Stdin:       bytes.NewReader(fakeDoc("Piped", "## Kapitel", "Ein Satz.")),
		StdinIsPipe: pipe(true),
	})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !strings.Contains(items[0].Text, "## Kapitel") {
		t.Errorf("text = %q, want decoded markdown", items[0].Text)
	}
	if got := items[0].Fields["format"]; got != fakeFormat {
		t.Errorf("fields[format] = %v, want %q", got, fakeFormat)
	}
	// Piped, not a file: there is no path to record.
	if _, ok := items[0].Fields["file"]; ok {
		t.Error("fields[file] set for stdin")
	}
}

// A document larger than the container ceiling is refused rather than read:
// half a container decodes to garbage, so it cannot be truncated the way text
// can. The error names the limit.
func TestContainerCeiling(t *testing.T) {
	big := fakeDoc("Big", strings.Repeat("a", 4096))
	_, err := Load(Options{
		Stdin: bytes.NewReader(big), StdinIsPipe: pipe(true), MaxFileBytes: 128,
	})
	if err == nil {
		t.Fatal("want an error, got nil")
	}
	if got := codeOf(t, err); got != errs.CodeInvalidArgs {
		t.Errorf("code = %q, want %q", got, errs.CodeInvalidArgs)
	}
	if !strings.Contains(err.Error(), "128") {
		t.Errorf("error = %q, want it to name the limit", err.Error())
	}
}

// --max-bytes keeps its meaning for a document: it bounds the analysed text,
// and it is applied after the decode, on a rune boundary.
func TestMaxBytesAppliesToDecodedText(t *testing.T) {
	p := writeTemp(t, "long.fake", fakeDoc("", "Größe und Straße und Weiß."))
	items, err := Load(Options{Files: []string{p}, MaxBytes: 7, StdinIsPipe: pipe(false)})
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !items[0].Truncated {
		t.Error("Truncated = false, want true")
	}
	// "Größe" is 6 bytes: a cut at 7 lands inside nothing, but a cut inside the
	// "ö" must move back rather than emit an invalid byte.
	if !utf8.ValidString(items[0].Text) {
		t.Errorf("text = %q is not valid UTF-8", items[0].Text)
	}
	if !strings.HasPrefix("Größe und Straße und Weiß.", items[0].Text) {
		t.Errorf("text = %q, want a prefix of the document", items[0].Text)
	}
}
