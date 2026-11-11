package ocr

import (
	"bytes"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"
)

// Reasons a page's OCR output is classified as degenerate.
const (
	DegenerateReasonTokenLimit = "token_limit"
	DegenerateReasonRepetition = "repetition_loop"
	DegenerateReasonNoise      = "symbol_noise"
	DegenerateReasonCommentary = "commentary"
)

// Thresholds of the degenerate-output heuristics. They are deliberately far
// away from anything a real page produces: a document with many identical
// table rows, a form with dozens of empty cells or a long numeric statement
// must never trip them, because the consequence is a failed document.
const (
	// A repetition loop is a unit of at most this many bytes ...
	degenerateMaxLoopUnit = 200
	// ... repeated back to back at least this many times ...
	degenerateMinLoopRepeats = 30
	// ... covering at least this many bytes ...
	degenerateMinLoopBytes = 600
	// ... and at least half of the page (see hasRepetitionLoop).
	//
	// When the unit is one or more whole lines, a form or table can
	// legitimately repeat it for most of a page: pre-printed log rows, a
	// column of identical entries. More identical lines than fit on a page
	// are needed before that is called a loop.
	degenerateMinLineLoopRepeats = 100

	// Symbol noise: runs of at least this many consecutive symbols that mix
	// two or more characters other than table/ruler characters and form
	// scaffolding (see isFormScaffolding) ...
	degenerateMinNoiseRun = 5
	// ... adding up to at least this many characters and this share of the page.
	degenerateMinNoiseChars   = 24
	degenerateNoiseSharePerc  = 5
	degenerateRulerCharacters = "|-:_.="
)

// DegenerateOutputError reports that a vision model answered, but with output
// that must not be stored as a successful transcription: it ran into the
// completion-token cap, got stuck in a repetition loop, produced symbol noise,
// or described the page instead of transcribing it.
//
// The message never contains document text, only the reason and counters, so
// it is safe to log and to store as a job error.
type DegenerateOutputError struct {
	Reason string
	Detail string
	// Result is the rejected result. It lets a caller that reviews pages
	// interactively, or that is configured to accept such output, still use it.
	Result *OCRResult
	// NoText says that every attempt answered with nothing but a complete
	// remark about legibility, without one transcribed letter or digit and
	// without running into the token limit: the model found no
	// text on the page, as on the blank back of a duplex scan. Such a page is
	// a page without text rather than a page with garbage, so a caller that
	// leaves pages without text out may leave it out as well.
	NoText bool
}

func (e *DegenerateOutputError) Error() string {
	if e.Detail == "" {
		return fmt.Sprintf("degenerate OCR output (%s)", e.Reason)
	}
	return fmt.Sprintf("degenerate OCR output (%s): %s", e.Reason, e.Detail)
}

// AsDegenerateOutput returns the DegenerateOutputError in err's chain, or nil.
func AsDegenerateOutput(err error) *DegenerateOutputError {
	var degenerate *DegenerateOutputError
	if errors.As(err, &degenerate) {
		return degenerate
	}
	return nil
}

var (
	// A whole line in square brackets that talks about legibility is the model
	// commenting on the page, not text printed on it. Short markers such as
	// "[illegible]" are intentionally not matched (see minCommentaryLineBytes).
	ocrLegibilityCommentaryPattern = regexp.MustCompile(`(?i)\b(?:illegible|unreadable|not legible|faint|faded|blurry|blurred|too (?:small|dark|light|blurry) to (?:read|transcribe))\b`)
	// A page that opens by describing the picture or by refusing is commentary
	// too. It is only counted, never removed: unlike the bracketed form it
	// could, rarely, be genuine document text. Only the first non-empty line
	// of a page is considered (see countDescriptiveCommentary): a model that
	// comments instead of transcribing starts with it, whereas a letter says
	// "I am sorry to inform you ..." or "Unfortunately, I ..." in its body,
	// after a letterhead, a date or a salutation.
	ocrDescriptiveCommentaryPattern = regexp.MustCompile(`(?i)^(?:(?:the|this) (?:image|picture|photo|scan|scanned image|scanned page) (?:shows|appears|contains|displays|depicts|is)\b|i(?:'m| am) (?:sorry|unable)\b|i (?:cannot|can't|apologi[sz]e)\b|sorry, (?:but )?i\b|unfortunately, i\b|as an ai\b)`)
)

const minCommentaryLineBytes = 40

// stripOCRCommentary removes bracketed legibility commentary and reports how
// many letters and digits belong to commentary (removed or merely counted).
func stripOCRCommentary(content string) (cleaned string, commentaryChars int) {
	if content == "" {
		return "", 0
	}
	lines := strings.Split(content, "\n")
	kept := make([]string, 0, len(lines))
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if isBracketedLegibilityCommentary(trimmed) {
			commentaryChars += countLettersAndDigits(trimmed)
			continue
		}
		kept = append(kept, line)
	}
	cleaned = content
	if len(kept) != len(lines) {
		cleaned = strings.TrimSpace(strings.Join(kept, "\n"))
	}
	return cleaned, commentaryChars + countDescriptiveCommentary(cleaned)
}

func isBracketedLegibilityCommentary(line string) bool {
	if len(line) < minCommentaryLineBytes || !strings.HasPrefix(line, "[") || !strings.HasSuffix(line, "]") {
		return false
	}
	inner := line[1 : len(line)-1]
	if strings.ContainsAny(inner, "[]") {
		return false
	}
	return ocrLegibilityCommentaryPattern.MatchString(inner)
}

func countLettersAndDigits(s string) int {
	n := 0
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			n++
		}
	}
	return n
}

// degenerateOCRReason classifies sanitized page text. commentaryChars is the
// count reported by stripOCRCommentary and limitHit says whether generation
// stopped at the completion-token cap. It returns an empty reason for output
// that looks like a transcription.
func degenerateOCRReason(text string, commentaryChars int, limitHit bool) (reason, detail string) {
	if limitHit && text != "" {
		return DegenerateReasonTokenLimit, "generation stopped at the completion-token cap"
	}
	if commentaryChars > 0 {
		transcribed := countLettersAndDigits(text)
		// Descriptive lines stay in the text, so they are part of transcribed
		// as well; the comparison is against what is left without them.
		if remaining := transcribed - countDescriptiveCommentary(text); remaining < commentaryChars {
			return DegenerateReasonCommentary, fmt.Sprintf("%d commentary characters against %d transcribed", commentaryChars, max(remaining, 0))
		}
	}
	if unitLen, repeats, ok := hasRepetitionLoop(text); ok {
		return DegenerateReasonRepetition, fmt.Sprintf("a %d-byte unit repeats %d times", unitLen, repeats)
	}
	if noise, ok := hasSymbolNoise(text); ok {
		return DegenerateReasonNoise, fmt.Sprintf("%d of %d characters are symbol noise", noise, len([]rune(text)))
	}
	return "", ""
}

// countDescriptiveCommentary returns the number of letters and digits in the
// first non-empty line of text when that line describes the picture or
// refuses the task, and 0 otherwise. Later lines never count: the same words
// in the body of a letter are document text.
func countDescriptiveCommentary(text string) int {
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if ocrDescriptiveCommentaryPattern.MatchString(trimmed) {
			return countLettersAndDigits(trimmed)
		}
		return 0
	}
	return 0
}

// hasRepetitionLoop reports a runaway generation loop: one short unit that
// contains at least one letter or digit, repeated back to back so often that
// the repetition makes up at least half of the page.
//
// What it must not flag: empty table rows and ruler lines (no letter or digit
// in the unit), a table whose rows repeat a few dozen times next to other
// content (less than half of the page), a page of identical filled rows
// (fewer than degenerateMinLineLoopRepeats whole-line repeats), and several
// copies of the same form on one page (the repeating unit is far longer than
// degenerateMaxLoopUnit).
func hasRepetitionLoop(text string) (unitLen, repeats int, found bool) {
	b := []byte(text)
	n := len(b)
	if n < degenerateMinLoopBytes {
		return 0, 0, false
	}
	for period := 1; period <= degenerateMaxLoopUnit && period*degenerateMinLoopRepeats <= n; period++ {
		need := period * degenerateMinLoopRepeats
		if need < degenerateMinLoopBytes {
			need = degenerateMinLoopBytes
		}
		if need*2 < n {
			need = (n + 1) / 2
		}
		run := 0
		for i := period; i < n; i++ {
			if b[i] != b[i-period] {
				run = 0
				continue
			}
			run++
			if run+period < need {
				continue
			}
			// Extend to the end of the run: the verdict depends on the real
			// count, and the unit stays the same for the rest of the run.
			for i+1 < n && b[i+1] == b[i+1-period] {
				i++
				run++
			}
			unit := b[i-period+1 : i+1]
			repeats := (run + period) / period
			// A unit without letters or digits (blank cells, rulers, spaces)
			// is never a loop; a unit of whole lines needs more repeats.
			if unitHasLetterOrDigit(unit) && (repeats >= degenerateMinLineLoopRepeats || bytes.IndexByte(unit, '\n') < 0) {
				return period, repeats, true
			}
			run = 0
		}
	}
	return 0, 0, false
}

func unitHasLetterOrDigit(unit []byte) bool {
	for _, r := range string(unit) {
		if r != unicode.ReplacementChar && (unicode.IsLetter(r) || unicode.IsDigit(r)) {
			return true
		}
	}
	return false
}

// isFormScaffolding reports characters that blank forms are drawn with next
// to ruler characters: the brackets around a fill-in field such as `(___)`
// or `[____]`, and pictographic symbols such as ballot boxes, check marks and
// bullets. A row of them is a form, not noise.
func isFormScaffolding(r rune) bool {
	return strings.ContainsRune("()[]", r) || unicode.Is(unicode.So, r)
}

// hasSymbolNoise reports output that is to a relevant degree made of long
// mixed-symbol runs such as `*+**+*+++*`. Runs made of one repeated character
// (masked digits, dot leaders), runs of table or ruler characters and runs of
// form scaffolding do not count, whatever their length.
func hasSymbolNoise(text string) (noiseChars int, found bool) {
	total := 0
	var run []rune
	flush := func() {
		if len(run) >= degenerateMinNoiseRun {
			distinct := map[rune]struct{}{}
			for _, r := range run {
				if !strings.ContainsRune(degenerateRulerCharacters, r) && !isFormScaffolding(r) {
					distinct[r] = struct{}{}
				}
			}
			if len(distinct) >= 2 {
				noiseChars += len(run)
			}
		}
		run = run[:0]
	}
	for _, r := range text {
		total++
		if unicode.IsLetter(r) || unicode.IsDigit(r) || unicode.IsMark(r) || unicode.IsSpace(r) {
			flush()
			continue
		}
		run = append(run, r)
	}
	flush()
	if noiseChars < degenerateMinNoiseChars || noiseChars*100 < degenerateNoiseSharePerc*total {
		return noiseChars, false
	}
	return noiseChars, true
}
