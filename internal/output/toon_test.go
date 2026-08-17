package output

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// encodeTOON is the test helper: it runs a raw JSON document through the same
// tree encoder WriteTOON uses, so cases can be written as JSON literals.
func encodeTOON(t *testing.T, jsonDoc string) string {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(jsonDoc))
	dec.UseNumber()
	var tree any
	if err := dec.Decode(&tree); err != nil {
		t.Fatalf("bad test fixture %q: %v", jsonDoc, err)
	}
	var sb strings.Builder
	e := &toonEncoder{b: &sb}
	switch v := tree.(type) {
	case map[string]any:
		e.object(v, 0)
	case []any:
		if len(v) == 0 {
			e.line(0, "[]")
		} else {
			e.array("", v, 0, true)
		}
	default:
		e.line(0, toonPrimitive(tree))
	}
	return sb.String()
}

func TestTOONEncode(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// --- shapes -------------------------------------------------------
		{
			name: "uniform array is tabular",
			in:   `{"items":[{"id":1,"qty":5},{"id":2,"qty":3}]}`,
			want: "items[2]{id,qty}:\n  1,5\n  2,3\n",
		},
		{
			name: "single row still tabular",
			in:   `{"items":[{"id":1,"n":"x"}]}`,
			want: "items[1]{id,n}:\n  1,x\n",
		},
		{
			name: "tabular fields are sorted not source order",
			in:   `{"items":[{"z":1,"a":2},{"a":3,"z":4}]}`,
			want: "items[2]{a,z}:\n  2,1\n  3,4\n",
		},
		{
			name: "non-uniform key sets fall back to list",
			in:   `{"items":[{"a":1,"b":2},{"a":3,"c":4}]}`,
			want: "items[2]:\n  - a: 1\n    b: 2\n  - a: 3\n    c: 4\n",
		},
		{
			name: "mixed element types fall back to list",
			in:   `{"items":[1,{"a":1},"x"]}`,
			want: "items[3]:\n  - 1\n  - a: 1\n  - x\n",
		},
		{
			name: "row with a nested array is not tabular",
			in:   `{"items":[{"id":1,"tags":["a","b"]},{"id":2,"tags":["c"]}]}`,
			want: "items[2]:\n  - id: 1\n    tags[2]: a,b\n  - id: 2\n    tags[1]: c\n",
		},
		{
			name: "uniform nested object folds into the header",
			in:   `{"items":[{"id":1,"o":{"x":1,"y":2}},{"id":2,"o":{"x":3,"y":4}}]}`,
			want: "items[2]{id,o{x,y}}:\n  1,1,2\n  2,3,4\n",
		},
		{
			name: "nested folding recurses",
			in:   `{"items":[{"a":{"b":{"c":1}}},{"a":{"b":{"c":2}}}]}`,
			want: "items[2]{a{b{c}}}:\n  1\n  2\n",
		},
		{
			name: "nested object with differing keys is not tabular",
			in:   `{"items":[{"id":1,"o":{"x":1}},{"id":2,"o":{"y":2}}]}`,
			want: "items[2]:\n  - id: 1\n    o:\n      x: 1\n  - id: 2\n    o:\n      y: 2\n",
		},
		{
			name: "nested empty object is not tabular",
			in:   `{"items":[{"id":1,"o":{}},{"id":2,"o":{}}]}`,
			want: "items[2]:\n  - id: 1\n    o:\n  - id: 2\n    o:\n",
		},
		{
			name: "array of scalars is inline",
			in:   `{"tags":["a","b","c"]}`,
			want: "tags[3]: a,b,c\n",
		},
		{
			name: "empty array",
			in:   `{"items":[]}`,
			want: "items: []\n",
		},
		{
			name: "empty array nested under an object",
			in:   `{"a":{"b":[]}}`,
			want: "a:\n  b: []\n",
		},
		{
			name: "empty array as a list item carries a length header",
			in:   `{"items":[[],[1]]}`,
			want: "items[2]:\n  - [0]:\n  - [1]: 1\n",
		},
		{
			name: "empty object",
			in:   `{"a":{},"b":1}`,
			want: "a:\nb: 1\n",
		},
		{
			name: "empty object as a list item is a bare marker",
			in:   `{"items":[{},{"a":1}]}`,
			want: "items[2]:\n  -\n  - a: 1\n",
		},
		{
			name: "nested object indents two spaces per level",
			in:   `{"user":{"id":1,"name":"Ada"}}`,
			want: "user:\n  id: 1\n  name: Ada\n",
		},
		{
			name: "deep nesting reaches a tabular array",
			in:   `{"a":{"b":{"c":[{"x":1,"y":2},{"x":3,"y":4}]}}}`,
			want: "a:\n  b:\n    c[2]{x,y}:\n      1,2\n      3,4\n",
		},
		{
			name: "array of arrays",
			in:   `{"pairs":[[1,2],[3,4]]}`,
			want: "pairs[2]:\n  - [2]: 1,2\n  - [2]: 3,4\n",
		},
		{
			name: "nested non-uniform array under a list item",
			in:   `{"items":["summary",{"id":1,"name":"Ada"},[{"id":2},{"status":"draft"}]]}`,
			want: "items[3]:\n  - summary\n  - id: 1\n    name: Ada\n  - [2]:\n    - id: 2\n    - status: draft\n",
		},
		{
			name: "object under a list item indents below its key",
			in:   `{"items":[{"a":{"b":1}},{"c":2}]}`,
			want: "items[2]:\n  - a:\n      b: 1\n  - c: 2\n",
		},
		{
			name: "root array of scalars",
			in:   `["x","y","z"]`,
			want: "[3]: x,y,z\n",
		},
		{
			name: "root tabular array",
			in:   `[{"id":1,"n":"a"},{"id":2,"n":"b"}]`,
			want: "[2]{id,n}:\n  1,a\n  2,b\n",
		},
		{
			name: "root empty array",
			in:   `[]`,
			want: "[]\n",
		},
		{
			name: "root scalar",
			in:   `"hi"`,
			want: "hi\n",
		},

		// --- scalars ------------------------------------------------------
		{
			name: "null and booleans",
			in:   `{"a":null,"b":true,"c":false}`,
			want: "a: null\nb: true\nc: false\n",
		},
		{
			name: "nulls survive tabular rows",
			in:   `{"items":[{"a":1,"v":null},{"a":2,"v":null}]}`,
			want: "items[2]{a,v}:\n  1,null\n  2,null\n",
		},
		{
			name: "integers stay integral",
			in:   `{"a":100,"b":1000000,"c":0,"d":-7}`,
			want: "a: 100\nb: 1000000\nc: 0\nd: -7\n",
		},
		{
			name: "large integers keep full precision",
			in:   `{"a":9007199254740993,"b":18446744073709551615}`,
			want: "a: 9007199254740993\nb: 18446744073709551615\n",
		},
		{
			name: "floats keep their literal form",
			in:   `{"a":1.5,"b":-3.14,"c":0.000001,"d":66.7}`,
			want: "a: 1.5\nb: -3.14\nc: 0.000001\nd: 66.7\n",
		},

		// --- quoting rules ------------------------------------------------
		{name: "quote empty string", in: `{"a":""}`, want: "a: \"\"\n"},
		{name: "quote leading space", in: `{"a":" x"}`, want: "a: \" x\"\n"},
		{name: "quote trailing space", in: `{"a":"x "}`, want: "a: \"x \"\n"},
		{name: "quote leading tab", in: `{"a":"\tx"}`, want: "a: \"\\tx\"\n"},
		{name: "quote literal true", in: `{"a":"true"}`, want: "a: \"true\"\n"},
		{name: "quote literal false", in: `{"a":"false"}`, want: "a: \"false\"\n"},
		{name: "quote literal null", in: `{"a":"null"}`, want: "a: \"null\"\n"},
		{name: "case differs from literal so bare", in: `{"a":"True","b":"NULL"}`, want: "a: True\nb: NULL\n"},
		{name: "quote integer-looking string", in: `{"a":"42"}`, want: "a: \"42\"\n"},
		{name: "quote negative-number-looking string", in: `{"a":"-3.14"}`, want: "a: \"-3.14\"\n"},
		{name: "quote exponent-looking string", in: `{"a":"1e-6"}`, want: "a: \"1e-6\"\n"},
		{name: "quote leading-zero number string", in: `{"a":"05"}`, want: "a: \"05\"\n"},
		{name: "quote plus-signed number string", in: `{"a":"+5"}`, want: "a: \"+5\"\n"},
		{name: "not a number so bare", in: `{"a":".5","b":"1_000","c":"0x1F","d":"NaN"}`, want: "a: .5\nb: 1_000\nc: 0x1F\nd: NaN\n"},
		{name: "quote colon", in: `{"a":"x:y"}`, want: "a: \"x:y\"\n"},
		{name: "quote double quote", in: `{"a":"x\"y"}`, want: "a: \"x\\\"y\"\n"},
		{name: "quote backslash", in: `{"a":"x\\y"}`, want: "a: \"x\\\\y\"\n"},
		{name: "quote brackets", in: `{"a":"[x]"}`, want: "a: \"[x]\"\n"},
		{name: "quote braces", in: `{"a":"{x}"}`, want: "a: \"{x}\"\n"},
		{name: "quote newline", in: `{"a":"x\ny"}`, want: "a: \"x\\ny\"\n"},
		{name: "quote tab", in: `{"a":"x\ty"}`, want: "a: \"x\\ty\"\n"},
		{name: "quote carriage return", in: `{"a":"x\ry"}`, want: "a: \"x\\ry\"\n"},
		{name: "quote other control chars", in: `{"a":"x\u0001y"}`, want: "a: \"x\\u0001y\"\n"},
		{name: "quote delimiter comma", in: `{"a":"hello, world"}`, want: "a: \"hello, world\"\n"},
		{name: "quote bare hyphen", in: `{"a":"-"}`, want: "a: \"-\"\n"},
		{name: "quote leading hyphen", in: `{"a":"-x"}`, want: "a: \"-x\"\n"},
		{name: "hyphen inside is bare", in: `{"a":"x-y"}`, want: "a: x-y\n"},
		{name: "quote leading hash", in: `{"a":"#x"}`, want: "a: \"#x\"\n"},
		{name: "hash inside is bare", in: `{"a":"x#y"}`, want: "a: x#y\n"},
		{name: "spaces inside are bare", in: `{"a":"hello world"}`, want: "a: hello world\n"},
		{
			// The whole point: this CLI emits German constantly.
			name: "german umlauts stay unquoted and unescaped",
			in:   `{"a":"Die Katze sitzt auf der Matte","b":"Größe für Äpfel","c":"Straße Öl Übung äöüß"}`,
			want: "a: Die Katze sitzt auf der Matte\nb: Größe für Äpfel\nc: Straße Öl Übung äöüß\n",
		},
		{
			name: "emoji and other unicode stay unquoted",
			in:   `{"a":"ökonomisch 😀","b":"日本語","c":"café — naïve"}`,
			want: "a: ökonomisch 😀\nb: 日本語\nc: café — naïve\n",
		},
		{
			name: "quoting applies inside tabular rows",
			in:   `{"items":[{"a":"","b":"x,y"},{"a":"ok","b":"Größe"}]}`,
			want: "items[2]{a,b}:\n  \"\",\"x,y\"\n  ok,Größe\n",
		},

		// --- keys ---------------------------------------------------------
		{
			name: "tabular header quotes unsafe field names",
			in:   `{"items":[{"a b":1,"c:d":2},{"a b":3,"c:d":4}]}`,
			want: "items[2]{\"a b\",\"c:d\"}:\n  1,2\n  3,4\n",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := encodeTOON(t, tc.in)
			if got != tc.want {
				t.Errorf("input %s\n got: %q\nwant: %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestTOONKeyOrdering checks key sorting and key quoting together. Keys follow
// a stricter rule than values: only an ASCII letter or underscore may lead, and
// only letters, digits, underscores, and dots may follow.
func TestTOONKeyOrdering(t *testing.T) {
	got := encodeTOON(t, `{"a b":1,"a,b":2,"a:b":3,"":4,"42":5,"-x":6,"a.b":7,"a_b":8,"A1":9,"a-b":10,"Größe":11,"true":12}`)
	want := strings.Join([]string{
		// Sorted by byte order on the raw key, not the encoded token.
		`"": 4`,
		`"-x": 6`,
		`"42": 5`,
		`A1: 9`,
		`"Größe": 11`,
		`"a b": 1`,
		`"a,b": 2`,
		`"a-b": 10`,
		`a.b: 7`,
		`"a:b": 3`,
		`a_b: 8`,
		`true: 12`,
		``,
	}, "\n")
	if got != want {
		t.Errorf("\n got: %q\nwant: %q", got, want)
	}
}

func TestTOONEnvelope(t *testing.T) {
	type doc struct {
		ID    string  `json:"id"`
		Score float64 `json:"score"`
	}
	var buf bytes.Buffer
	err := WriteTOON(&buf, map[string]any{
		"documents": []doc{{ID: "a", Score: 66.7}, {ID: "b", Score: 12}},
	}, Meta{APICalls: 1, Language: "de", Documents: 2})
	if err != nil {
		t.Fatalf("WriteTOON: %v", err)
	}
	got := buf.String()

	// Root has exactly the two envelope keys the JSON path emits, at column 0.
	var roots []string
	for _, ln := range strings.Split(strings.TrimRight(got, "\n"), "\n") {
		if !strings.HasPrefix(ln, " ") {
			roots = append(roots, strings.SplitN(ln, ":", 2)[0])
		}
	}
	if len(roots) != 2 || roots[0] != "data" || roots[1] != "meta" {
		t.Fatalf("root keys = %v, want [data meta]; output:\n%s", roots, got)
	}
	if !strings.Contains(got, "documents[2]{id,score}:") {
		t.Errorf("expected tabular documents header, got:\n%s", got)
	}
	// 12 came in as a float64 but marshals as an integer; it must not gain a
	// decimal point or an exponent on the way through.
	if !strings.Contains(got, "\n    b,12\n") {
		t.Errorf("expected row `b,12`, got:\n%s", got)
	}
}

func TestTOONEnvelopeMatchesJSON(t *testing.T) {
	data := map[string]any{"documents": []map[string]any{{"id": "a", "n": 1}}}
	meta := Meta{APICalls: 2, Cached: true, Language: "en"}

	var jb bytes.Buffer
	if err := WriteJSON(&jb, data, meta); err != nil {
		t.Fatal(err)
	}
	var jsonTree map[string]any
	if err := json.Unmarshal(jb.Bytes(), &jsonTree); err != nil {
		t.Fatal(err)
	}
	if _, ok := jsonTree["data"]; !ok {
		t.Fatal("JSON envelope lost data")
	}

	var tb bytes.Buffer
	if err := WriteTOON(&tb, data, meta); err != nil {
		t.Fatal(err)
	}
	// Every key the JSON envelope's meta carries must appear in the TOON meta
	// block, so omitempty behaviour cannot drift between the two encodings.
	metaKeys := jsonTree["meta"].(map[string]any)
	for k := range metaKeys {
		if !strings.Contains(tb.String(), "\n  "+k+":") {
			t.Errorf("meta key %q missing from TOON:\n%s", k, tb.String())
		}
	}
}

func TestTOONDeterministic(t *testing.T) {
	data := map[string]any{
		"zeta":  1,
		"alpha": map[string]any{"q": 1, "b": 2, "m": 3, "a": 4, "z": 5},
		"items": []map[string]any{
			{"id": "a", "score": 1.5, "flag": true},
			{"id": "b", "score": 2.5, "flag": false},
		},
		"tags": []string{"x", "y"},
	}
	meta := Meta{APICalls: 1, Documents: 2, Language: "de", LanguageDetected: true}

	var first bytes.Buffer
	if err := WriteTOON(&first, data, meta); err != nil {
		t.Fatal(err)
	}
	// Many iterations: Go randomises map iteration order per range, so an
	// unsorted encoder fails this well before the loop ends.
	for i := 0; i < 200; i++ {
		var buf bytes.Buffer
		if err := WriteTOON(&buf, data, meta); err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(buf.Bytes(), first.Bytes()) {
			t.Fatalf("run %d differs:\n%s\n---\n%s", i, first.String(), buf.String())
		}
	}
}

func TestTOONSmallerThanJSON(t *testing.T) {
	data := map[string]any{"documents": []map[string]any{
		{"id": "a", "flesch": 66.7, "grade": 8.1, "words": 120, "sentences": 9},
		{"id": "b", "flesch": 41.2, "grade": 12.4, "words": 340, "sentences": 14},
		{"id": "c", "flesch": 72.9, "grade": 6.5, "words": 88, "sentences": 7},
	}}
	meta := Meta{APICalls: 0, Documents: 3, Language: "de"}

	var jb, tb bytes.Buffer
	if err := WriteJSON(&jb, data, meta); err != nil {
		t.Fatal(err)
	}
	if err := WriteTOON(&tb, data, meta); err != nil {
		t.Fatal(err)
	}
	if tb.Len() >= jb.Len() {
		t.Errorf("TOON (%d bytes) not smaller than JSON (%d bytes):\n%s", tb.Len(), jb.Len(), tb.String())
	}
}

func TestValidAcceptsTOON(t *testing.T) {
	if !Valid("toon") {
		t.Error("Valid(\"toon\") = false, want true")
	}
	if Valid("yaml") {
		t.Error("Valid(\"yaml\") = true, want false")
	}
}
