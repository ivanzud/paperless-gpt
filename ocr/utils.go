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
	ocrChattyPrefixPatterns = []*regexp.Regexp{
		regexp.MustCompile(`(?is)^\s*so,\s*here is the result of the text recognition:\s*(?:---\s*)?`),
		regexp.MustCompile(`(?is)^\s*here(?:'s| is) the (?:result of the )?text recognition:\s*(?:---\s*)?`),
		regexp.MustCompile(`(?is)^\s*here(?:'s| is) the extracted text:\s*(?:---\s*)?`),
	}
)

func sanitizeOCRText(content string) string {
	cleaned := textsanitize.StripReasoning(content)
	cleaned = stripMarkdownCodeFences(cleaned)
	cleaned = stripOCRFailureBoilerplate(cleaned)
	cleaned = stripOCRChattyPrefixes(cleaned)
	return strings.TrimSpace(cleaned)
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
