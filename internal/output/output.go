// Package output handles JSON / TOON / NDJSON / CSV / table rendering.
//
// JSON is the default when stdout is not a terminal, so `text flesch < a.md |
// jq` works with no flags. NDJSON exists for the batch case: one result object
// per line, flushed as it is produced, so a long pipeline streams. TOON encodes
// the same envelope as JSON in a far cheaper form for feeding to an LLM; see
// toon.go.
package output

import (
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strconv"
	"time"

	"golang.org/x/term"
)

type Format string

const (
	FormatJSON   Format = "json"
	FormatTOON   Format = "toon"
	FormatNDJSON Format = "ndjson"
	FormatCSV    Format = "csv"
	FormatTable  Format = "table"
	FormatText   Format = "text"
)

// Valid reports whether s names a supported format.
func Valid(s string) bool {
	switch Format(s) {
	case FormatJSON, FormatTOON, FormatNDJSON, FormatCSV, FormatTable, FormatText:
		return true
	}
	return false
}

// Meta is the envelope metadata included on every JSON response.
type Meta struct {
	Cached          bool   `json:"cached"`
	CachedAt        string `json:"cached_at,omitempty"` // RFC3339 or empty
	TTLRemainingSec *int   `json:"ttl_remaining_sec"`
	APICalls        int    `json:"api_calls"`
	// Language the analysis ran in, and whether it was detected rather than
	// given. An agent that sees detected=true should treat a surprising score
	// as possibly a detection miss.
	Language         string `json:"language,omitempty"`
	LanguageDetected bool   `json:"language_detected,omitempty"`
	// Provider names the backend for commands that call one (e.g. "google").
	Provider string `json:"provider,omitempty"`
	// Documents is the number of input documents processed.
	Documents int `json:"documents,omitempty"`
	// Truncated marks input that hit a size limit.
	Truncated bool `json:"truncated,omitempty"`
}

type Envelope struct {
	Data any  `json:"data"`
	Meta Meta `json:"meta"`
}

// ResolveFormat returns the requested format or auto-detects based on TTY.
func ResolveFormat(flag string, stdoutFd uintptr) Format {
	if flag != "" {
		return Format(flag)
	}
	if term.IsTerminal(int(stdoutFd)) {
		return FormatTable
	}
	return FormatJSON
}

// IsTTY reports whether fd is a terminal.
func IsTTY(fd uintptr) bool { return term.IsTerminal(int(fd)) }

// WriteJSON writes the envelope as pretty JSON.
func WriteJSON(w io.Writer, data any, meta Meta) error {
	env := Envelope{Data: data, Meta: meta}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(env)
}

// WriteNDJSON writes one compact JSON object per line. Records are written in
// order and each line is a complete document, so a consumer can process the
// stream without waiting for the run to finish.
func WriteNDJSON(w io.Writer, records []any) error {
	enc := json.NewEncoder(w)
	for _, r := range records {
		if err := enc.Encode(r); err != nil {
			return err
		}
	}
	return nil
}

// Row is a generic row for CSV/Table rendering.
type Row = map[string]any

// WriteCSV writes rows with a header row in the given column order.
func WriteCSV(w io.Writer, columns []string, rows []Row) error {
	cw := csv.NewWriter(w)
	if err := cw.Write(columns); err != nil {
		return err
	}
	for _, r := range rows {
		rec := make([]string, len(columns))
		for i, c := range columns {
			rec[i] = fmtCell(r[c])
		}
		if err := cw.Write(rec); err != nil {
			return err
		}
	}
	cw.Flush()
	return cw.Error()
}

// WriteTable writes a simple aligned table.
func WriteTable(w io.Writer, columns []string, rows []Row) error {
	widths := make([]int, len(columns))
	for i, c := range columns {
		widths[i] = runeLen(c)
	}
	data := make([][]string, len(rows))
	for i, r := range rows {
		rec := make([]string, len(columns))
		for j, c := range columns {
			s := fmtCell(r[c])
			rec[j] = s
			if n := runeLen(s); n > widths[j] {
				widths[j] = n
			}
		}
		data[i] = rec
	}
	writeRow(w, columns, widths)
	sep := make([]string, len(columns))
	for i, wd := range widths {
		sep[i] = pad("", wd, '-')
	}
	writeRow(w, sep, widths)
	for _, rec := range data {
		writeRow(w, rec, widths)
	}
	return nil
}

func writeRow(w io.Writer, rec []string, widths []int) {
	for i, s := range rec {
		if i > 0 {
			fmt.Fprint(w, "  ")
		}
		fmt.Fprint(w, pad(s, widths[i], ' '))
	}
	fmt.Fprintln(w)
}

// runeLen measures display width in runes; German text is full of multi-byte
// umlauts and len() would misalign every column containing one.
func runeLen(s string) int { return len([]rune(s)) }

func pad(s string, n int, ch rune) string {
	l := runeLen(s)
	if l >= n {
		return s
	}
	p := make([]rune, n-l)
	for i := range p {
		p[i] = ch
	}
	return s + string(p)
}

func fmtCell(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		return strconv.FormatFloat(float64(x), 'f', -1, 32)
	case bool:
		return strconv.FormatBool(x)
	case time.Time:
		return x.Format(time.RFC3339)
	default:
		b, _ := json.Marshal(x)
		return string(b)
	}
}

// MetaFromCache builds a Meta entry given cache info.
func MetaFromCache(cached bool, cachedAt time.Time, ttlRemaining time.Duration, apiCalls int) Meta {
	m := Meta{Cached: cached, APICalls: apiCalls}
	if cached {
		m.CachedAt = cachedAt.Format(time.RFC3339)
		sec := int(ttlRemaining.Seconds())
		m.TTLRemainingSec = &sec
	}
	return m
}

// Stdout returns os.Stdout (helper for tests).
func Stdout() io.Writer { return os.Stdout }
