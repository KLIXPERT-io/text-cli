package textproc

import (
	"errors"
	"math"
	"strings"
	"testing"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
)

const floatEps = 1e-9

func assertStats(t *testing.T, got, want Stats) {
	t.Helper()
	if got.Sentences != want.Sentences {
		t.Errorf("Sentences = %d, want %d", got.Sentences, want.Sentences)
	}
	if got.Words != want.Words {
		t.Errorf("Words = %d, want %d", got.Words, want.Words)
	}
	if got.Syllables != want.Syllables {
		t.Errorf("Syllables = %d, want %d", got.Syllables, want.Syllables)
	}
	if got.Characters != want.Characters {
		t.Errorf("Characters = %d, want %d", got.Characters, want.Characters)
	}
	if got.PolysyllabicWords != want.PolysyllabicWords {
		t.Errorf("PolysyllabicWords = %d, want %d", got.PolysyllabicWords, want.PolysyllabicWords)
	}
	if got.MonosyllabicWords != want.MonosyllabicWords {
		t.Errorf("MonosyllabicWords = %d, want %d", got.MonosyllabicWords, want.MonosyllabicWords)
	}
	if got.LongWords != want.LongWords {
		t.Errorf("LongWords = %d, want %d", got.LongWords, want.LongWords)
	}
	for _, f := range []struct {
		name      string
		got, want float64
	}{
		{"AvgSentenceLength", got.AvgSentenceLength, want.AvgSentenceLength},
		{"AvgSyllablesPerWord", got.AvgSyllablesPerWord, want.AvgSyllablesPerWord},
		{"AvgWordLength", got.AvgWordLength, want.AvgWordLength},
	} {
		if math.Abs(f.got-f.want) > floatEps {
			t.Errorf("%s = %v, want %v", f.name, f.got, f.want)
		}
	}
}

func TestAnalyzeEnglishParagraph(t *testing.T) {
	t.Parallel()
	const text = "The quick brown fox jumps over the lazy dog. It was a simple test."
	doc, err := Analyze(text, LangEnglish)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if doc.Language != LangEnglish || doc.Detected {
		t.Errorf("Language = %q, Detected = %v; want %q, false", doc.Language, doc.Detected, LangEnglish)
	}
	// The(1) quick(1) brown(1) fox(1) jumps(1) over(2) the(1) lazy(2) dog(1) = 11
	// It(1) was(1) a(1) simple(2) test(1) = 6
	assertStats(t, doc.Stats, Stats{
		Sentences:           2,
		Words:               14,
		Syllables:           17,
		Characters:          51,
		PolysyllabicWords:   0,
		MonosyllabicWords:   11,
		LongWords:           0,
		AvgSentenceLength:   7,
		AvgSyllablesPerWord: 17.0 / 14.0,
		AvgWordLength:       51.0 / 14.0,
	})
	if len(doc.Sentences) != 2 {
		t.Fatalf("len(doc.Sentences) = %d, want 2", len(doc.Sentences))
	}
	if len(doc.Sentences[0].Words) != 9 || doc.Sentences[0].Words[0].Text != "The" {
		t.Errorf("first sentence words = %+v", doc.Sentences[0].Words)
	}
	if got := doc.Sentences[0].Words[5]; got.Text != "over" || got.Syllables != 2 || got.Runes != 4 {
		t.Errorf("word 'over' = %+v", got)
	}
}

func TestAnalyzeGermanParagraph(t *testing.T) {
	t.Parallel()
	const text = "Die Geschwindigkeit ist beeindruckend. Am 3. Oktober 1990 wurde Deutschland wiedervereinigt."
	doc, err := Analyze(text, LangGerman)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	// Die(1) Geschwindigkeit(4) ist(1) beeindruckend(4) = 10
	// Am(1) 3(1) Oktober(3) 1990(1) wurde(2) Deutschland(2) wiedervereinigt(5) = 15
	assertStats(t, doc.Stats, Stats{
		Sentences:           2,
		Words:               11,
		Syllables:           25,
		Characters:          79,
		PolysyllabicWords:   4,
		MonosyllabicWords:   5,
		LongWords:           5,
		AvgSentenceLength:   5.5,
		AvgSyllablesPerWord: 25.0 / 11.0,
		AvgWordLength:       79.0 / 11.0,
	})
	if len(doc.Sentences) != 2 {
		t.Fatalf("len(doc.Sentences) = %d, want 2", len(doc.Sentences))
	}
	// The ordinal "3." must not have ended the second sentence.
	if got := doc.Sentences[1].Text; got != "Am 3. Oktober 1990 wurde Deutschland wiedervereinigt." {
		t.Errorf("second sentence = %q", got)
	}
	// Numbers are words and count as one syllable.
	if got := doc.Sentences[1].Words[1]; got.Text != "3" || got.Syllables != 1 {
		t.Errorf("number word = %+v, want {3 1 1}", got)
	}
}

func TestAnalyzeEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		lang Language
		want Stats
	}{
		{"empty", "", LangEnglish, Stats{}},
		{"whitespace only", "   \n\n\t ", LangEnglish, Stats{}},
		{"punctuation only", "--- ... !!!", LangEnglish, Stats{}},
		{
			// A headline without terminal punctuation is one sentence, never
			// zero — otherwise every metric divides by zero.
			name: "headline without punctuation",
			in:   "Die Zukunft der Arbeit",
			lang: LangGerman,
			want: Stats{
				Sentences:           1,
				Words:               4,
				Syllables:           6, // Die(1) Zu-kunft(2) der(1) Ar-beit(2)
				Characters:          19,
				PolysyllabicWords:   0,
				MonosyllabicWords:   2,
				LongWords:           1, // "Zukunft" has 7 characters
				AvgSentenceLength:   4,
				AvgSyllablesPerWord: 6.0 / 4.0,
				AvgWordLength:       19.0 / 4.0,
			},
		},
		{
			name: "single word",
			in:   "Hello",
			lang: LangEnglish,
			want: Stats{
				Sentences:           1,
				Words:               1,
				Syllables:           2,
				Characters:          5,
				MonosyllabicWords:   0,
				AvgSentenceLength:   1,
				AvgSyllablesPerWord: 2,
				AvgWordLength:       5,
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			doc, err := Analyze(tt.in, tt.lang)
			if err != nil {
				t.Fatalf("Analyze(%q): %v", tt.in, err)
			}
			assertStats(t, doc.Stats, tt.want)
		})
	}
}

func TestAnalyzeNeverProducesNaN(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"", "    ", "...", "!!!", "\n\n\n", "42"} {
		doc, err := Analyze(in, LangEnglish)
		if err != nil {
			t.Fatalf("Analyze(%q): %v", in, err)
		}
		for name, v := range map[string]float64{
			"AvgSentenceLength":   doc.Stats.AvgSentenceLength,
			"AvgSyllablesPerWord": doc.Stats.AvgSyllablesPerWord,
			"AvgWordLength":       doc.Stats.AvgWordLength,
		} {
			if math.IsNaN(v) || math.IsInf(v, 0) {
				t.Errorf("Analyze(%q).Stats.%s = %v", in, name, v)
			}
		}
	}
}

func TestAnalyzeEmptyDoc(t *testing.T) {
	t.Parallel()
	doc, err := Analyze("", LangEnglish)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if !doc.Empty() {
		t.Error("Analyze(\"\").Empty() = false, want true")
	}
	if doc.Text != "" || doc.Sentences != nil {
		t.Errorf("unexpected doc contents: %+v", doc)
	}
}

func TestAnalyzeAutoDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		in   string
		want Language
	}{
		{"Der Hund läuft durch den Park und bellt laut.", LangGerman},
		{"The dog runs through the park and barks loudly.", LangEnglish},
		{"", LangEnglish},
	}
	for _, tt := range tests {
		doc, err := Analyze(tt.in, LangAuto)
		if err != nil {
			t.Fatalf("Analyze(%q, auto): %v", tt.in, err)
		}
		if doc.Language != tt.want {
			t.Errorf("Analyze(%q, auto).Language = %q, want %q", tt.in, doc.Language, tt.want)
		}
		if !doc.Detected {
			t.Errorf("Analyze(%q, auto).Detected = false, want true", tt.in)
		}
	}
}

func TestAnalyzeUnsupportedLanguage(t *testing.T) {
	t.Parallel()
	for _, lang := range []Language{"fr", "EN", "", "english"} {
		doc, err := Analyze("Bonjour le monde.", lang)
		if err == nil {
			t.Fatalf("Analyze(_, %q) = %+v, want error", lang, doc)
		}
		if doc != nil {
			t.Errorf("Analyze(_, %q) returned a doc alongside the error", lang)
		}
		var e *errs.E
		if !errors.As(err, &e) {
			t.Fatalf("Analyze(_, %q) error = %T, want *errs.E", lang, err)
		}
		if e.Code != errs.CodeUnsupportedLanguage {
			t.Errorf("error code = %q, want %q", e.Code, errs.CodeUnsupportedLanguage)
		}
		if e.Hint == "" {
			t.Error("error hint is empty")
		}
	}
}

func TestAnalyzeSupportedLanguages(t *testing.T) {
	t.Parallel()
	for _, lang := range Supported() {
		if _, err := Analyze("Ein Satz. Noch einer.", lang); err != nil {
			t.Errorf("Analyze(_, %q): %v", lang, err)
		}
	}
}

func TestAnalyzeSentenceOffsetsAndWords(t *testing.T) {
	t.Parallel()
	const text = "Der Test läuft.\n\nEr ist z.B. schnell und sehr gründlich!"
	doc, err := Analyze(text, LangGerman)
	if err != nil {
		t.Fatalf("Analyze: %v", err)
	}
	if len(doc.Sentences) != 2 {
		t.Fatalf("len(doc.Sentences) = %d, want 2: %q", len(doc.Sentences), sentenceTexts(doc.Sentences))
	}
	words := 0
	for _, s := range doc.Sentences {
		if text[s.Start:s.End] != s.Text {
			t.Errorf("offsets %d..%d do not match %q", s.Start, s.End, s.Text)
		}
		if len(s.Words) == 0 {
			t.Errorf("sentence %q has no words", s.Text)
		}
		for _, w := range s.Words {
			if w.Syllables < 1 {
				t.Errorf("word %q has %d syllables", w.Text, w.Syllables)
			}
			if w.Runes < 1 {
				t.Errorf("word %q has %d runes", w.Text, w.Runes)
			}
			if strings.ContainsAny(w.Text, " \t\n") {
				t.Errorf("word %q contains whitespace", w.Text)
			}
			words++
		}
	}
	if words != doc.Stats.Words {
		t.Errorf("sum of sentence words = %d, Stats.Words = %d", words, doc.Stats.Words)
	}
}

// benchDoc builds a ~100 KB synthetic German/English document.
func benchDoc() string {
	const para = "Die Verständlichkeit eines Textes hängt vor allem von der Länge seiner Sätze ab. " +
		"Kurze Sätze mit maximal 15 Wörtern sind deutlich leichter zu lesen als lange Schachtelsätze, " +
		"die z.B. mehrere Nebensätze enthalten. Am 3. Oktober 1990 wurde das anders bewertet, " +
		"aber die Grundregel gilt bis heute: schreibe klar, kurz und ohne Fachjargon!\n\n" +
		"Readability formulas count sentences, words and syllables. " +
		"They do not understand meaning, so they reward short words and short sentences. " +
		"Used well they are a quick smoke test for a draft, e.g. before it goes to review.\n\n"
	var b strings.Builder
	for b.Len() < 100*1024 {
		b.WriteString(para)
	}
	return b.String()
}

func BenchmarkAnalyze(b *testing.B) {
	text := benchDoc()
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		doc, err := Analyze(text, LangGerman)
		if err != nil {
			b.Fatal(err)
		}
		if doc.Stats.Words == 0 {
			b.Fatal("no words")
		}
	}
}

func BenchmarkAnalyzeAuto(b *testing.B) {
	text := benchDoc()
	b.SetBytes(int64(len(text)))
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if _, err := Analyze(text, LangAuto); err != nil {
			b.Fatal(err)
		}
	}
}
