package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"
)

// The envelope is contractual: consumers index data/meta and read meta.cached
// and meta.ttl_remaining_sec, which must be present-but-null on a fresh call.
func TestWriteJSONEnvelope(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteJSON(&buf, map[string]any{"score": 61.2}, Meta{Language: "en", Documents: 1}); err != nil {
		t.Fatalf("WriteJSON: %v", err)
	}

	var got map[string]any
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v", err)
	}
	if _, ok := got["data"]; !ok {
		t.Error("missing data key")
	}
	meta, ok := got["meta"].(map[string]any)
	if !ok {
		t.Fatal("missing meta object")
	}
	if v, present := meta["ttl_remaining_sec"]; !present || v != nil {
		t.Errorf("ttl_remaining_sec = %v (present=%v), want an explicit null", v, present)
	}
	if meta["cached"] != false {
		t.Errorf("cached = %v, want false", meta["cached"])
	}
}

func TestWriteNDJSONOnePerLine(t *testing.T) {
	var buf bytes.Buffer
	records := []any{
		map[string]any{"id": "a", "flesch": 61.2},
		map[string]any{"id": "b", "flesch": 30.4},
	}
	if err := WriteNDJSON(&buf, records); err != nil {
		t.Fatalf("WriteNDJSON: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 2 {
		t.Fatalf("got %d lines, want 2", len(lines))
	}
	for i, l := range lines {
		var obj map[string]any
		if err := json.Unmarshal([]byte(l), &obj); err != nil {
			t.Errorf("line %d is not standalone JSON: %v", i, err)
		}
	}
}

func TestWriteCSV(t *testing.T) {
	var buf bytes.Buffer
	err := WriteCSV(&buf, []string{"id", "flesch"}, []Row{
		{"id": "a", "flesch": 61.2},
		{"id": "b", "flesch": nil}, // a missing metric renders as an empty cell
	})
	if err != nil {
		t.Fatalf("WriteCSV: %v", err)
	}
	want := "id,flesch\na,61.2\nb,\n"
	if buf.String() != want {
		t.Errorf("got:\n%q\nwant:\n%q", buf.String(), want)
	}
}

// Column alignment must be measured in runes: German text is full of umlauts,
// and byte-length padding would visibly skew every table containing one.
func TestWriteTableAlignsUmlauts(t *testing.T) {
	var buf bytes.Buffer
	err := WriteTable(&buf, []string{"wort", "n"}, []Row{
		{"wort": "Größe", "n": 2},
		{"wort": "Haus", "n": 1},
	})
	if err != nil {
		t.Fatalf("WriteTable: %v", err)
	}
	lines := strings.Split(strings.TrimRight(buf.String(), "\n"), "\n")
	if len(lines) != 4 {
		t.Fatalf("got %d lines, want header+sep+2 rows", len(lines))
	}
	// "Größe" is 5 runes but 6 bytes. Byte-based padding would push the second
	// column of the umlaut row one place left of the others, so assert that
	// every row's last column begins at the same *rune* offset.
	offsets := make([]int, 0, len(lines))
	for _, l := range lines {
		r := []rune(l)
		// The value of the final column is the last non-space run.
		i := len(r)
		for i > 0 && r[i-1] != ' ' {
			i--
		}
		offsets = append(offsets, i)
	}
	for i, off := range offsets {
		if off != offsets[0] {
			t.Errorf("line %d starts its last column at rune %d, want %d (%q)", i, off, offsets[0], lines[i])
		}
	}
}

func TestResolveFormat(t *testing.T) {
	// An explicit flag always wins.
	if got := ResolveFormat("csv", 0); got != FormatCSV {
		t.Errorf("got %q, want csv", got)
	}
	// A non-TTY fd (a pipe in CI, or fd 0 under `go test`) defaults to JSON so
	// piping needs no flags.
	if got := ResolveFormat("", ^uintptr(0)); got != FormatJSON {
		t.Errorf("got %q, want json for a non-terminal", got)
	}
}

func TestValid(t *testing.T) {
	for _, f := range []string{"json", "ndjson", "csv", "table", "text"} {
		if !Valid(f) {
			t.Errorf("Valid(%q) = false", f)
		}
	}
	for _, f := range []string{"yaml", "xml", "", "JSON"} {
		if Valid(f) {
			t.Errorf("Valid(%q) = true, want false", f)
		}
	}
}

func TestMetaFromCache(t *testing.T) {
	at := time.Now().Add(-30 * time.Minute)
	m := MetaFromCache(true, at, 90*time.Minute, 0)
	if !m.Cached || m.CachedAt == "" {
		t.Errorf("got %+v", m)
	}
	if m.TTLRemainingSec == nil || *m.TTLRemainingSec != 5400 {
		t.Errorf("ttl_remaining_sec = %v, want 5400", m.TTLRemainingSec)
	}

	// A fresh result carries no cache timestamps at all.
	fresh := MetaFromCache(false, time.Time{}, 0, 1)
	if fresh.CachedAt != "" || fresh.TTLRemainingSec != nil || fresh.APICalls != 1 {
		t.Errorf("got %+v", fresh)
	}
}
