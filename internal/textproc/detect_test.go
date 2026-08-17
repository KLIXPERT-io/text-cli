package textproc

import "testing"

func TestDetect(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		in   string
		want Language
	}{
		// Short German sentences.
		{"de short", "Das ist ein Test.", LangGerman},
		{"de everyday", "Ich gehe heute nach Hause.", LangGerman},
		{"de umlauts", "Die Geschwindigkeit der Übertragung ist beeindruckend.", LangGerman},
		{"de no umlauts", "Wir treffen uns morgen in dem grossen Haus.", LangGerman},
		{"de suffixes", "Lesbarkeit ist wichtig für die Verstaendlichkeit von Texten.", LangGerman},
		{"de question", "Wo hast du das Buch gekauft?", LangGerman},
		{"de paragraph", "Der Hund läuft schnell durch den Park. Danach trinkt er Wasser.", LangGerman},

		// Short English sentences.
		{"en short", "This is a test.", LangEnglish},
		{"en pangram", "The quick brown fox jumps over the lazy dog.", LangEnglish},
		{"en gerund", "I am going home today.", LangEnglish},
		{"en prose", "Readability matters for the quality of your writing.", LangEnglish},
		{"en contraction", "She said that the meeting was over and we don't mind.", LangEnglish},
		{"en question", "Where did you buy that book?", LangEnglish},

		// Capitalisation alone must never decide German: an English sentence
		// full of proper nouns collects no lexical German evidence, so it has
		// to fall back to English.
		{"en proper nouns", "Ada Lovelace worked in London.", LangEnglish},
		{"en more proper nouns", "Barack Obama visited Berlin and Paris.", LangEnglish},
		{"en proper nouns no function words", "Tokyo Osaka Kyoto Nagoya Sapporo Fukuoka", LangEnglish},

		// German still wins on lexical evidence alone, with or without capitals.
		{"de function word only", "Der Hund bellt laut.", LangGerman},
		{"de suffix and umlaut only", "Wissenschaftliche Untersuchungen belegen Verständlichkeitsprobleme.", LangGerman},
		{"de capitals as tie-breaker", "Der Hund von Ada Lovelace bellt in London laut.", LangGerman},

		// No signal at all falls back to English.
		{"empty", "", LangEnglish},
		{"whitespace", "   \n\t", LangEnglish},
		{"digits", "42 1990 3.14", LangEnglish},
		{"two words", "Hello world", LangEnglish},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Detect(tt.in); got != tt.want {
				t.Errorf("Detect(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestDetectLongDocument(t *testing.T) {
	t.Parallel()
	de := repeatText("Die Verständlichkeit eines Textes hängt von der Länge der Sätze ab. "+
		"Kurze Sätze sind leichter zu lesen als lange Schachtelsätze. ", 200)
	if got := Detect(de); got != LangGerman {
		t.Errorf("Detect(long german) = %q, want %q", got, LangGerman)
	}
	en := repeatText("The readability of a text depends on the length of its sentences. "+
		"Short sentences are easier to read than long nested ones. ", 200)
	if got := Detect(en); got != LangEnglish {
		t.Errorf("Detect(long english) = %q, want %q", got, LangEnglish)
	}
}

func repeatText(s string, n int) string {
	buf := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		buf = append(buf, s...)
	}
	return string(buf)
}
