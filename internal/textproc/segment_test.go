package textproc

import (
	"reflect"
	"testing"
)

func sentenceTexts(ss []Sentence) []string {
	out := make([]string, len(ss))
	for i, s := range ss {
		out[i] = s.Text
	}
	return out
}

func TestSplitSentences(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{
			name: "empty",
			in:   "",
			want: []string{},
		},
		{
			name: "whitespace only",
			in:   "   \n\t  ",
			want: []string{},
		},
		{
			name: "plain sentences",
			in:   "Hello world. This is a test.",
			want: []string{"Hello world.", "This is a test."},
		},
		{
			name: "no terminal punctuation is still one sentence",
			in:   "Die Zukunft der Arbeit",
			want: []string{"Die Zukunft der Arbeit"},
		},
		{
			name: "trailing sentence without punctuation",
			in:   "Erster Satz. Zweiter Satz ohne Punkt",
			want: []string{"Erster Satz.", "Zweiter Satz ohne Punkt"},
		},
		{
			name: "terminator runs",
			in:   "Really?! Yes... Of course!",
			want: []string{"Really?!", "Yes...", "Of course!"},
		},
		{
			name: "ellipsis rune",
			in:   "Und dann… Er ging.",
			want: []string{"Und dann…", "Er ging."},
		},
		{
			name: "blank line ends a sentence",
			in:   "Erste Zeile\n\nZweite Zeile",
			want: []string{"Erste Zeile", "Zweite Zeile"},
		},
		{
			name: "headline then paragraph",
			in:   "Wichtige Nachricht\n\nDas ist der Text. Und noch einer.",
			want: []string{"Wichtige Nachricht", "Das ist der Text.", "Und noch einer."},
		},
		{
			name: "single newline does not split without punctuation",
			in:   "ein langer Satz\nueber zwei Zeilen",
			want: []string{"ein langer Satz\nueber zwei Zeilen"},
		},
		{
			name: "newline after terminator",
			in:   "Line one.\nLine two.",
			want: []string{"Line one.", "Line two."},
		},
		{
			name: "english abbreviations",
			in:   "Dr. Smith met Mrs. Jones. They talked.",
			want: []string{"Dr. Smith met Mrs. Jones.", "They talked."},
		},
		{
			name: "english abbreviations with numbers",
			in:   "See Fig. 3 and Vol. 2 for details. No. 7 is missing.",
			want: []string{"See Fig. 3 and Vol. 2 for details.", "No. 7 is missing."},
		},
		{
			name: "dotted english abbreviations",
			in:   "The U.S. Army trains here. It is loud.",
			want: []string{"The U.S. Army trains here.", "It is loud."},
		},
		{
			name: "german abbreviations",
			in:   "Wir treffen uns z.B. am Montag bzw. am Dienstag. Das dauert ca. 3 Tage.",
			want: []string{"Wir treffen uns z.B. am Montag bzw. am Dienstag.", "Das dauert ca. 3 Tage."},
		},
		{
			name: "more german abbreviations",
			in:   "Siehe Abb. 4 und Nr. 12 usw. Das gilt d.h. immer. Vgl. dazu Kap. 2.",
			want: []string{"Siehe Abb. 4 und Nr. 12 usw. Das gilt d.h. immer.", "Vgl. dazu Kap. 2."},
		},
		{
			name: "german ordinals",
			in:   "Am 3. Oktober 1990 kam die Einheit. Im 19. Jahrhundert war das anders.",
			want: []string{"Am 3. Oktober 1990 kam die Einheit.", "Im 19. Jahrhundert war das anders."},
		},
		{
			name: "decimals",
			in:   "Pi ist 3.14 und nicht 3.15 oder mehr. Das ist klar.",
			want: []string{"Pi ist 3.14 und nicht 3.15 oder mehr.", "Das ist klar."},
		},
		{
			name: "date with year ends the sentence",
			in:   "Das Treffen war am 15. März 2020. Danach gingen alle.",
			want: []string{"Das Treffen war am 15. März 2020.", "Danach gingen alle."},
		},
		{
			// Trade-off of the ordinal rule: a short number before the dot is
			// read as an ordinal, so the two sentences stay joined.
			name: "short number before a dot never terminates",
			in:   "Der Wert ist 12. Danach kam nichts.",
			want: []string{"Der Wert ist 12. Danach kam nichts."},
		},
		{
			name: "german thousands separator",
			in:   "Der Preis beträgt 1.000,50 Euro inkl. Versand.",
			want: []string{"Der Preis beträgt 1.000,50 Euro inkl. Versand."},
		},
		{
			name: "version numbers",
			in:   "Nutze v1.2.3 heute. Morgen kommt v2.",
			want: []string{"Nutze v1.2.3 heute.", "Morgen kommt v2."},
		},
		{
			name: "domains and filenames",
			in:   "Visit example.com now. Open file.txt later.",
			want: []string{"Visit example.com now.", "Open file.txt later."},
		},
		{
			name: "initials",
			in:   "J. R. R. Tolkien wrote it. We read it.",
			want: []string{"J. R. R. Tolkien wrote it.", "We read it."},
		},
		{
			name: "closing quote belongs to the sentence",
			in:   `Er sagte: "Das ist gut." Dann ging er.`,
			want: []string{`Er sagte: "Das ist gut."`, "Dann ging er."},
		},
		{
			name: "german guillemets",
			in:   "Er rief: »Halt!« Dann war Ruhe.",
			want: []string{"Er rief: »Halt!«", "Dann war Ruhe."},
		},
		{
			name: "closing bracket belongs to the sentence",
			in:   "Das steht im Anhang (siehe dort.) Alles klar.",
			want: []string{"Das steht im Anhang (siehe dort.)", "Alles klar."},
		},
		{
			name: "lower case start does not open a sentence",
			in:   "Das ist gut. und das auch.",
			want: []string{"Das ist gut. und das auch."},
		},
		{
			name: "quoted sentence start",
			in:   `Sie fragte. "Warum denn?"`,
			want: []string{"Sie fragte.", `"Warum denn?"`},
		},
		{
			name: "multiple blank lines",
			in:   "Absatz eins.\n\n\n   \n\nAbsatz zwei.",
			want: []string{"Absatz eins.", "Absatz zwei."},
		},
		{
			name: "surrounding whitespace is trimmed",
			in:   "\n\n  Ein Satz.  \n\n",
			want: []string{"Ein Satz."},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := sentenceTexts(SplitSentences(tt.in))
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SplitSentences(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSentenceOffsets(t *testing.T) {
	t.Parallel()
	inputs := []string{
		"Hallo Welt. Wie geht es dir?",
		"\n\n  Größe: 1.000,50 €.  Zweiter Satz!\n\nDritter Satz",
		"Am 3. Oktober 1990 kam die Einheit. Im 19. Jahrhundert war das anders.",
		"Kein Satzende hier",
	}
	for _, in := range inputs {
		prevEnd := -1
		for _, s := range SplitSentences(in) {
			if s.Start < 0 || s.End > len(in) || s.Start >= s.End {
				t.Fatalf("SplitSentences(%q): bad offsets %d..%d", in, s.Start, s.End)
			}
			if in[s.Start:s.End] != s.Text {
				t.Errorf("SplitSentences(%q): text[%d:%d] = %q, want %q", in, s.Start, s.End, in[s.Start:s.End], s.Text)
			}
			if s.Start < prevEnd {
				t.Errorf("SplitSentences(%q): sentence at %d overlaps previous end %d", in, s.Start, prevEnd)
			}
			prevEnd = s.End
		}
	}
}

func TestSplitWords(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want []string
	}{
		{"empty", "", nil},
		{"punctuation only", "--- *** ... !?", nil},
		{"simple", "Hello, world!", []string{"Hello", "world"}},
		{"leading and trailing punctuation", "»Zitat«, (klar)?", []string{"Zitat", "klar"}},
		{"umlauts and eszett", "Größe der Straße in Köln", []string{"Größe", "der", "Straße", "in", "Köln"}},
		{"apostrophes", "don't stop, it's fine", []string{"don't", "stop", "it's", "fine"}},
		{"typographic apostrophe", "it’s l’année", []string{"it’s", "l’année"}},
		{"hyphenated compounds", "e-mail und Software-Entwicklung", []string{"e-mail", "und", "Software-Entwicklung"}},
		{"dangling hyphen", "well- known —dash—", []string{"well", "known", "dash"}},
		{"numbers are words", "42 Äpfel und 3.14 sowie 1.000,50 Euro", []string{"42", "Äpfel", "und", "3.14", "sowie", "1.000,50", "Euro"}},
		{"sentence dot is not part of a word", "Ende. Anfang", []string{"Ende", "Anfang"}},
		{"accented latin", "l'été à Genève", []string{"l'été", "à", "Genève"}},
		{"newlines and tabs", "eins\tzwei\ndrei", []string{"eins", "zwei", "drei"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := SplitWords(tt.in)
			if len(got) == 0 && len(tt.want) == 0 {
				return
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("SplitWords(%q)\n got: %q\nwant: %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestSupported(t *testing.T) {
	t.Parallel()
	got := Supported()
	want := []Language{LangEnglish, LangGerman}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Supported() = %v, want %v", got, want)
	}
	got[0] = LangAuto
	if Supported()[0] != LangEnglish {
		t.Error("Supported() returns a shared slice that callers can mutate")
	}
}
