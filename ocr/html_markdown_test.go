package ocr

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestConvertOCRHTML(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "plain text is untouched",
			input:    "Invoice #42\nRate <5% APR\nTotal: 123.45",
			expected: "Invoice #42\nRate <5% APR\nTotal: 123.45",
		},
		{
			name:     "markdown table is untouched",
			input:    "| Month | Amount |\n| --- | --- |\n| Jan | 1 |",
			expected: "| Month | Amount |\n| --- | --- |\n| Jan | 1 |",
		},
		{
			name:     "paragraphs, line breaks and entities",
			input:    "<p><b>MAIL PAYMENTS TO:</b></p><p>PO BOX 12345<br/>ANYTOWN, ST 00000</p><p>JANE &amp; JOHN DOE</p>",
			expected: "MAIL PAYMENTS TO:\n\nPO BOX 12345\nANYTOWN, ST 00000\n\nJANE & JOHN DOE",
		},
		{
			name:     "image descriptions dropped, image text kept",
			input:    `<img alt="Logo: Acme Insurance logo."/><p class="img-text">Acme Insurance®</p><p class="img-desc">A black-and-white logo.</p><p>Agency Phone: (555) 010-0100</p>`,
			expected: "Acme Insurance®\n\nAgency Phone: (555) 010-0100",
		},
		{
			name:     "table with omitted end tags",
			input:    "<table><tr><th>SERVICE<th>AMOUNT<tr><td>AA<td>10.00<tr><td>BB<td>20.00</table>",
			expected: "| SERVICE | AMOUNT |\n| --- | --- |\n| AA | 10.00 |\n| BB | 20.00 |",
		},
		{
			name:     "surya parent and ghost cells keep columns aligned",
			input:    `<table><tr><th data-cs="2" data-kind="parent">TAXES<th data-kind="ghost"><th>TOTAL<tr><td>COUNTY<td>1.23<td>1,000.00</table>`,
			expected: "| TAXES |  | TOTAL |\n| --- | --- | --- |\n| COUNTY | 1.23 | 1,000.00 |",
		},
		{
			name:     "colspan and rowspan are padded",
			input:    `<table><tr><td rowspan="2">SOLD TO<br/>Sample<td>DATE<td>BY<tr><td>PHONE<td>ORDER<tr><td colspan="2">TERMS<td>Net 30</table>`,
			expected: "| SOLD TO Sample | DATE | BY |\n| --- | --- | --- |\n|  | PHONE | ORDER |\n| TERMS |  | Net 30 |",
		},
		{
			name:     "blank rows dropped and pipes escaped",
			input:    "<table><tr><th>A<th>B<tr><td><td><tr><td>x|y<td>z</table>",
			expected: "| A | B |\n| --- | --- |\n| x\\|y | z |",
		},
		{
			name:     "headings, lists and checkboxes",
			input:    `<h3>OTHER WAYS TO PAY:</h3><ul><li><input type="checkbox"/> <b>Online</b></li><li><input type="checkbox" checked/> Mail</li></ul><p>Thanks</p>`,
			expected: "### OTHER WAYS TO PAY:\n\n- [ ] Online\n- [x] Mail\n\nThanks",
		},
		{
			name:     "br inside a markdown table row stays on the row",
			input:    "| Name | Address |\n| --- | --- |\n| Bob | 1 Main St<br>Apt 2 |\n<p>Footer</p>",
			expected: "| Name | Address |\n| --- | --- |\n| Bob | 1 Main St Apt 2 |\n\nFooter",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.expected, convertOCRHTML(tt.input))
		})
	}
}

func TestSanitizeOCRTextConvertsHTML(t *testing.T) {
	input := "```html\n<p>Account No.<br/>12345</p>\n```"
	assert.Equal(t, "Account No.\n12345", sanitizeOCRText(input))
}
