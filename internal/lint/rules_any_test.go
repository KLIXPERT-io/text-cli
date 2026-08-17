package lint

import (
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/textproc"
)

// ruleCase is one table row: a document, a configuration, and exactly which
// spans the rule is expected to flag.
type ruleCase struct {
	name string
	text string
	lang textproc.Language
	cfg  Config
	want []string // the excerpt of each expected finding, in order
	// starts pins the expected byte offsets when the first occurrence of the
	// excerpt is not the one that should be flagged.
	starts []int
	// sev, when set, is asserted on every finding.
	sev Severity
	// values, when set, is asserted position by position.
	values []float64
}

func runRuleCases(t *testing.T, rule string, cases []ruleCase) {
	t.Helper()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fs := runRule(t, tc.text, tc.lang, rule, tc.cfg)
			assertExcerpts(t, tc.text, fs, tc.want, tc.starts)
			for i, f := range fs {
				if f.Rule != rule {
					t.Fatalf("finding %d carries rule %q", i, f.Rule)
				}
				if tc.sev != "" && f.Severity != tc.sev {
					t.Fatalf("finding %d severity = %q, want %q", i, f.Severity, tc.sev)
				}
				if f.Sentence < 0 {
					t.Fatalf("finding %d has sentence %d", i, f.Sentence)
				}
			}
			for i, v := range tc.values {
				if fs[i].Value != v {
					t.Fatalf("finding %d value = %v, want %v", i, fs[i].Value, v)
				}
			}
		})
	}
}

const (
	// 26 words, one over the default limit.
	longDE = "Die Abteilung prüft die Anträge der Kunden und schickt danach eine Antwort an die Zentrale, " +
		"damit dort die Zahlen für den Bericht des laufenden Quartals vorliegen."
	shortDE = "Die Abteilung prüft die Anträge. Danach schickt sie eine Antwort."
)

func TestLongSentence(t *testing.T) {
	// A 40-word sentence, comfortably past the warn threshold.
	veryLong := "Die Abteilung prüft die Anträge der Kunden und schickt danach eine Antwort an die Zentrale, " +
		"damit dort die Zahlen für den Bericht des Quartals vorliegen und die Kollegen im nächsten Monat " +
		"eine Entscheidung über das weitere Vorgehen treffen können."

	runRuleCases(t, "long-sentence", []ruleCase{
		{
			name: "over the limit",
			text: longDE, lang: textproc.LangGerman,
			want: []string{longDE}, sev: SeverityInfo, values: []float64{26},
		},
		{
			name: "short sentences say nothing",
			text: shortDE, lang: textproc.LangGerman,
			want: nil,
		},
		{
			name: "well past the limit warns",
			text: veryLong, lang: textproc.LangGerman,
			want: []string{veryLong}, sev: SeverityWarn,
		},
		{
			name: "the limit is configurable",
			text: shortDE, lang: textproc.LangGerman,
			cfg:  Config{MaxSentenceWords: 4},
			want: []string{"Die Abteilung prüft die Anträge.", "Danach schickt sie eine Antwort."},
		},
	})

	fs := runRule(t, longDE, textproc.LangGerman, "long-sentence", Config{})
	if !strings.Contains(fs[0].Message, "26 Wörtern") {
		t.Fatalf("the message must name the count: %q", fs[0].Message)
	}
	if !strings.Contains(fs[0].Suggestion, "zwei Sätze") {
		t.Fatalf("the suggestion must be an instruction: %q", fs[0].Suggestion)
	}
}

func TestHardSentence(t *testing.T) {
	hard := "Die Inanspruchnahme der Genehmigungsverfahren erfolgt unter Berücksichtigung " +
		"der landesrechtlichen Ausführungsbestimmungen."
	easy := "Der Hund lief schnell über die Straße und bellte laut."

	runRuleCases(t, "hard-sentence", []ruleCase{
		{
			name: "a hard sentence is reported",
			text: hard, lang: textproc.LangGerman,
			want: []string{hard}, sev: SeverityWarn,
		},
		{
			name: "an easy sentence is not",
			text: easy, lang: textproc.LangGerman,
			want: nil,
		},
		{
			name: "fragments are not hard sentences, they are fragments",
			text: "Genehmigungsverfahren.", lang: textproc.LangGerman,
			want: nil,
		},
	})

	// --worst caps the list: three hard sentences, one reported, and it is the
	// worst of the three.
	text := "Die Inanspruchnahme der Genehmigungsverfahren erfolgt unter Berücksichtigung der " +
		"landesrechtlichen Ausführungsbestimmungen. Die Verwaltungsvereinbarung regelt die " +
		"Zusammenarbeit der Vertragsparteien. Die Datenverarbeitung erfolgt nach den Vorgaben."
	all := runRule(t, text, textproc.LangGerman, "hard-sentence", Config{})
	if len(all) < 2 {
		t.Fatalf("expected several hard sentences, got %d", len(all))
	}
	worst := runRule(t, text, textproc.LangGerman, "hard-sentence", Config{Worst: 1})
	if len(worst) != 1 {
		t.Fatalf("--worst 1 returned %d findings", len(worst))
	}
	for _, f := range all {
		if f.Value < worst[0].Value {
			t.Fatalf("worst list kept %v but %v scores lower", worst[0].Value, f.Value)
		}
	}
	if !strings.Contains(worst[0].Message, "Lesbarkeit") {
		t.Fatalf("German message expected, got %q", worst[0].Message)
	}

	// English uses Flesch, and says so.
	en := runRule(t, "The implementation of the configuration management procedure was subsequently "+
		"discontinued.", textproc.LangEnglish, "hard-sentence", Config{})
	if len(en) != 1 || !strings.Contains(en[0].Message, "Flesch") {
		t.Fatalf("English hard-sentence = %+v", en)
	}
}

func TestSentenceEaseMatchesTheDocumentFormula(t *testing.T) {
	// The arithmetic is duplicated from the readability package on purpose;
	// this pins it so a change there is a visible decision here.
	d := mustDoc(t, "Der Hund lief schnell über die Straße.", textproc.LangGerman)
	toks := newView(d).sentences[0].Toks
	got, ok := sentenceEase(textproc.LangGerman, toks)
	if !ok {
		t.Fatal("German sentence scored nothing")
	}
	syll := 0
	for _, tk := range toks {
		syll += tk.Syll
	}
	want := 180 - float64(len(toks)) - 58.5*(float64(syll)/float64(len(toks)))
	if got != want {
		t.Fatalf("Amstad arithmetic drifted: %v vs %v", got, want)
	}
	if _, ok := sentenceEase(textproc.Language("fr"), toks); ok {
		t.Fatal("an unsupported language must not get a score")
	}
}

func TestSentenceLengthVariance(t *testing.T) {
	monotone := "Der Hund lief schnell nach Hause. Die Katze sprang leise vom Sofa. " +
		"Das Kind malte gerne mit Farben. Der Mann kaufte frisches Brot beim Bäcker. " +
		"Die Frau las abends ein Buch. Der Junge warf den Ball weit."
	varied := "Er kam. Die Abteilung prüft seit dem Frühjahr sämtliche Anträge der Kunden und " +
		"schickt danach eine Antwort an die Zentrale. Sie ging. Das Team bereitet den Bericht " +
		"für das nächste Quartal vor und stimmt ihn mit der Leitung ab. Gut. Ende."

	fs := runRule(t, monotone, textproc.LangGerman, "sentence-length-variance", Config{})
	if len(fs) != 1 {
		t.Fatalf("monotone document produced %d findings", len(fs))
	}
	f := fs[0]
	if f.Start != 0 || f.End != 0 || f.Excerpt != "" || f.Sentence != 0 {
		t.Fatalf("a document-level finding must be anchored at offset 0: %+v", f)
	}
	if !strings.Contains(f.Message, "ganzen Dokument") {
		t.Fatalf("the message must say it is document-level: %q", f.Message)
	}
	if fs[0].Value <= 0 {
		t.Fatalf("value should carry the coefficient of variation, got %v", fs[0].Value)
	}

	if got := runRule(t, varied, textproc.LangGerman, "sentence-length-variance", Config{}); len(got) != 1 ||
		!strings.Contains(got[0].Message, "unregelmäßige") {
		t.Fatalf("erratic document = %+v", got)
	}
	// A document with a normal mix says nothing.
	mixed := "Der Bericht liegt vor. Das Team hat die Zahlen für das dritte Quartal geprüft und " +
		"korrigiert. Die Leitung entscheidet am Montag. Wir informieren die Kunden danach schriftlich. " +
		"Die Frist läuft."
	if got := runRule(t, mixed, textproc.LangGerman, "sentence-length-variance", Config{}); len(got) != 0 {
		t.Fatalf("a normal mix should say nothing: %+v", got)
	}
	// Too short to have a rhythm.
	if got := runRule(t, shortDE, textproc.LangGerman, "sentence-length-variance", Config{}); len(got) != 0 {
		t.Fatalf("two sentences are not a rhythm: %+v", got)
	}
}

func TestRepeatedWord(t *testing.T) {
	near := "Die Anforderungen sind klar dokumentiert. Die Anforderungen gelten ab Montag."
	runRuleCases(t, "repeated-word", []ruleCase{
		{
			name: "the second occurrence is the one to fix",
			text: near, lang: textproc.LangGerman,
			want: []string{"Anforderungen"}, starts: []int{46}, values: []float64{5},
		},
		{
			name: "a narrow window forgives it",
			text: near, lang: textproc.LangGerman,
			cfg:  Config{RepeatWindow: 2},
			want: nil,
		},
		{
			name: "function words are not repetition",
			text: "Die Katze und der Hund und die Maus und der Vogel sind da.", lang: textproc.LangGerman,
			want: nil,
		},
		{
			name: "numbers are not repetition",
			text: "Im Jahr 2024 waren es 2024 Vorgänge insgesamt.", lang: textproc.LangGerman,
			want: nil,
		},
	})

	fs := runRule(t, near, textproc.LangGerman, "repeated-word", Config{})
	start, _ := span(t, near, "Die Anforderungen gelten")
	if fs[0].Start != start+len("Die ") {
		t.Fatalf("the finding must sit on the second occurrence, got offset %d", fs[0].Start)
	}
	if fs[0].Sentence != 2 {
		t.Fatalf("sentence = %d, want 2", fs[0].Sentence)
	}
}

func TestRepeatedSentenceStart(t *testing.T) {
	three := "Die Abteilung prüft. Die Kunden warten. Die Antwort kommt später."
	two := "Die Abteilung prüft. Die Kunden warten. Danach kommt die Antwort."
	runRuleCases(t, "repeated-sentence-start", []ruleCase{
		{
			name: "three in a row",
			text: three, lang: textproc.LangGerman,
			want: []string{"Die"}, values: []float64{3},
		},
		{
			name: "two in a row is a coincidence",
			text: two, lang: textproc.LangGerman,
			want: nil,
		},
	})
	fs := runRule(t, three, textproc.LangGerman, "repeated-sentence-start", Config{})
	if fs[0].Start != 0 || fs[0].End != 3 {
		t.Fatalf("the finding must anchor on the first opening word, got [%d:%d]", fs[0].Start, fs[0].End)
	}
}

func TestLongWord(t *testing.T) {
	text := "Die Straßenverkehrsordnungsnovelle gilt ab Montag."
	runRuleCases(t, "long-word", []ruleCase{
		{
			name: "a compound worth breaking up",
			text: text, lang: textproc.LangGerman,
			// 30 runes, 31 bytes: the value counts characters, not bytes.
			want: []string{"Straßenverkehrsordnungsnovelle"}, values: []float64{30},
		},
		{
			name: "ordinary words are fine",
			text: shortDE, lang: textproc.LangGerman,
			want: nil,
		},
		{
			name: "the limit is configurable",
			text: text, lang: textproc.LangGerman,
			cfg:  Config{MaxWordChars: 40},
			want: nil,
		},
	})
	fs := runRule(t, text, textproc.LangGerman, "long-word", Config{})
	if got := fs[0].End - fs[0].Start; got != 31 {
		t.Fatalf("span is %d bytes; the word is 30 runes and 31 bytes", got)
	}
}
