package strip

import (
	"strings"
	"testing"
)

func TestStripHTML(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		// -- elements whose contents are not prose --------------------------
		{
			name: "script contents are dropped",
			in:   "<p>Text.</p><script>var x = 1; if (a < b) { alert(\"hi\"); }</script><p>More.</p>",
			want: "Text.\n\nMore.",
		},
		{
			name: "unclosed script eats the rest",
			in:   "<p>Text.</p><script>var x = 1;",
			want: "Text.",
		},
		{
			name: "style contents are dropped",
			in:   "<style>body { color: red; font-family: serif; }</style><p>Text.</p>",
			want: "Text.",
		},
		{
			name: "head contents are dropped",
			in:   "<html><head><title>Page title</title><meta charset=\"utf-8\"></head><body><p>Text.</p></body></html>",
			want: "Text.",
		},
		{
			name: "unclosed head ends at body",
			in:   "<html><head><title>Page title</title><body><p>Text.</p>",
			want: "Text.",
		},
		{
			name: "noscript contents are dropped",
			in:   "<noscript>Bitte JavaScript aktivieren.</noscript><p>Text.</p>",
			want: "Text.",
		},
		{
			name: "comments are dropped with their contents",
			in:   "<p>Before.</p><!-- hidden <p>ghost text</p> --><p>After.</p>",
			want: "Before.\n\nAfter.",
		},
		{
			name: "unterminated comment",
			in:   "<p>Before.</p><!-- hidden forever",
			want: "Before.",
		},

		// -- block vs inline -------------------------------------------------
		{
			name: "block elements end sentences",
			in:   "<p>Erster Absatz ohne Punkt</p><p>Zweiter Absatz ohne Punkt</p>",
			want: "Erster Absatz ohne Punkt.\n\nZweiter Absatz ohne Punkt.",
		},
		{
			name: "headings end sentences",
			in:   "<h1>Überschrift</h1><p>Der folgende Absatz.</p>",
			want: "Überschrift.\n\nDer folgende Absatz.",
		},
		{
			name: "list items end sentences",
			in:   "<ul><li>Eins</li><li>Zwei</li></ul>",
			want: "Eins.\n\nZwei.",
		},
		{
			name: "table cells and rows end sentences",
			in:   "<table><tr><td>Ada</td><td>Lead</td></tr><tr><td>Alan</td><td>Analyst</td></tr></table>",
			want: "Ada.\n\nLead.\n\nAlan.\n\nAnalyst.",
		},
		{
			name: "br ends a sentence",
			in:   "<p>Zeile eins<br>Zeile zwei</p>",
			want: "Zeile eins.\n\nZeile zwei.",
		},
		{
			name: "inline elements never split a word",
			in:   "<p>Der <em>Sprach</em><strong>raum</strong> ist gro<span>ß</span>.</p>",
			want: "Der Sprachraum ist groß.",
		},
		{
			name: "inline anchor keeps its text",
			in:   "<p>Siehe die <a href=\"https://example.com/docs?a=1&amp;b=2\">Dokumentation</a> dazu.</p>",
			want: "Siehe die Dokumentation dazu.",
		},
		{
			name: "inline code keeps its text",
			in:   "<p>Der Wert <code>maxRetries</code> steuert das.</p>",
			want: "Der Wert maxRetries steuert das.",
		},
		{
			name: "existing punctuation is not doubled",
			in:   "<h2>Warum?</h2><p>Darum.</p>",
			want: "Warum?\n\nDarum.",
		},
		{
			name: "source newlines inside a paragraph are just spaces",
			in:   "<p>Ein Satz, der im Quelltext\nüber zwei Zeilen läuft.</p>",
			want: "Ein Satz, der im Quelltext über zwei Zeilen läuft.",
		},
		{
			name: "attributes never reach the prose",
			in:   "<div id=\"main\" data-title=\"Nicht lesen\" class=\"a b\"><p>Nur das hier.</p></div>",
			want: "Nur das hier.",
		},
		{
			name: "attribute value containing a closing bracket",
			in:   "<p title=\"a > b\">Text.</p>",
			want: "Text.",
		},
		{
			name: "unknown custom elements are stripped as inline",
			in:   "<p>Ein <my-widget foo=\"1\">eingebettetes</my-widget> Element.</p>",
			want: "Ein eingebettetes Element.",
		},
		{
			// "<" followed by a space cannot start a tag. An unescaped "a<b"
			// would be parsed as a <b> start tag here, exactly as a browser
			// parses it; in valid HTML that "<" would be written "&lt;".
			name: "text that only looks like a tag survives",
			in:   "<p>Wenn 5 < 10 gilt, dann ist c > d.</p>",
			want: "Wenn 5 < 10 gilt, dann ist c > d.",
		},
		{
			name: "doctype and processing instructions vanish",
			in:   "<!DOCTYPE html><?xml version=\"1.0\"?><p>Text.</p>",
			want: "Text.",
		},
		{
			name: "img alt text is not body prose",
			in:   "<p>Vor <img src=\"a.png\" alt=\"Ein Bild\"> nach.</p>",
			want: "Vor nach.",
		},
		{
			name: "empty elements leave no sentence behind",
			in:   "<p></p><div>   </div><p>Nur ein Satz.</p><br>",
			want: "Nur ein Satz.",
		},
		{
			name: "empty input",
			in:   "",
			want: "",
		},

		// -- entities ---------------------------------------------------------
		{
			name: "named entities including german umlauts",
			in:   "<p>Gr&ouml;&szlig;e, &auml;u&szlig;erst &Uuml;bel, &Auml;pfel und &Ouml;l</p>",
			want: "Größe, äußerst Übel, Äpfel und Öl.",
		},
		{
			name: "markup entities",
			in:   "<p>Tom &amp; Jerry, 5 &lt; 10 &gt; 3, &quot;zitiert&quot;, &#39;auch&#39;</p>",
			// The terminator goes inside the trailing quote, as it does for any
			// block that ends on a closing quotation mark.
			want: "Tom & Jerry, 5 < 10 > 3, \"zitiert\", 'auch.'",
		},
		{
			name: "numeric entities decimal and hex",
			in:   "<p>&#8364;5, &#x41;BC, &#252;ber</p>",
			want: "€5, ABC, über.",
		},
		{
			name: "nbsp becomes an ordinary space",
			in:   "<p>10&nbsp;000&nbsp;Wörter</p>",
			want: "10 000 Wörter.",
		},
		{
			name: "soft hyphen disappears without splitting the word",
			in:   "<p>Sil&shy;ben&shy;tren&shy;nung</p>",
			want: "Silbentrennung.",
		},
		{
			name: "entities are decoded exactly once",
			in:   "<p>&amp;lt; bleibt sichtbar</p>",
			want: "&lt; bleibt sichtbar.",
		},
		{
			name: "an unknown or bare ampersand is left alone",
			in:   "<p>Tom & Jerry, &nichts; und &amp</p>",
			want: "Tom & Jerry, &nichts; und &amp.",
		},

		// -- whole documents ----------------------------------------------------
		{
			name: "full document",
			in: "<!DOCTYPE html>\n<html lang=\"de\">\n<head><title>T</title><style>p{color:red}</style></head>\n" +
				"<body>\n<!-- nav -->\n<h1>Titel</h1>\n<p>Ein Satz mit <em>Betonung</em>.</p>\n" +
				"<ul>\n  <li>Punkt eins</li>\n  <li>Punkt zwei</li>\n</ul>\n" +
				"<script>console.log(\"x\");</script>\n</body>\n</html>\n",
			want: "Titel.\n\nEin Satz mit Betonung.\n\nPunkt eins.\n\nPunkt zwei.",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := Apply(tc.in, ModeHTML); got != tc.want {
				t.Errorf("Apply(html)\n got: %q\nwant: %q", got, tc.want)
			}
		})
	}
}

func TestDecodeEntities(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"no entities", "plain text", "plain text"},
		{"amp", "a &amp; b", "a & b"},
		{"lt gt", "&lt;tag&gt;", "<tag>"},
		{"quot and apos", "&quot;x&quot; &apos;y&apos;", "\"x\" 'y'"},
		{"numeric apostrophe", "it&#39;s", "it's"},
		{"german umlauts", "&auml;&ouml;&uuml;&szlig;&Auml;&Ouml;&Uuml;", "äöüßÄÖÜ"},
		{"hex", "&#x2014;", "—"},
		{"nbsp", "a&nbsp;b", "a b"},
		{"unknown entity stays", "&frobnicate;", "&frobnicate;"},
		{"unterminated entity stays", "&amp no semicolon", "&amp no semicolon"},
		{"bare ampersand", "AT&T", "AT&T"},
		{"zero code point is not decoded", "&#0;", "&#0;"},
		{"surrogate is not decoded", "&#xD800;", "&#xD800;"},
		{"control code becomes a space", "a&#10;b", "a b"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := decodeEntities(tc.in); got != tc.want {
				t.Errorf("decodeEntities(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// The umlaut case is why entity decoding is not optional: "&ouml;" left raw
// contributes five extra characters and at least one extra vowel group to every
// German word that contains it.
func TestEntityDecodingProtectsGermanWordShape(t *testing.T) {
	const in = "<p>Gr&ouml;&szlig;e und Ma&szlig;e der W&auml;nde.</p>"
	got := Apply(in, ModeHTML)
	if want := "Größe und Maße der Wände."; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	for _, w := range strings.Fields(got) {
		if strings.ContainsAny(w, "&;") {
			t.Errorf("entity residue in word %q", w)
		}
	}
}
