package textproc

import "testing"

func TestSyllablesEnglish(t *testing.T) {
	t.Parallel()
	tests := []struct {
		word string
		want int
	}{
		// The canonical verification list.
		{"the", 1},
		{"syllable", 3},
		{"beautiful", 3},
		{"area", 3},
		{"idea", 3},
		{"business", 2},
		{"everything", 4},
		{"science", 2},
		{"reading", 2},
		{"queue", 1},
		{"people", 2},
		{"simple", 2},
		{"created", 3},
		{"ocean", 2},
		{"poem", 2},
		// "rhythm": documented choice. The only vowel letter is the 'y', so the
		// heuristic says 1. The syllabic 'm' of rhy-thm would make it 2; we do
		// not model syllabic consonants because the rule misfires on "prism",
		// "spasm", "film" and friends.
		{"rhythm", 1},
		{"university", 5},
		// "readability": read·a·bil·i·ty = 5 (CMU: R IY2 D AH0 B IH1 L AH0 T IY0).
		// The brief lists 6; that is a miscount, so we keep the correct value.
		{"readability", 5},

		// Silent trailing e, and the cases where it is not silent.
		{"make", 1},
		{"code", 1},
		{"be", 1},
		{"table", 2},
		{"little", 2},
		{"apples", 2},
		{"whole", 1},
		{"coffee", 2},
		// -es / -ed endings.
		{"asked", 1},
		{"played", 1},
		{"wanted", 2},
		{"needed", 2},
		{"passes", 2},
		{"boxes", 2},
		{"watches", 2},
		{"races", 2},
		{"makes", 1},
		{"agreed", 2},
		// y as a vowel, but not word-initially.
		{"yes", 1},
		{"myth", 1},
		{"day", 1},
		{"lazy", 2},
		// Assorted prose words.
		{"a", 1},
		{"I", 1},
		{"hello", 2},
		{"language", 2},
		{"readable", 3},
		{"over", 2},
		{"quick", 1},
		{"media", 3},
		{"radio", 3},
		{"sea", 1},
		{"flea", 1},
		// Accented letters are folded to their base vowel, not dropped.
		{"über", 2},
		{"señor", 2},
	}
	for _, tt := range tests {
		if got := Syllables(tt.word, LangEnglish); got != tt.want {
			t.Errorf("Syllables(%q, en) = %d, want %d", tt.word, got, tt.want)
		}
	}
}

func TestSyllablesGerman(t *testing.T) {
	t.Parallel()
	tests := []struct {
		word string
		want int
	}{
		// The canonical verification list.
		{"Haus", 1},
		{"Häuser", 2},
		{"Auto", 2},
		{"Sonne", 2},
		{"Beispiel", 2},
		{"Deutschland", 2},
		{"Wissenschaft", 3},
		{"Geschwindigkeit", 4},
		{"Universität", 5},
		{"Lesbarkeit", 3},
		{"verständlich", 3},
		{"Straße", 2},
		// "Möglichkeit": Mög·lich·keit = 3. The brief lists 4; that is a
		// miscount (there are exactly three vowel nuclei), so we keep 3.
		{"Möglichkeit", 3},
		{"ein", 1},
		{"eine", 2},
		// "Familie": documented trade-off. We treat "ie" as one nucleus, which
		// yields Fa-mi-lie = 3 (the brief accepts 3 or 4).
		{"Familie", 3},
		// Do·nau·dampf·schiff·fahrt = 5 nuclei (o, au, a, i, a). The brief
		// lists 6; kept at the correct 5.
		{"Donaudampfschifffahrt", 5},

		// Diphthongs and digraphs are one nucleus.
		{"Frauen", 2},
		{"heute", 2},
		{"Bayern", 2},
		{"Boot", 1},
		{"Haar", 1},
		{"Tee", 1},
		{"Idee", 2},
		{"Beere", 2},
		// Vowel + h stays one syllable.
		{"geht", 1},
		{"Bahn", 1},
		{"sieht", 1},
		{"ihn", 1},
		// Trailing e is a full syllable in German.
		{"Hase", 2},
		{"Ende", 2},
		{"Katze", 2},
		// "ie": one nucleus, except the "-ien" plural.
		{"Chemie", 2},
		{"Melodie", 3},
		{"Batterie", 3},
		{"Ferien", 3},
		{"Familien", 4},
		{"Studien", 3},
		{"Wien", 1},
		// qu is a consonant cluster.
		{"Quelle", 2},
		// Ordinary prose.
		{"und", 1},
		{"der", 1},
		{"ist", 1},
		{"Wörterbuch", 3},
		{"Übertragung", 4},
		{"schön", 1},
		{"System", 2},
	}
	for _, tt := range tests {
		if got := Syllables(tt.word, LangGerman); got != tt.want {
			t.Errorf("Syllables(%q, de) = %d, want %d", tt.word, got, tt.want)
		}
	}
}

func TestSyllablesEdgeCases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		word string
		lang Language
		want int
	}{
		{"empty english", "", LangEnglish, 0},
		{"empty german", "", LangGerman, 0},
		{"digits only", "42", LangEnglish, 0},
		{"digits only german", "1990", LangGerman, 0},
		{"punctuation only", "---", LangEnglish, 0},
		{"single letter", "x", LangEnglish, 1},
		{"consonants only", "GmbH", LangGerman, 1},
		{"consonants only english", "hmm", LangEnglish, 1},
		{"unknown script", "привет", LangEnglish, 1},
		{"auto falls back to english", "make", LangAuto, 1},
		{"apostrophe kept", "don't", LangEnglish, 1},
		{"hyphenated compound", "e-mail", LangEnglish, 2},
		{"german compound", "Software-Entwicklung", LangGerman, 5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Syllables(tt.word, tt.lang); got != tt.want {
				t.Errorf("Syllables(%q, %s) = %d, want %d", tt.word, tt.lang, got, tt.want)
			}
		})
	}
}
