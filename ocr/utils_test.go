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

func TestSanitizeOCRTextStripsModelControlTokens(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "layout label and coordinate tokens separate blocks instead of gluing them",
			input:    "<|Section-Header|><|209|><|33|><|388|><|59|>ACME INVOICE<|Text|><|68|><|218|><|300|><|240|>Jane Doe<|Text|><|68|><|250|><|300|><|270|>1234 Maple Court",
			expected: "ACME INVOICE\n\nJane Doe\n\n1234 Maple Court",
		},
		{
			name:     "end-of-turn marker and its broken remnants are removed",
			input:    "Total due: 12.00\n<|im_end|>\n|im_end|>\n|im_end|>\n|im_end|>",
			expected: "Total due: 12.00",
		},
		{
			name:     "tokens inside HTML output do not survive the conversion",
			input:    "<p>Example Bank</p><|Text|><|10|><|20|><|30|><|40|><p>Statement for John Doe</p><|im_end|>",
			expected: "Example Bank\n\nStatement for John Doe",
		},
		{
			name:     "image and footer tokens",
			input:    "<|Image|><|1|><|2|><|3|><|4|>\nPage text\n<|Page-Footer|><|5|><|6|><|7|><|8|>Page 1 of 2",
			expected: "Page text\n\nPage 1 of 2",
		},
		{
			name:     "markdown tables keep their pipes",
			input:    "| Item | Qty |\n| --- | --- |\n| Widget | 2 |",
			expected: "| Item | Qty |\n| --- | --- |\n| Widget | 2 |",
		},
		{
			name:     "comparison operators and blank lines are left alone when no token is present",
			input:    "if a <b| or c |> d\n\n\n\nnext paragraph",
			expected: "if a <b| or c |> d\n\n\n\nnext paragraph",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, sanitizeOCRText(tc.input))
		})
	}
}

func TestIsMeaningfulOCRTextRejectsTokenOnlyOutput(t *testing.T) {
	assert.False(t, IsMeaningfulOCRText("<|Text|><|100|><|200|><|300|><|400|>\n<|im_end|>\n|im_end|>\n|im_end|>"))
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
			input:    "Account Number 00012345\nAmount Due $100.00",
			expected: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.expected, IsMeaningfulOCRText(tc.input))
		})
	}
}
