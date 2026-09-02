package cmd

import (
	"fmt"
	"io"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/KLIXPERT-io/text-cli/internal/input"
	"github.com/KLIXPERT-io/text-cli/internal/output"
	"github.com/spf13/cobra"
)

// newExtractCmd builds `text extract`.
//
// It is the one command that prints a document instead of measuring one, and it
// exists because the decoders were otherwise write-only: `--file report.pdf`
// scored a PDF perfectly and gave a user no way to see the text those numbers
// were computed from. A wrong score and a bad extraction look identical from
// the outside, so the extraction has to be inspectable.
//
// It is named extract rather than read because `text fetch` already answers to
// read, for URLs.
func newExtractCmd() *cobra.Command {
	var (
		out   string
		force bool
	)

	c := &cobra.Command{
		Use:     "extract <file...>",
		Aliases: []string{"convert", "md"},
		Short:   "Print a document (PDF, DOCX, PPTX, ODT, ODS, EPUB, RTF) as markdown",
		Long: `extract decodes a document file and prints it as markdown.

It is the input side of everything else, for files, the way fetch is for URLs.
The markdown it prints is exactly what the analysis commands measure, so it is
also how you check an extraction before trusting a number computed from it:

  text extract report.pdf                     # markdown on stdout
  text extract report.pdf --out report.md     # write it to a file
  text extract report.pdf | text lint         # pipe it onwards
  text extract slides.pptx notes.docx         # several at once

Markdown is the default because the block structure is what the strip pass
needs: a heading is what keeps the sentence after it from fusing into it.
--strip auto (or markdown, html) flattens it to plain prose instead:

  text extract report.pdf --strip auto        # prose, no markup

--out writes to a path instead of stdout — a file for one document, a
directory for several, one .md each named after its source. An existing file
is never overwritten without --force.

Every other input flag works here too: --file, --from, --max-bytes, and --url
(which reads a web page through the fetch path instead).`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			s := getState(cmd)

			// Positional arguments are paths here, not text. Every analysis
			// command joins its arguments into one document; this one cannot,
			// for the same reason `text diff a.md b.md` cannot — nothing is
			// named "report.pdf slides.pptx".
			st := *s
			st.Files = append(append([]string{}, s.Files...), args...)

			items, err := st.loadRaw(cmd.Context(), nil)
			if err != nil {
				return err
			}
			// The document is printed as the decoder produced it. Stripping is
			// what the analysis commands do next, and doing it here by default
			// would hand back prose that no longer round-trips: the whole point
			// of this command is to show what they were given. An explicit
			// --strip asks for the flattened form on purpose.
			if cmd.Flags().Changed("strip") {
				st.stripItems(items)
			}

			docs := extractDocs(items, extractLabels(&st, len(items)))
			written, err := extractWrite(cmd, docs, out, force)
			if err != nil {
				return err
			}

			var data any = map[string]any{"documents": docs}
			if len(docs) == 1 {
				data = docs[0]
			}
			return emitResult(cmd, emitOpts{
				Data: data,
				Meta: output.Meta{Documents: len(docs)},
				// Markdown on stdout by default: this command's payload *is*
				// text, and a JSON envelope would break the pipe it exists to
				// feed. --output json still gives the envelope.
				DefaultFormat: output.FormatText,
				Columns:       []string{"file", "format", "title", "chars"},
				Rows:          extractRows(docs),
				Records:       extractRecords(docs),
				Text: func(w io.Writer) error {
					if written != nil {
						return extractWriteReport(w, written)
					}
					return extractWriteText(w, docs)
				},
			})
		},
	}

	c.Flags().StringVarP(&out, "out", "o", "", "write to a file, or to a directory when there are several documents")
	c.Flags().BoolVar(&force, "force", false, "overwrite an existing --out file")
	return c
}

// extractDoc is one decoded document as this command reports it.
type extractDoc struct {
	// File is the source path, or the id input assigned it ("stdin", a URL).
	File string `json:"file"`
	// Format is the decoder that read it, absent for input that was already
	// text — that absence is the honest answer, not "text".
	Format string `json:"format,omitempty"`
	Title  string `json:"title,omitempty"`
	// Content is the markdown, or prose when --strip was asked for.
	Content string `json:"content"`
	// Chars counts runes, not bytes: it is there for a human deciding whether
	// an extraction looks complete, and "1.2 MB" answers a different question
	// than "how much text is in here".
	Chars int `json:"chars"`
	// Truncated marks a document cut short by --max-bytes, so a short
	// extraction is never mistaken for a short document.
	Truncated bool `json:"truncated,omitempty"`
	// Out is where the document was written, when --out was given.
	Out string `json:"out,omitempty"`
}

// extractLabels names each document by where it came from, or returns nil when
// the items already know.
//
// input keeps a single document's id as "0" — every existing consumer's JSON
// carries that string, so it cannot change. The rule is right for a measurement
// and useless here, where "file": "0" names nothing, so the source is recovered
// alongside. It is only safe when the counts line up: --input-format lines
// turns one file into many documents, and labelling each line with the filename
// would claim they came from different places.
func extractLabels(st *State, n int) []string {
	if len(st.URLs) > 0 {
		// A fetched page is already keyed by its URL.
		return nil
	}
	if len(st.Files) == 0 {
		if n == 1 {
			return []string{"stdin"}
		}
		return nil
	}
	if len(st.Files) != n {
		return nil
	}
	out := make([]string, n)
	for i, f := range st.Files {
		if f == "" || f == "-" {
			out[i] = "stdin"
			continue
		}
		out[i] = f
	}
	return out
}

func extractDocs(items []input.Item, labels []string) []*extractDoc {
	docs := make([]*extractDoc, 0, len(items))
	for i, it := range items {
		d := &extractDoc{
			File:      it.ID,
			Content:   it.Text,
			Chars:     len([]rune(it.Text)),
			Truncated: it.Truncated,
		}
		if len(labels) == len(items) {
			d.File = labels[i]
		}
		if v, ok := it.Fields["file"].(string); ok && v != "" {
			d.File = v
		}
		if v, ok := it.Fields["format"].(string); ok {
			d.Format = v
		}
		if v, ok := it.Fields["title"].(string); ok {
			d.Title = v
		}
		docs = append(docs, d)
	}
	return docs
}

// extractWrite honours --out, returning the paths written or nil when the
// documents go to stdout.
//
// The overwrite rule is the reason this is not three lines: this is the only
// command in the CLI that creates a file on the user's disk, and a decode that
// silently replaced the notes they pointed it at would be unrecoverable. An
// existing path is refused, and --force is how a rerun says it meant it.
func extractWrite(cmd *cobra.Command, docs []*extractDoc, out string, force bool) ([]string, error) {
	if out == "" {
		return nil, nil
	}
	dir := false
	if fi, err := os.Stat(out); err == nil && fi.IsDir() {
		dir = true
	}
	if len(docs) > 1 && !dir {
		return nil, errs.Newf(errs.CodeInvalidArgs, "--out %s is one path but there are %d documents", out, len(docs)).
			WithHint("Point --out at an existing directory to write one .md per document, or extract one file at a time.")
	}

	written := make([]string, 0, len(docs))
	for _, d := range docs {
		path := out
		if dir {
			path = filepath.Join(out, extractName(d.File))
		}
		if !force {
			if _, err := os.Stat(path); err == nil {
				return nil, errs.Newf(errs.CodeInvalidArgs, "%s already exists", path).
					WithHint("Pass --force to overwrite it, or choose another --out path.")
			}
		}
		content := d.Content
		if !strings.HasSuffix(content, "\n") {
			content += "\n"
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			return nil, errs.Newf(errs.CodeInvalidArgs, "cannot write %s: %s", path, err.Error()).
				WithHint("Check the directory exists and is writable.")
		}
		d.Out = path
		written = append(written, path)
	}
	return written, nil
}

// extractName turns a source id into a .md filename.
//
// A URL has no filename, only a path that sometimes looks like one: the last
// segment is the best name available and the host is the fallback for a bare
// domain. Neither is guaranteed unique, and that is deliberate — two sources
// that want the same name collide on the overwrite check, where the user sees
// it, rather than being silently numbered apart.
func extractName(id string) string {
	if u, err := url.Parse(id); err == nil && u.Scheme != "" && u.Host != "" {
		seg := path.Base(strings.TrimSuffix(u.Path, "/"))
		if seg == "" || seg == "." || seg == "/" {
			// A bare domain. Its dot is part of the name, not an extension:
			// trimming it would file example.com under "example".
			return u.Host + ".md"
		}
		return strings.TrimSuffix(seg, path.Ext(seg)) + ".md"
	}
	base := filepath.Base(id)
	if base == "" || base == "." || base == string(filepath.Separator) {
		return "document.md"
	}
	return strings.TrimSuffix(base, filepath.Ext(base)) + ".md"
}

// extractWriteText prints the documents themselves.
//
// Nothing decorates them — no filename banners, no rules — because this output
// is meant to be piped into the next command, and a banner would be scored as
// prose by whatever reads it. The provenance is in --output json.
func extractWriteText(w io.Writer, docs []*extractDoc) error {
	for i, d := range docs {
		if i > 0 {
			if _, err := io.WriteString(w, "\n"); err != nil {
				return err
			}
		}
		s := d.Content
		if !strings.HasSuffix(s, "\n") {
			s += "\n"
		}
		if _, err := io.WriteString(w, s); err != nil {
			return err
		}
	}
	return nil
}

// extractWriteReport is what --out prints instead of the document: the document
// went to the file, so stdout says where.
func extractWriteReport(w io.Writer, written []string) error {
	for _, p := range written {
		if _, err := fmt.Fprintf(w, "wrote %s\n", p); err != nil {
			return err
		}
	}
	return nil
}

func extractRows(docs []*extractDoc) []output.Row {
	rows := make([]output.Row, 0, len(docs))
	for _, d := range docs {
		rows = append(rows, output.Row{
			"file":   d.File,
			"format": d.Format,
			"title":  d.Title,
			"chars":  d.Chars,
		})
	}
	return rows
}

func extractRecords(docs []*extractDoc) []any {
	recs := make([]any, 0, len(docs))
	for _, d := range docs {
		recs = append(recs, d)
	}
	return recs
}
