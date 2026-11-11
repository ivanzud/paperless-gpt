package ocr

import (
	"paperless-gpt/internal/textsanitize"
	"regexp"
	"strings"
	"unicode"
)

var (
	ocrFailureBoilerplatePatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)your transcription is empty\.\s*no text was detected in the image\.?`),
		regexp.MustCompile(`(?is)this image does not contain any readable text\.?`),
		regexp.MustCompile(`(?is)the image provided is too blurry and illegible to accurately recognize any text\.\s*therefore,\s*no text can be extracted from this image\.?`),
		regexp.MustCompile(`(?is)\b\d+\.\s*the image contains a series of lines with no visible text or images\.\s*it appears to be a blank or poorly parsed document\.?`),
	}
	// modelControlTokenPattern matches special tokens a vision model leaks into
	// its answer: complete `<|name|>` tokens (layout labels, bounding-box
	// coordinates, end-of-turn markers) and the truncated remnants of
	// chat-template markers such as `|im_end|>`. None of them is document text.
	//
	// A token name never contains whitespace, and a remnant keeps at least one
	// of its angle brackets. Anything looser deletes document text: the words
	// between two operators in `a <| b |> c`, or a table cell `|im_end|`.
	modelControlTokenPattern = regexp.MustCompile(`<\|[^|<>\s]{1,40}\|>|<\|(?:im_start|im_end|endoftext|end_of_text|eot_id)\||\|(?:im_start|im_end|endoftext|end_of_text|eot_id)\|>`)
	excessBlankLinesPattern  = regexp.MustCompile(`\n[ \t]*(?:\n[ \t]*){2,}`)
	ocrChattyPrefixPatterns  = []*regexp.Regexp{
		regexp.MustCompile(`(?is)^\s*so,\s*here is the result of the text recognition:\s*(?:---\s*)?`),
		regexp.MustCompile(`(?is)^\s*here(?:'s| is) the (?:result of the )?text recognition:\s*(?:---\s*)?`),
		regexp.MustCompile(`(?is)^\s*here(?:'s| is) the extracted text:\s*(?:---\s*)?`),
	}
)

func sanitizeOCRText(content string) string {
	cleaned, _ := sanitizeOCRTextWithCommentary(content)
	return cleaned
}

// sanitizeOCRTextWithCommentary is sanitizeOCRText plus the number of letters
// and digits that were model commentary rather than transcription, which the
// degenerate-output check needs (see degenerateOCRReason).
func sanitizeOCRTextWithCommentary(content string) (string, int) {
	cleaned := textsanitize.StripReasoning(content)
	cleaned = stripMarkdownCodeFences(cleaned)
	cleaned = stripModelControlTokens(cleaned)
	cleaned = convertOCRHTML(cleaned)
	cleaned = stripOCRFailureBoilerplate(cleaned)
	cleaned = stripOCRChattyPrefixes(cleaned)
	cleaned, commentaryChars := stripOCRCommentary(cleaned)
	return strings.TrimSpace(cleaned), commentaryChars
}

// IsMeaningfulOCRText rejects empty or boilerplate-only OCR responses before they poison the queue.
func IsMeaningfulOCRText(content string) bool {
	cleaned := sanitizeOCRText(content)
	if cleaned == "" {
		return false
	}

	alphaNumericCount := 0
	for _, r := range cleaned {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			alphaNumericCount++
		}
	}

	return alphaNumericCount >= 12
}

func stripMarkdownCodeFences(content string) string {
	if content == "" {
		return ""
	}

	lines := strings.Split(content, "\n")
	filtered := make([]string, 0, len(lines))
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			continue
		}
		filtered = append(filtered, line)
	}

	return strings.Join(filtered, "\n")
}

// stripModelControlTokens removes leaked special tokens. Each token becomes a
// line break rather than nothing: layout models put one token group in front
// of every text block, so deleting them outright would glue the blocks
// together. The blank lines that leaves behind are collapsed again.
func stripModelControlTokens(content string) string {
	if !modelControlTokenPattern.MatchString(content) {
		return content
	}
	cleaned := modelControlTokenPattern.ReplaceAllString(content, "\n")
	return excessBlankLinesPattern.ReplaceAllString(cleaned, "\n\n")
}

func stripOCRFailureBoilerplate(content string) string {
	cleaned := content
	for _, pattern := range ocrFailureBoilerplatePatterns {
		cleaned = pattern.ReplaceAllString(cleaned, "")
	}

	return strings.TrimSpace(strings.ReplaceAll(cleaned, "\u0000", ""))
}

func stripOCRChattyPrefixes(content string) string {
	cleaned := content
	for _, pattern := range ocrChattyPrefixPatterns {
		cleaned = pattern.ReplaceAllString(cleaned, "")
	}

	return strings.TrimSpace(cleaned)
}
