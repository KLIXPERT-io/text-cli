// Package textproc turns raw text into the counts every readability metric
// needs: sentences, words, and syllables, per language.
//
// The package is deliberately dependency-free and allocation-light: analysing a
// document is a single pass over runes, and the resulting Doc is reused by
// every metric so a `--metrics flesch,amstad` run tokenizes once.
package textproc

// Language is an ISO 639-1 code. LangAuto asks for detection.
type Language string

const (
	LangAuto    Language = "auto"
	LangEnglish Language = "en"
	LangGerman  Language = "de"
)

// Word is a single token plus its syllable count for the document's language.
type Word struct {
	Text      string `json:"text"`
	Syllables int    `json:"syllables"`
	// Runes is the length in runes (not bytes), so umlauts count as one.
	Runes int `json:"runes"`
}

// Sentence is a span of words delimited by sentence-ending punctuation.
type Sentence struct {
	Text  string `json:"text"`
	Words []Word `json:"words,omitempty"`
	// Start and End are byte offsets into the source text.
	Start int `json:"start"`
	End   int `json:"end"`
}

// Stats are the aggregate counts metrics are computed from.
type Stats struct {
	Sentences  int `json:"sentences"`
	Words      int `json:"words"`
	Syllables  int `json:"syllables"`
	Characters int `json:"characters"`
	// PolysyllabicWords counts words of three or more syllables.
	PolysyllabicWords int `json:"polysyllabic_words"`
	// MonosyllabicWords counts words of exactly one syllable.
	MonosyllabicWords int `json:"monosyllabic_words"`
	// LongWords counts words longer than six characters (used by German
	// readability formulas).
	LongWords int `json:"long_words"`

	// AvgSentenceLength is words per sentence (ASL).
	AvgSentenceLength float64 `json:"avg_sentence_length"`
	// AvgSyllablesPerWord is syllables per word (ASW).
	AvgSyllablesPerWord float64 `json:"avg_syllables_per_word"`
	// AvgWordLength is characters per word.
	AvgWordLength float64 `json:"avg_word_length"`
}

// Doc is an analysed document: the source text, the language it was analysed
// in, and the counts derived from it.
type Doc struct {
	Text      string     `json:"-"`
	Language  Language   `json:"language"`
	Detected  bool       `json:"language_detected"`
	Sentences []Sentence `json:"-"`
	Stats     Stats      `json:"stats"`
}

// Empty reports whether the document has nothing measurable in it.
func (d *Doc) Empty() bool { return d == nil || d.Stats.Words == 0 || d.Stats.Sentences == 0 }
