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

// Every input below is synthetic. Each case is text that the converter used
// to drop or glue together.
func TestConvertOCRHTMLKeepsTextFidelity(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "angle-bracketed text is not an element",
			input:    `<p>Contact John Doe <john@example.com> or Jane <jane.doe@example.com></p>`,
			expected: `Contact John Doe <john@example.com> or Jane <jane.doe@example.com>`,
		},
		{
			name:     "bare less-than does not swallow the paragraph",
			input:    `<p>Fee applies if balance<minimum and rate <5% APR</p><p>Next paragraph</p>`,
			expected: "Fee applies if balance<minimum and rate <5% APR\n\nNext paragraph",
		},
		{
			name:     "comparison operators",
			input:    `<p>a < b and c > d</p><p>if x<3 then</p>`,
			expected: "a < b and c > d\n\nif x<3 then",
		},
		{
			name:     "reference codes in angle brackets",
			input:    `<p>Ref <ABC123> and <see page 4></p>`,
			expected: `Ref <ABC123> and <see page 4>`,
		},
		{
			name:     "single letter markers without a closing tag are text",
			input:    `<p>Milk 1.99 <A></p><p>Bread 2.49 <B></p>`,
			expected: "Milk 1.99 <A>\n\nBread 2.49 <B>",
		},
		{
			name:     "real inline markup is still removed",
			input:    `<p>See <a href="https://example.com">site</a>, <b>bold</b> and <i>italic</i></p>`,
			expected: `See site, bold and italic`,
		},
		{
			name:     "paired unknown elements keep their text only",
			input:    `<p>Text <unknown>kept</unknown> and <custom-tag attr="1">also</custom-tag></p>`,
			expected: `Text kept and also`,
		},
		{
			name:     "sectioning elements are blocks",
			input:    `<section>One</section><header>Two</header><footer>Three</footer><address>1234 Oak Ave</address>`,
			expected: "One\n\nTwo\n\nThree\n\n1234 Oak Ave",
		},
		{
			name:     "blockquote, pre and figure captions are blocks",
			input:    "<blockquote>Quoted</blockquote><pre>Line 1\nLine 2</pre><figure><figcaption>Fig 1</figcaption></figure>after",
			expected: "Quoted\n\nLine 1\nLine 2\n\nFig 1\n\nafter",
		},
		{
			name:     "definition list terms and values are lines",
			input:    `<dl><dt>Policy<dd>1234<dt>Holder<dd>Jane Doe</dl>`,
			expected: "Policy\n1234\nHolder\nJane Doe",
		},
		{
			name:     "adjacent labels and spans are separated",
			input:    `<label>Name</label><span>Jane Doe</span><span>Acme</span>`,
			expected: `Name Jane Doe Acme`,
		},
		{
			name:     "span inside running text gets no extra space",
			input:    `<p>Total<span>:</span> 12<span>.50</span></p>`,
			expected: `Total: 12.50`,
		},
		{
			name:     "horizontal rule separates paragraphs",
			input:    `Line one<hr>Line two`,
			expected: "Line one\n\nLine two",
		},
		{
			name:     "table rows without a table element",
			input:    `<tr><td>Item</td><td>Price</td></tr><tr><td>Widget</td><td>1.00</td></tr>`,
			expected: "| Item | Price |\n| --- | --- |\n| Widget | 1.00 |",
		},
		{
			name:     "orphan rows between text",
			input:    `Intro<tr><td>Item</td><td>Price</td></tr><tr><td>Widget</td><td>1.00</td></tr>Outro`,
			expected: "Intro\n\n| Item | Price |\n| --- | --- |\n| Widget | 1.00 |\n\nOutro",
		},
		{
			name:     "table caption is kept above the table",
			input:    `<table><caption>Fees 2020</caption><tr><th>A</th><th>B</th></tr><tr><td>1</td><td>2</td></tr></table>`,
			expected: "Fees 2020\n\n| A | B |\n| --- | --- |\n| 1 | 2 |",
		},
		{
			name: "parent cell spanning rows and columns with ghost cells",
			input: `<table><tr><td data-kind="parent" data-cs="2" data-rs="2">HEAD</td><td data-kind="ghost"></td><td>X</td></tr>` +
				`<tr><td data-kind="ghost"></td><td data-kind="ghost"></td><td>Y</td></tr>` +
				`<tr><td>a</td><td>b</td><td>c</td></tr></table>`,
			expected: "| HEAD |  | X |\n| --- | --- | --- |\n|  |  | Y |\n| a | b | c |",
		},
		{
			name:     "plain rowspan and colspan lay out the same grid",
			input:    `<table><tr><td rowspan="2" colspan="2">HEAD</td><td>X</td></tr><tr><td>Y</td></tr><tr><td>a</td><td>b</td><td>c</td></tr></table>`,
			expected: "| HEAD |  | X |\n| --- | --- | --- |\n|  |  | Y |\n| a | b | c |",
		},
		{
			name:     "entities are decoded without any markup",
			input:    "Acme &amp; Co\nRate &lt;5% APR &#36;10\nhttps://example.com/?a=1&copy=2",
			expected: "Acme & Co\nRate <5% APR $10\nhttps://example.com/?a=1&copy=2",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := convertOCRHTML(tt.input)
			assert.Equal(t, tt.expected, got)
			// Stored text is sanitized again when it is checked for
			// meaningful content, so converting must be stable.
			assert.Equal(t, got, convertOCRHTML(got), "conversion must be idempotent")
		})
	}
}
