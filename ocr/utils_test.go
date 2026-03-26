package ocr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestSanitizeOCRText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "removes reasoning and markdown fences",
			input:    "<think>plan</think>\n```markdown\nInvoice #42\nTotal: 123.45\n```",
			expected: "Invoice #42\nTotal: 123.45",
		},
		{
			name:     "handles dangling close tag before fenced text",
			input:    "garbage </think>\n```text\nLine A\n```",
			expected: "Line A",
		},
		{
			name:     "trims surrounding whitespace",
			input:    "\n\nResult line\n\n",
			expected: "Result line",
		},
		{
			name:     "removes OCR failure boilerplate tail",
			input:    "Invoice line\n\nThe image provided is too blurry and illegible to accurately recognize any text. Therefore, no text can be extracted from this image.",
			expected: "Invoice line",
		},
		{
			name:     "removes chatty OCR prefix",
			input:    "So, here is the result of the text recognition:\n\n---\n\nAccount Number 12345",
			expected: "Account Number 12345",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, sanitizeOCRText(tc.input))
		})
	}
}

func TestIsMeaningfulOCRText(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "rejects OCR placeholder",
			input:    "This image does not contain any readable text.",
			expected: false,
		},
		{
			name:     "rejects short noise",
			input:    "123",
			expected: false,
		},
		{
			name:     "accepts real OCR text",
			input:    "Account Number 01310874\nAmount Due $1,204.00",
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, IsMeaningfulOCRText(tc.input))
		})
	}
}
