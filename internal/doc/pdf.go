package doc

import (
	"bytes"
	"math"
	"sort"
	"strings"
	"unicode"

	"github.com/KLIXPERT-io/text-cli/internal/errs"
	"github.com/ledongthuc/pdf"
)

// FormatPDF is the registry name of the PDF decoder.
const FormatPDF = "pdf"

func init() {
	Register(FormatPDF, func() (Decoder, error) { return pdfDecoder{}, nil })
}

// PDF is the only format in this package that has no document structure to
// read. A DOCX says "this paragraph is Heading 2"; a PDF says "draw this glyph
// at these coordinates". Everything below — lines, paragraphs, headings, the
// running header that must not appear thirteen times in the prose — is
// *reconstructed* from geometry, and reconstructing it is the whole job.
//
// Getting it wrong is not a cosmetic failure. A decoder that concatenates
// glyphs without rebuilding paragraph boundaries hands the tokenizer one
// sentence per page, and every per-sentence average this CLI computes becomes
// a number about the page size rather than about the writing.

// pdfMagic is the header every PDF starts with. The version digits vary, so
// only the prefix is matched.
var pdfMagic = []byte("%PDF-")

type pdfDecoder struct{}

func (pdfDecoder) Name() string { return FormatPDF }

func (pdfDecoder) Extensions() []string { return []string{".pdf"} }

func (pdfDecoder) Sniff(data []byte) bool { return bytes.HasPrefix(data, pdfMagic) }

// ---------------------------------------------------------------------------
// Tunable geometry
//
// Every constant here is a threshold on a ratio, never on an absolute number of
// points, because a 24pt title and a 9pt footnote have to be judged by the same
// rule. They are named rather than inlined so the reasoning below can refer to
// them.
// ---------------------------------------------------------------------------

const (
	// pdfLineTol is how far apart two glyph baselines may sit and still belong
	// to one line, as a fraction of the font size. It has to absorb subscripts
	// and a mixed-size line (a 7pt label beside 11pt prose) without swallowing
	// the next line, whose baseline is a whole leading away.
	pdfLineTol = 0.3

	// pdfAvgAdvance is the assumed width of one glyph, in ems, used only when
	// the font's widths cannot be resolved. See pdfRun for why a guess is
	// enough here and a real width is not needed.
	pdfAvgAdvance = 0.5

	// pdfGapSpace and pdfGapSlack decide when the gap between two text runs is
	// a word break rather than kerning. The slack term scales with the width of
	// the run being measured because the error in an estimated width does too.
	pdfGapSpace = 0.6
	pdfGapSlack = 0.15

	// pdfGapKnown is the same decision when the pen position is not estimated
	// but known — the font resolved a real advance for the previous glyph.
	//
	// It has to be a different, much smaller number, and getting that wrong is
	// expensive: a TeX-produced PDF encodes no space characters at all, setting
	// each word as its own run and leaving the word break entirely to the
	// coordinates. A space is 0.25em in a Times-like face and 0.28em in a
	// Helvetica-like one, so a 0.6em tolerance swallows every one of them and a
	// whole page arrives as "Manyprogramsanddesktopsuse". Kerning between two
	// glyphs of one word is an order of magnitude smaller than a space —
	// 0.02em is a wide kern — so 0.18em sits far below the narrowest space and
	// far above the widest kern, and neither side is close.
	pdfGapKnown = 0.18

	// pdfParaBreak is how much bigger than the document's usual line spacing a
	// vertical gap must be to end a paragraph. Ordinary leading lands near 1.2
	// to 1.6 times the font size and paragraph spacing at twice that, so the
	// midpoint is forgiving in both directions.
	pdfParaBreak = 1.35

	// pdfHeadingRatio is how much larger than the body text a line must be to
	// be a heading. Set low enough to catch a 13pt subhead over 10.5pt prose
	// and high enough that a 12pt lead paragraph stays a paragraph.
	pdfHeadingRatio = 1.25

	// pdfSizeSame is the point difference below which two lines count as the
	// same size. Font sizes come out of a matrix multiplication, so the same
	// nominal size differs in the last digits between lines.
	pdfSizeSame = 0.6

	// pdfMarginLines is how many lines at each end of a page are eligible to be
	// a running header or footer. Two rather than one because a header is often
	// a logo line plus a section line.
	pdfMarginLines = 2

	// pdfRepeatPages is the smallest number of pages a line must repeat on
	// before it is treated as furniture, and pdfRepeatShare the fraction of
	// pages it must cover. Both have to hold: three pages of a long report is
	// not evidence, and half of a four-page report is not either.
	pdfRepeatPages = 3
	pdfRepeatShare = 0.6

	// pdfMinPageBytes is the smallest number of file bytes one page can possibly
	// cost, and it is what the declared page count is checked against.
	//
	// NumPage returns /Root/Pages/Count verbatim: a number in the file, not a
	// number of pages the file contains. A truncated download or a crafted PDF
	// declaring two billion pages sends the decode loop round two billion times
	// walking a page tree that holds one — no allocation, no panic, no progress,
	// and nothing above notices, because a CLI that is merely slow looks exactly
	// like a CLI that is working. Bounding it by the file's own size is what
	// makes the count checkable rather than believed. A page needs a dictionary,
	// an entry in the page tree and an xref or object-stream slot; eight bytes is
	// far below what even the densest compressed object stream achieves, so the
	// bound cannot refuse a real document — a 1 MiB PDF is allowed 131,072 pages,
	// and 1 MiB of real PDF holds a few hundred.
	pdfMinPageBytes = 8

	// pdfMissingRun is how many consecutive absent pages end the read.
	//
	// The page tree answers an index it does not hold with a null page, so a
	// declared count larger than the tree costs one tree walk per phantom page
	// and yields nothing. One absent page is not proof — an inconsistent /Count
	// on an intermediate node leaves a hole in a file that is otherwise readable
	// end to end, and stopping at the hole would drop the rest of a document the
	// user can see. A run this long is proof, and no real page tree contains one.
	pdfMissingRun = 64
)

// ---------------------------------------------------------------------------
// Decode
// ---------------------------------------------------------------------------

func (d pdfDecoder) Decode(data []byte) (*Document, error) {
	r, err := pdfOpen(data)
	if err != nil {
		return nil, err
	}

	pages, err := pdfNumPages(r, len(data))
	if err != nil {
		return nil, err
	}

	var lines []*pdfLine
	failed, missing := 0, 0
	for p := 1; p <= pages; p++ {
		glyphs, ok, present := pdfPageGlyphs(r, p)
		if !present {
			// The tree holds no page at this index. See pdfMissingRun.
			if missing++; missing >= pdfMissingRun {
				break
			}
			continue
		}
		missing = 0
		if !ok {
			// One unreadable page costs one page, not the document. A report
			// with a corrupt embedded font on page 9 is still worth scoring,
			// and the alternative — refusing the file — leaves the user with
			// no way to read the other twelve pages.
			failed++
			continue
		}
		lines = append(lines, pdfPageLines(p, glyphs)...)
	}
	if failed > 0 && failed == pages {
		return nil, errs.Newf(errs.CodeInvalidArgs, "no page of this PDF could be read").
			WithHint("The file may be corrupt or use an unsupported encoding. Re-export it, or convert it with another tool first.")
	}

	lines = pdfDropFurniture(lines, pages)
	if len(lines) == 0 {
		return nil, EmptyErr(FormatPDF, "")
	}

	var b mdBuilder
	pdfBuild(&b, lines)
	if b.Empty() {
		return nil, EmptyErr(FormatPDF, "")
	}

	return &Document{
		Content: b.String(),
		Title:   pdfTitle(r),
		Pages:   pages,
	}, nil
}

// pdfOpen parses the container.
//
// The reader panics rather than returning an error on a malformed object, so
// every entry point into the library in this file is wrapped. Turning that
// panic into an *errs.E is not defensive programming for its own sake: the
// input is an arbitrary file named by the user, and a CLI that stack-traces on
// a truncated download is reporting a bug in itself for a problem in the file.
func pdfOpen(data []byte) (r *pdf.Reader, err error) {
	defer pdfRecover(&err, "PDF structure is unreadable")

	data = pdfNormalizeVersion(data)
	r, err = pdf.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		msg := err.Error()
		e := errs.Newf(errs.CodeInvalidArgs, "cannot read PDF: %s", msg)
		if strings.Contains(strings.ToLower(msg), "password") || strings.Contains(msg, "encrypt") {
			return nil, e.WithHint("The document is password-protected. Remove the password and try again.")
		}
		return nil, e.WithHint("The file may be truncated or not a PDF. Check it opens in a viewer first.")
	}
	return r, nil
}

// pdfNormalizeVersion rewrites a PDF 2.x header to 1.7 in a copy of the bytes.
//
// The reader accepts %PDF-1.0 through %PDF-1.7 and refuses anything else, so a
// PDF 2.0 file — which Word, LibreOffice and most browsers now emit — would be
// rejected outright for a version number. The parts of the file this decoder
// touches, the cross-reference table and the content streams, are unchanged in
// 2.0; the additions are encryption and metadata this package does not read.
// The rewrite is length-preserving on purpose: every offset in the xref table
// is a byte offset into this buffer, and shifting them would corrupt the file
// far more thoroughly than the version byte ever could.
func pdfNormalizeVersion(data []byte) []byte {
	const n = len("%PDF-1.7")
	if len(data) < n || !bytes.HasPrefix(data, pdfMagic) {
		return data
	}
	if data[5] == '1' {
		return data
	}
	out := make([]byte, len(data))
	copy(out, data)
	out[5], out[6], out[7] = '1', '.', '7'
	return out
}

// pdfNumPages returns the page count, refusing one the file is too small to
// back. See pdfMinPageBytes for why the declared number cannot be trusted.
func pdfNumPages(r *pdf.Reader, size int) (n int, err error) {
	defer pdfRecover(&err, "PDF page tree is unreadable")
	n = r.NumPage()
	if n <= 0 {
		return 0, errs.Newf(errs.CodeInvalidArgs, "PDF declares no pages").
			WithHint("The file may be truncated. Check it opens in a viewer first.")
	}
	if max := size / pdfMinPageBytes; n > max {
		return 0, errs.Newf(errs.CodeInvalidArgs, "PDF declares %d pages but the file is only %d bytes", n, size).
			WithHint("The page count is corrupt. The file may be truncated — check it opens in a viewer first.")
	}
	return n, nil
}

// pdfPageGlyphs returns one Text per glyph drawn on the page, in the order the
// content stream draws them. ok is false if the page could not be interpreted;
// present is false if the page tree holds nothing at this index at all, which
// is a different thing and is what bounds the loop above.
func pdfPageGlyphs(r *pdf.Reader, page int) (out []pdf.Text, ok, present bool) {
	defer func() {
		if recover() != nil {
			out, ok, present = nil, false, true
		}
	}()
	p := r.Page(page)
	if p.V.IsNull() {
		return nil, true, false
	}
	return p.Content().Text, true, true
}

func pdfTitle(r *pdf.Reader) (title string) {
	defer func() {
		if recover() != nil {
			title = ""
		}
	}()
	return strings.TrimSpace(cleanInline(r.Trailer().Key("Info").Key("Title").Text()))
}

// pdfRecover converts a panic from the PDF reader into an *errs.E. An error the
// caller already produced wins, because it is the more specific one.
func pdfRecover(err *error, what string) {
	if v := recover(); v != nil && *err == nil {
		*err = errs.Newf(errs.CodeInvalidArgs, "%s", what).
			WithHint("The file may be corrupt or use an unsupported encoding. Re-export it and try again.")
	}
}

// ---------------------------------------------------------------------------
// Glyphs to lines
// ---------------------------------------------------------------------------

// pdfLine is one visual line of text on one page.
type pdfLine struct {
	page int
	y    float64 // baseline, increasing upwards
	size float64 // the largest font size on the line
	text string

	// run tracks the text run currently being appended to. See pdfRun.
	run pdfRun
	buf strings.Builder
}

// pdfRun is the state needed to tell a word break from kerning.
//
// A PDF positions text by run, not by glyph: a Td or Tm sets the pen and the
// glyphs that follow advance from there. The library reports the pen position
// for every glyph, but it can only advance the pen when it can resolve the
// font's widths — and for a subset-embedded font, or a base-14 font with no
// Widths array, it cannot. Both real-world files this was built against report
// a width of zero for every glyph, which means every glyph in a run carries the
// run's *starting* X and the X coordinate says nothing about where a glyph
// actually sits.
//
// That is survivable, because the coordinate still says exactly one thing that
// matters: where each run starts. Words inside a run are separated by real
// space characters in the stream. What the stream does not contain is the gap
// between two runs — between a table's first and second column, or between a
// logo and the page header beside it — and joining those without a space is
// what glues "Abs. 3" to "Mittleres Unternehmen". So the only decision made
// from geometry is whether consecutive runs are adjacent or separated, and the
// run's own width is estimated from its glyph count when it cannot be summed.
// A wrong estimate costs one space between two runs that were mid-word, which
// is rare; not estimating at all costs a fabricated compound word every time a
// document has a table.
type pdfRun struct {
	startX float64
	glyphs int
	size   float64
	ended  float64 // pen X after the last glyph, when widths are known
	widths bool    // whether this run's glyphs reported real widths
	// exact records whether the *last* glyph resolved a real width, which is
	// what decides how much the gap after it may be trusted. It is separate
	// from widths because a run can mix the two — a base-14 font beside an
	// embedded one — and a run that has seen one real width must not go on
	// measuring later gaps as if it still knew where the pen was.
	exact bool
}

// end returns where the run is estimated to finish.
func (r pdfRun) end() float64 {
	if r.widths {
		return r.ended
	}
	return r.startX + pdfAvgAdvance*r.size*float64(r.glyphs)
}

// slack is the tolerance on that estimate: a fixed word-space plus a share of
// the estimated width, because a longer estimate is a less certain one.
func (r pdfRun) slack() float64 {
	if r.exact {
		return pdfGapKnown * r.size
	}
	if r.widths {
		return pdfGapSpace * r.size
	}
	return pdfGapSpace*r.size + pdfGapSlack*pdfAvgAdvance*r.size*float64(r.glyphs)
}

// pdfPageLines groups a page's glyphs into lines.
//
// Glyphs are consumed in content-stream order and never re-sorted within a
// line. Sorting by X is the textbook approach and it is wrong here for the same
// reason the run comment gives: with unresolvable widths every glyph in a run
// reports the run's starting X, so an X sort shuffles a sentence into
// alphabetical-by-accident order. Stream order is what the writer meant.
//
// Lines, on the other hand, *are* re-sorted, by descending Y. A writer is free
// to draw the footer before the body — both sample documents do — and the
// reading order of a page is top to bottom whatever order the boxes were
// emitted in.
func pdfPageLines(page int, glyphs []pdf.Text) []*pdfLine {
	var lines []*pdfLine
	for _, g := range glyphs {
		s := pdfGlyphText(g.S)
		if s == "" {
			continue
		}
		l := pdfLineFor(lines, g)
		if l == nil {
			l = &pdfLine{page: page, y: g.Y, size: g.FontSize}
			l.run = pdfRun{startX: g.X, size: g.FontSize}
			lines = append(lines, l)
		} else if pdfNewRun(l, g) {
			// A jump the previous run does not account for: the pen was moved,
			// and a moved pen between two glyphs is a word or column boundary.
			l.buf.WriteByte(' ')
			l.run = pdfRun{startX: g.X, size: g.FontSize}
		}
		l.run.glyphs++
		if g.W > 0 {
			l.run.widths, l.run.exact = true, true
			l.run.ended = g.X + g.W
		} else if l.run.widths {
			// A glyph with no width inside a run that had them: keep the pen
			// moving on the estimate, or the following glyph reads as a jump
			// and a word gains a space in the middle of it.
			l.run.exact = false
			l.run.ended = g.X + pdfAvgAdvance*g.FontSize
		}
		if g.FontSize > l.size {
			l.size = g.FontSize
		}
		if g.FontSize > l.run.size {
			l.run.size = g.FontSize
		}
		l.buf.WriteString(s)
	}

	out := lines[:0]
	for _, l := range lines {
		l.text = cleanInline(l.buf.String())
		if l.text != "" {
			out = append(out, l)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].y > out[j].y })
	return out
}

// pdfLineFor finds the line a glyph belongs to, or nil for a new one. The scan
// is backwards because the glyph almost always belongs to the line most
// recently written to.
func pdfLineFor(lines []*pdfLine, g pdf.Text) *pdfLine {
	for i := len(lines) - 1; i >= 0; i-- {
		l := lines[i]
		tol := pdfLineTol * math.Max(l.size, g.FontSize)
		if math.Abs(l.y-g.Y) <= tol {
			return l
		}
	}
	return nil
}

// pdfNewRun reports whether a glyph starts far enough right of where the
// current run should have ended to count as a separate run.
func pdfNewRun(l *pdfLine, g pdf.Text) bool {
	if l.buf.Len() == 0 {
		return false
	}
	return g.X-l.run.end() > l.run.slack()
}

// pdfLigatures are the Latin typographic ligatures a typesetter substitutes for
// a letter pair, expanded back into their letters.
//
// A PDF is the only input in this repo that carries them, because it records
// what was printed rather than what was written: TeX sets "specification" with
// one ﬁ glyph, and that glyph reaches us as U+FB01. Left alone it is one
// character where the document has two, and — worse for this CLI — it holds no
// ASCII vowel, so the syllable counter reads "ﬁrm" as vowelless and every
// reading-ease score that divides by syllables drifts. Expanding is not
// normalisation for its own sake: it restores the word the author typed.
var pdfLigatures = strings.NewReplacer(
	"ﬀ", "ff", "ﬁ", "fi", "ﬂ", "fl", "ﬃ", "ffi",
	"ﬄ", "ffl", "ﬅ", "st", "ﬆ", "st",
)

// pdfGlyphText filters one decoded glyph.
//
// U+FFFD is what the reader emits for a code the font's ToUnicode map does not
// cover, and both sample documents produce one at the end of every text run.
// It is dropped rather than kept or turned into a space: it is never prose, it
// would be counted as a word by the tokenizer, and turning it into a space
// splits a word that the run-gap rule above would have joined correctly.
func pdfGlyphText(s string) string {
	if s == "" {
		return ""
	}
	s = pdfLigatures.Replace(s)
	if !strings.ContainsRune(s, unicode.ReplacementChar) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if r == unicode.ReplacementChar {
			return -1
		}
		return r
	}, s)
}

// ---------------------------------------------------------------------------
// Running headers and footers
// ---------------------------------------------------------------------------

// pdfDropFurniture removes the running header and footer.
//
// This is not tidying. A thirteen-page report with "AUDITSHIELD.EU 04 / 13" at
// the foot of every page contributes thirteen copies of that line to the prose,
// and every one of them is a short, punctuation-free "sentence" dragging the
// average sentence length down and the reading ease up. The user did not write
// them and should not be scored on them.
//
// A line is furniture when it sits within the first or last few lines of its
// page *and* the same line, with its numbers masked, appears on most of the
// document's pages. Both halves are needed: position alone would delete a
// heading that happens to open a page, and repetition alone would delete a
// refrain. Short documents are left alone entirely, because three pages is not
// enough evidence that anything repeats on purpose.
func pdfDropFurniture(lines []*pdfLine, pages int) []*pdfLine {
	if pages < pdfRepeatPages {
		return lines
	}

	byPage := map[int][]*pdfLine{}
	for _, l := range lines {
		byPage[l.page] = append(byPage[l.page], l)
	}

	// Count the pages each margin line appears on, not the occurrences: a line
	// printed twice on one page is not evidence of a running header.
	seen := map[string]map[int]bool{}
	margin := map[*pdfLine]bool{}
	for page, pl := range byPage {
		for _, l := range pdfMarginOf(pl) {
			margin[l] = true
			key := pdfFurnitureKey(l.text)
			if seen[key] == nil {
				seen[key] = map[int]bool{}
			}
			seen[key][page] = true
		}
	}

	need := int(math.Ceil(pdfRepeatShare * float64(len(byPage))))
	if need < pdfRepeatPages {
		need = pdfRepeatPages
	}

	out := lines[:0]
	for _, l := range lines {
		if margin[l] && len(seen[pdfFurnitureKey(l.text)]) >= need {
			continue
		}
		out = append(out, l)
	}
	return out
}

// pdfMarginOf returns the lines at the top and bottom of one page.
func pdfMarginOf(page []*pdfLine) []*pdfLine {
	n := pdfMarginLines
	if len(page) <= 2*n {
		return page
	}
	out := make([]*pdfLine, 0, 2*n)
	out = append(out, page[:n]...)
	return append(out, page[len(page)-n:]...)
}

// pdfFurnitureKey masks the parts of a running header that legitimately change
// from page to page — the page number, the date — so "04 / 13" and "05 / 13"
// are recognised as one header rather than thirteen distinct lines.
func pdfFurnitureKey(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	digit := false
	for _, r := range strings.ToLower(s) {
		if unicode.IsDigit(r) {
			if !digit {
				b.WriteByte('#')
				digit = true
			}
			continue
		}
		digit = false
		b.WriteRune(r)
	}
	return b.String()
}

// ---------------------------------------------------------------------------
// Lines to markdown
// ---------------------------------------------------------------------------

// pdfBuild turns the surviving lines into blocks.
func pdfBuild(b *mdBuilder, lines []*pdfLine) {
	body := pdfBodySize(lines)
	levels := pdfHeadingLevels(lines, body)
	lead := pdfLeading(lines)

	var (
		text    string
		size    float64
		item    bool
		ordered bool
		open    bool
		prev    *pdfLine
	)
	flush := func() {
		if !open {
			return
		}
		switch {
		case item:
			b.Item(0, ordered, text)
		case size >= body*pdfHeadingRatio:
			b.Heading(levels[pdfSizeKey(size)], text)
		default:
			b.Para(text)
		}
		open = false
	}

	for _, l := range lines {
		marker, isOrdered, rest := pdfMarker(l.text)
		if !open || pdfBreaks(prev, l, lead) || marker {
			flush()
			text, size, item, ordered, open = rest, l.size, marker, isOrdered, true
		} else {
			text = pdfJoin(text, l.text)
			if l.size > size {
				size = l.size
			}
		}
		prev = l
	}
	flush()
}

// pdfBreaks reports whether a line starts a new block.
func pdfBreaks(prev, cur *pdfLine, lead float64) bool {
	if prev == nil {
		return false
	}
	if math.Abs(prev.size-cur.size) > pdfSizeSame {
		// A size change is a structural change: a heading, a caption, a table
		// label. It is the one signal a PDF gives away for free.
		return true
	}
	if prev.page != cur.page {
		// A paragraph may run across a page break, and the only evidence for
		// it is that the previous page stopped mid-sentence and this one
		// starts mid-sentence. Everything else is a new block.
		return pdfEndsSentence(prev.text) || !pdfStartsLower(cur.text)
	}
	gap := prev.y - cur.y
	if gap <= 0 {
		// Same page, no downward movement: a second column, or a box drawn
		// beside the last one. Not a continuation of it.
		return true
	}
	size := math.Max(prev.size, cur.size)
	if size <= 0 {
		return false
	}
	return gap/size > pdfParaBreak*lead
}

// pdfLeading is the document's usual line spacing, in multiples of the font
// size. Measuring it rather than assuming it is what lets one threshold work
// for a tightly set legal PDF and a double-spaced manuscript.
func pdfLeading(lines []*pdfLine) float64 {
	var ratios []float64
	for i := 1; i < len(lines); i++ {
		prev, cur := lines[i-1], lines[i]
		if prev.page != cur.page {
			continue
		}
		size := math.Max(prev.size, cur.size)
		gap := prev.y - cur.y
		if size <= 0 || gap <= 0 {
			continue
		}
		if r := gap / size; r < 4 {
			// Anything looser than four times the font size is the space
			// between blocks, which is what this measurement exists to detect;
			// letting it into the sample would raise the bar it sets.
			ratios = append(ratios, r)
		}
	}
	if len(ratios) == 0 {
		return 1.2
	}
	sort.Float64s(ratios)
	return ratios[len(ratios)/2]
}

// pdfBodySize is the font size most of the document's text is set in, weighted
// by how much text each size carries. It is the reference every heading is
// measured against, and it must be the most *voluminous* size rather than the
// most common line size: a contents page has more heading lines than body ones.
func pdfBodySize(lines []*pdfLine) float64 {
	weight := map[int]int{}
	for _, l := range lines {
		weight[pdfSizeKey(l.size)] += len([]rune(l.text))
	}
	best, bestKey := 0, 0
	for k, w := range weight {
		if w > best || (w == best && k < bestKey) {
			best, bestKey = w, k
		}
	}
	if bestKey == 0 {
		return 0
	}
	return float64(bestKey) / 10
}

// pdfHeadingLevels ranks the sizes above the body size, largest first, and maps
// them onto markdown levels. Absolute point sizes mean nothing across
// documents; the ordering of the sizes within one document means everything.
func pdfHeadingLevels(lines []*pdfLine, body float64) map[int]int {
	seen := map[int]bool{}
	for _, l := range lines {
		if body > 0 && l.size >= body*pdfHeadingRatio {
			seen[pdfSizeKey(l.size)] = true
		}
	}
	keys := make([]int, 0, len(seen))
	for k := range seen {
		keys = append(keys, k)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(keys)))

	levels := make(map[int]int, len(keys))
	for i, k := range keys {
		levels[k] = i + 1
	}
	return levels
}

// pdfSizeKey buckets a font size to a tenth of a point so that the same
// nominal size, arrived at through different matrices, hashes to one key.
func pdfSizeKey(size float64) int { return int(math.Round(size * 10)) }

// pdfJoin appends a wrapped line to the paragraph so far, undoing the
// hyphenation the layout introduced.
//
// A justified German PDF breaks words across lines constantly, and leaving the
// hyphen in turns one word into two and mis-counts the syllables of both. The
// exception is the suspended hyphen — "Post- und Kurierdienste" — where the
// hyphen is the author's and the next word is a conjunction. That construction
// is why the check is a word list rather than "is the next character
// lower-case": in German the continuation of a suspended hyphen is always
// lower-case too, so lower-case alone cannot tell the two apart.
func pdfJoin(text, next string) string {
	if text == "" {
		return next
	}
	if next == "" {
		return text
	}
	if pdfHyphenated(text, next) {
		return text[:len(text)-len(pdfTrailingHyphen(text))] + next
	}
	return text + " " + next
}

// pdfSuspended are the words that follow a suspended hyphen rather than
// completing the word before it. German first, because that is the language
// that builds compounds this way; the English pair costs nothing.
var pdfSuspended = map[string]bool{
	"und": true, "oder": true, "bzw": true, "beziehungsweise": true,
	"sowie": true, "als": true, "wie": true,
	"and": true, "or": true,
}

func pdfHyphenated(text, next string) bool {
	h := pdfTrailingHyphen(text)
	if h == "" {
		return false
	}
	stem := text[:len(text)-len(h)]
	if r, ok := pdfLastRune(stem); !ok || !unicode.IsLetter(r) {
		return false
	}
	first := []rune(next)[0]
	if !unicode.IsLetter(first) || !unicode.IsLower(first) {
		return false
	}
	word := next
	if i := strings.IndexAny(word, " \t"); i >= 0 {
		word = word[:i]
	}
	return !pdfSuspended[strings.Trim(strings.ToLower(word), ".,;:!?")]
}

// pdfTrailingHyphen returns the hyphen character ending s, if any. The
// typographic variants matter: a PDF produced from a word processor is as
// likely to carry U+2010 as an ASCII hyphen.
func pdfTrailingHyphen(s string) string {
	for _, h := range []string{"-", "‐", "‑", "­"} {
		if strings.HasSuffix(s, h) {
			return h
		}
	}
	return ""
}

func pdfLastRune(s string) (rune, bool) {
	rs := []rune(s)
	if len(rs) == 0 {
		return 0, false
	}
	return rs[len(rs)-1], true
}

func pdfEndsSentence(s string) bool {
	r, ok := pdfLastRune(strings.TrimRight(s, "\"')]»”’"))
	return ok && strings.ContainsRune(".!?:;", r)
}

func pdfStartsLower(s string) bool {
	rs := []rune(s)
	return len(rs) > 0 && unicode.IsLower(rs[0])
}

// pdfBullets are the glyphs a PDF uses to draw a list marker. The marker itself
// is dropped and replaced by markdown's, because the strip pass reads "- " and
// leaves "•" in the prose as a word.
const pdfBullets = "•‣▪▫◦●○·–—*"

// pdfMarker splits a leading list marker off a line, reporting whether there
// was one and whether it was ordered.
//
// A marker must be followed by a space: "•Text" is a glyph run that happens to
// start with a bullet, while "1.5 Millionen" is a number and not an item.
func pdfMarker(s string) (marker, ordered bool, rest string) {
	rs := []rune(s)
	if len(rs) < 2 {
		return false, false, s
	}
	if strings.ContainsRune(pdfBullets, rs[0]) && rs[1] == ' ' {
		return true, false, strings.TrimSpace(string(rs[1:]))
	}
	i := 0
	for i < len(rs) && unicode.IsDigit(rs[i]) {
		i++
	}
	if i > 0 && i+1 < len(rs) && (rs[i] == '.' || rs[i] == ')') && rs[i+1] == ' ' {
		return true, true, strings.TrimSpace(string(rs[i+2:]))
	}
	return false, false, s
}
