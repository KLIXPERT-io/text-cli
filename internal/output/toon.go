package output

// TOON (Token-Oriented Object Notation) encoding.
//
// TOON is a compact, indentation-based serialization that keeps JSON's data
// model but drops most of its punctuation. For a uniform array of objects it
// hoists the field names into a single header line and emits the rows as bare
// delimited values, which is where the 30-60% token saving over JSON comes
// from. This CLI's `documents[]`, `metrics[]`, and `entities[]` arrays are
// exactly that shape.
//
// The encoded document is the same envelope the JSON path emits, so
// `--output toon` and `--output json` are one document in two encodings. To
// guarantee that parity rather than re-deriving it, the envelope is marshalled
// to JSON and re-parsed into a generic tree, which applies every `json` tag,
// `omitempty`, and custom marshaller exactly as the JSON path would.
//
// Grammar implemented (verified against the reference encoder,
// github.com/toon-format/toon):
//
//	object field       key: value                 (2 spaces per nesting level)
//	nested object      key:                       then fields at level+1
//	empty object       key:                       (no children)
//	array of scalars   key[3]: a,b,c
//	empty array        key: []                    ([] at root, [0]: as a list item)
//	uniform objects    key[2]{id,qty}:            then rows `1,5` at level+1
//	  with sub-object  key[2]{id,extra{a,b}}:     nested fields fold into the header
//	other arrays       key[3]:                    then `- item` lines at level+1
//	root array         [3]: x,y,z                 (same forms, empty key)
//
// Strings are emitted bare unless they would be ambiguous; see toonSafeUnquoted
// for the full rule list. Unicode and emoji are always safe unquoted, so German
// text keeps its umlauts verbatim.

import (
	"bytes"
	"encoding/json"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// toonDelimiter is the active delimiter. TOON also allows tab and pipe; this
// encoder only emits the default comma, and quotes any string containing one.
const toonDelimiter = ","

// toonIndent is one nesting level. The format fixes this at two spaces.
const toonIndent = "  "

// WriteTOON writes the {data, meta} envelope as a TOON document.
func WriteTOON(w io.Writer, data any, meta Meta) error {
	raw, err := json.Marshal(Envelope{Data: data, Meta: meta})
	if err != nil {
		return err
	}
	// UseNumber keeps numbers as their literal JSON text, so a large int64 is
	// not silently rounded through float64 and 100 does not become 100.0 or
	// 1e+06. The literal is emitted verbatim, which also makes TOON numbers
	// byte-identical to the ones --output json prints.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		return err
	}

	var sb strings.Builder
	t := &toonEncoder{b: &sb}
	switch v := tree.(type) {
	case map[string]any:
		t.object(v, 0)
	case []any:
		if len(v) == 0 {
			t.line(0, "[]")
		} else {
			t.array("", v, 0, true)
		}
	default:
		t.line(0, toonPrimitive(tree))
	}

	_, err = io.WriteString(w, sb.String())
	return err
}

type toonEncoder struct{ b *strings.Builder }

func (t *toonEncoder) line(indent int, s string) {
	t.b.WriteString(strings.Repeat(toonIndent, indent))
	t.b.WriteString(s)
	t.b.WriteByte('\n')
}

// object writes an object's fields, one per line, at the given nesting level.
//
// Keys are sorted. Struct field order does not survive the JSON round-trip
// (encoding/json unmarshals objects into map[string]any, and Go map iteration
// order is randomised per run), so sorting is what makes the output
// deterministic and the tests stable.
func (t *toonEncoder) object(m map[string]any, indent int) {
	for _, k := range sortedKeys(m) {
		t.field(k, m[k], indent)
	}
}

// field writes one `key: value` construct, recursing for objects and arrays.
func (t *toonEncoder) field(key string, v any, indent int) {
	kt := toonKey(key)
	switch x := v.(type) {
	case map[string]any:
		// An empty object is a bare `key:` with no children.
		t.line(indent, kt+":")
		t.object(x, indent+1)
	case []any:
		if len(x) == 0 {
			t.line(indent, kt+": []")
			return
		}
		t.array(kt, x, indent, true)
	default:
		t.line(indent, kt+": "+toonPrimitive(v))
	}
}

// array writes a non-empty array whose header line starts with prefix (the
// encoded key, or "" for a root array or list item). allowTabular is false for
// an array that is itself a list item, matching the reference encoder, which
// only folds an array into tabular form when it hangs off a key or the root.
func (t *toonEncoder) array(prefix string, arr []any, indent int, allowTabular bool) {
	header := prefix + "[" + strconv.Itoa(len(arr)) + "]"

	// All scalars: inline on the header line.
	if toonAllScalars(arr) {
		t.line(indent, header+": "+toonJoin(arr))
		return
	}

	// Uniform objects: hoist the field names into the header and emit bare rows.
	if allowTabular {
		if rows, ok := toonRows(arr); ok {
			if fields, paths, ok := toonTabularSpec(rows); ok {
				t.line(indent, header+"{"+strings.Join(fields, toonDelimiter)+"}:")
				for _, row := range rows {
					cells := make([]string, len(paths))
					for i, p := range paths {
						cells[i] = toonPrimitive(toonAt(row, p))
					}
					t.line(indent+1, strings.Join(cells, toonDelimiter))
				}
				return
			}
		}
	}

	// Everything else: one `- ` list item per element.
	t.line(indent, header+":")
	for _, el := range arr {
		t.listItem(el, indent+1)
	}
}

// listItem writes one `- ` element of a non-uniform array.
//
// The `- ` marker is exactly as wide as one indent level, so a list item is
// rendered by encoding its content one level deeper and then overwriting the
// last two spaces of the first line's indentation with "- ". Continuation lines
// keep their own indentation, which is why `- id: 1` is followed by
// `  name: Ada` aligned under `id`.
func (t *toonEncoder) listItem(v any, indent int) {
	switch x := v.(type) {
	case map[string]any:
		if len(x) == 0 {
			// An empty object carries no content, so only the marker remains.
			t.line(indent, "-")
			return
		}
		var sub strings.Builder
		(&toonEncoder{b: &sub}).object(x, indent+1)
		t.writeMarked(sub.String(), (indent+1)*len(toonIndent), indent)
	case []any:
		if len(x) == 0 {
			// A keyed empty array is `key: []`, but a bare one needs a header
			// to carry the length.
			t.line(indent, "- [0]:")
			return
		}
		// A nested array keeps its own level: its children sit one level below
		// the marker, not below the header text.
		var sub strings.Builder
		(&toonEncoder{b: &sub}).array("", x, indent, false)
		t.writeMarked(sub.String(), indent*len(toonIndent), indent)
	default:
		t.line(indent, "- "+toonPrimitive(v))
	}
}

// writeMarked emits s with the first line's leading indentation (firstIndent
// bytes) replaced by indent levels of spaces plus the "- " list marker.
func (t *toonEncoder) writeMarked(s string, firstIndent, indent int) {
	if len(s) >= firstIndent {
		s = s[firstIndent:]
	}
	t.b.WriteString(strings.Repeat(toonIndent, indent))
	t.b.WriteString("- ")
	t.b.WriteString(s)
}

// toonRows asserts that every element of arr is an object.
func toonRows(arr []any) ([]map[string]any, bool) {
	rows := make([]map[string]any, len(arr))
	for i, el := range arr {
		m, ok := el.(map[string]any)
		if !ok {
			return nil, false
		}
		rows[i] = m
	}
	return rows, true
}

// toonTabularSpec decides whether a slice of objects can be written in tabular
// form, and if so returns the header field tokens and the value path for each
// column.
//
// Rows qualify only if the slice is non-empty, every row is a non-empty object
// with exactly the same key set, and every field is either a scalar in all rows
// or an object in all rows that itself qualifies. That nested case folds into
// the header as `extra{asl,asw}` and contributes its leaves as extra columns,
// which is what lets this CLI's metrics[] rows collapse to one line each.
//
// Anything else — a field that is an array, a mixed batch whose rows disagree
// on their keys — returns false and falls back to the list form, which is
// always valid TOON.
func toonTabularSpec(rows []map[string]any) (header []string, paths [][]string, ok bool) {
	if len(rows) == 0 || len(rows[0]) == 0 {
		return nil, nil, false
	}
	fields := sortedKeys(rows[0])
	for _, r := range rows {
		if len(r) != len(fields) {
			return nil, nil, false
		}
		for _, f := range fields {
			if _, present := r[f]; !present {
				return nil, nil, false
			}
		}
	}

	for _, f := range fields {
		vals := make([]any, len(rows))
		for i, r := range rows {
			vals[i] = r[f]
		}
		if toonAllScalars(vals) {
			header = append(header, toonKey(f))
			paths = append(paths, []string{f})
			continue
		}
		sub, ok := toonRows(vals)
		if !ok {
			return nil, nil, false
		}
		subHeader, subPaths, ok := toonTabularSpec(sub)
		if !ok {
			return nil, nil, false
		}
		header = append(header, toonKey(f)+"{"+strings.Join(subHeader, toonDelimiter)+"}")
		for _, p := range subPaths {
			paths = append(paths, append([]string{f}, p...))
		}
	}
	return header, paths, true
}

// toonAt resolves a column path against one row.
func toonAt(row map[string]any, path []string) any {
	var cur any = row
	for _, seg := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil
		}
		cur = m[seg]
	}
	return cur
}

func toonAllScalars(arr []any) bool {
	for _, el := range arr {
		if !toonIsScalar(el) {
			return false
		}
	}
	return true
}

func toonIsScalar(v any) bool {
	switch v.(type) {
	case nil, bool, string, json.Number:
		return true
	}
	return false
}

func toonJoin(arr []any) string {
	parts := make([]string, len(arr))
	for i, el := range arr {
		parts[i] = toonPrimitive(el)
	}
	return strings.Join(parts, toonDelimiter)
}

// toonPrimitive renders a scalar as a TOON token.
func toonPrimitive(v any) string {
	switch x := v.(type) {
	case nil:
		return "null"
	case bool:
		if x {
			return "true"
		}
		return "false"
	case json.Number:
		// Emitted verbatim: encoding/json already produced the shortest
		// round-trippable form, so integers stay integral and floats keep the
		// same text --output json prints.
		return x.String()
	case string:
		return toonString(x)
	default:
		// Unreachable after the JSON round-trip; degrade to JSON text.
		b, err := json.Marshal(v)
		if err != nil {
			return "null"
		}
		return toonString(string(b))
	}
}

// toonNumericLike matches strings a decoder would read back as a number.
var toonNumericLike = regexp.MustCompile(`^[+-]?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?$`)

// toonUnquotedKey matches keys that need no quoting: ASCII letter or
// underscore, then letters, digits, underscores, or dots.
var toonUnquotedKey = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_.]*$`)

func toonKey(k string) string {
	if toonUnquotedKey.MatchString(k) {
		return k
	}
	return `"` + toonEscape(k) + `"`
}

func toonString(s string) string {
	if toonSafeUnquoted(s) {
		return s
	}
	return `"` + toonEscape(s) + `"`
}

// toonSafeUnquoted reports whether s can be written without quotes. It must be
// quoted if it is empty, has leading or trailing space or tab, reads as
// true/false/null or as a number, contains a structural character
// (: " \ [ ] { }), contains a control character, contains the active delimiter,
// or starts with the list marker "-" or the comment marker "#".
//
// Everything else is left bare, including all non-ASCII text: umlauts, accents,
// CJK, and emoji are safe unquoted and must not be escaped.
func toonSafeUnquoted(s string) bool {
	if s == "" {
		return false
	}
	if hasEdge(s, ' ') || hasEdge(s, '\t') {
		return false
	}
	switch s {
	case "true", "false", "null":
		return false
	}
	if toonNumericLike.MatchString(s) {
		return false
	}
	if strings.ContainsAny(s, ":\"\\[]{}") {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < 0x20 {
			return false
		}
	}
	if strings.Contains(s, toonDelimiter) {
		return false
	}
	if s[0] == '-' || s[0] == '#' {
		return false
	}
	return true
}

func hasEdge(s string, c byte) bool {
	return s[0] == c || s[len(s)-1] == c
}

// toonEscape applies the five TOON escapes; any other control character becomes
// a \uXXXX sequence. Multi-byte UTF-8 passes through untouched.
func toonEscape(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 2)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case '\\':
			b.WriteString(`\\`)
		case '"':
			b.WriteString(`\"`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		default:
			if c < 0x20 {
				const hex = "0123456789abcdef"
				b.WriteString(`\u00`)
				b.WriteByte(hex[c>>4])
				b.WriteByte(hex[c&0xf])
				continue
			}
			b.WriteByte(c)
		}
	}
	return b.String()
}

func sortedKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
