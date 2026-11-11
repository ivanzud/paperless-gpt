package ocr

import (
	"regexp"
	"strconv"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// ocrHTMLTagPattern matches the layout tags that HTML-emitting OCR models
// (Surya, LightOnOCR) wrap their transcription in. Plain-text or Markdown
// output without these tags is passed through untouched, so a stray "<" in a
// recognized document ("<5% APR") is never mistaken for markup.
var ocrHTMLTagPattern = regexp.MustCompile(`(?i)</?(?:p|br|hr|table|thead|tbody|tfoot|tr|td|th|caption|h[1-6]|ul|ol|li|dl|dt|dd|b|strong|i|em|u|img|input|a|div|span|label|sup|sub|section|article|header|footer|address|blockquote|pre|figure|figcaption)(?:\s[^<>]*)?/?>`)

var (
	ocrBlankLineRun    = regexp.MustCompile(`\n{3,}`)
	ocrMarkdownTableBr = regexp.MustCompile(`(?im)^(\s*\|.*)$`)
	ocrBrTag           = regexp.MustCompile(`(?i)<br\s*/?>`)

	// ocrTagCandidate matches what the HTML tokenizer would read as a tag at
	// the start of the input; the first group is the element name.
	ocrTagCandidate = regexp.MustCompile(`^</?([A-Za-z][A-Za-z0-9-]*)(?:\s[^<>]*)?/?>`)
	// ocrSemicolonEntity matches a character reference that is terminated
	// properly, the only kind that is safe to decode in plain text
	// ("?a=1&copy=2" in a printed URL is not an entity).
	ocrSemicolonEntity = regexp.MustCompile(`&(?:[A-Za-z][A-Za-z0-9]{1,31}|#[0-9]{1,7}|#[xX][0-9A-Fa-f]{1,6});`)
	ocrTableTag        = regexp.MustCompile(`(?i)<table[\s>]`)
	ocrOrphanRowOpen   = regexp.MustCompile(`(?i)<(?:thead|tbody|tfoot|tr|td|th)(?:\s[^<>]*)?>`)
	ocrOrphanRowClose  = regexp.MustCompile(`(?i)</(?:thead|tbody|tfoot|tr|td|th)\s*>`)
)

// ocrKnownHTMLElements are the element names accepted as markup without
// further evidence. Anything else in angle brackets is only a tag when the
// output also contains its counterpart (see escapeNonTagAngleBrackets).
var ocrKnownHTMLElements = map[string]bool{}

func init() {
	for _, name := range strings.Fields(`
		html head body p br hr div span table thead tbody tfoot tr td th caption colgroup col
		h1 h2 h3 h4 h5 h6 ul ol li dl dt dd b strong i em u s strike del ins mark small big
		sub sup code pre kbd samp var tt q cite abbr blockquote a img input label form
		fieldset legend select option button section article header footer main nav aside
		address figure figcaption picture center font math script style details summary
		time wbr nobr`) {
		ocrKnownHTMLElements[name] = true
	}
}

// ocrInlineMarkerElements are one-letter inline elements whose closing tag
// HTML never lets an author omit. A lone "<A>" or "<B>" without attributes
// and without any closing tag is therefore printed text (receipts mark tax
// classes that way), not markup.
var ocrInlineMarkerElements = map[string]bool{"a": true, "b": true, "i": true, "u": true, "s": true, "q": true}

// escapeNonTagAngleBrackets turns every "<" that does not start real markup
// into "&lt;", so the HTML parser keeps it as text. Without this, printed
// text such as "<jane@example.com>" is parsed as an unknown element and
// dropped, and "balance<minimum ..." swallows the rest of its paragraph as
// the attributes of a tag that never closes.
func escapeNonTagAngleBrackets(content string) string {
	if !strings.Contains(content, "<") {
		return content
	}
	lower := strings.ToLower(content)
	var sb strings.Builder
	sb.Grow(len(content) + 16)
	for i := 0; i < len(content); i++ {
		if content[i] != '<' || strings.HasPrefix(content[i:], "<!--") || isOCRMarkupAt(content, lower, i) {
			sb.WriteByte(content[i])
			continue
		}
		sb.WriteString("&lt;")
	}
	return sb.String()
}

func isOCRMarkupAt(content, lower string, i int) bool {
	m := ocrTagCandidate.FindStringSubmatch(content[i:])
	if m == nil {
		return false
	}
	name := strings.ToLower(m[1])
	closing := strings.HasPrefix(m[0], "</")
	if ocrKnownHTMLElements[name] {
		if !closing && ocrInlineMarkerElements[name] && m[0] == content[i:i+len(name)+2] {
			return strings.Contains(lower, "</"+name+">")
		}
		return true
	}
	// An unknown element is still markup when the model both opens and
	// closes it; its tags are dropped and its text kept, as before.
	if closing {
		return strings.Contains(lower, "<"+name+">") || strings.Contains(lower, "<"+name+" ")
	}
	return strings.Contains(lower, "</"+name+">") || strings.HasSuffix(m[0], "/>")
}

// wrapOrphanTableRows puts table rows that arrive without a <table> around
// them into one. The HTML parser discards row and cell tags outside a table,
// which would glue all cell texts together.
func wrapOrphanTableRows(content string) string {
	if ocrTableTag.MatchString(content) {
		return content
	}
	open := ocrOrphanRowOpen.FindStringIndex(content)
	if open == nil {
		return content
	}
	end := len(content)
	if closes := ocrOrphanRowClose.FindAllStringIndex(content, -1); len(closes) > 0 && closes[len(closes)-1][1] > open[0] {
		end = closes[len(closes)-1][1]
	}
	return content[:open[0]] + "<table>" + content[open[0]:end] + "</table>" + content[end:]
}

// convertOCRHTML rewrites HTML OCR output as the plain text and Markdown
// tables paperless-ngx stores, so document content, full-text search and the
// suggestion prompts never see raw markup. Tables become Markdown tables with
// merged cells padded so columns stay aligned. Model-written image
// descriptions are dropped because they describe the page rather than
// transcribe it; text printed inside images is kept.
func convertOCRHTML(content string) string {
	if !ocrHTMLTagPattern.MatchString(content) {
		// No markup, but a model that writes HTML may still have escaped the
		// text ("AT&amp;T").
		return ocrSemicolonEntity.ReplaceAllStringFunc(content, html.UnescapeString)
	}
	content = escapeNonTagAngleBrackets(content)
	content = wrapOrphanTableRows(content)

	// A <br> inside a Markdown table row would split the row once parsed, so
	// those become spaces before the HTML parser sees them.
	content = ocrMarkdownTableBr.ReplaceAllStringFunc(content, func(line string) string {
		return ocrBrTag.ReplaceAllString(line, " ")
	})

	body := &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body}
	nodes, err := html.ParseFragment(strings.NewReader(content), body)
	if err != nil {
		return content
	}

	w := &ocrMarkdownWriter{}
	for _, n := range nodes {
		w.node(n)
	}

	lines := strings.Split(w.sb.String(), "\n")
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, " \t")
	}
	out := ocrBlankLineRun.ReplaceAllString(strings.Join(lines, "\n"), "\n\n")
	return strings.TrimSpace(out)
}

type ocrMarkdownWriter struct {
	sb strings.Builder
	// afterCheckbox swallows the space a model types after a checkbox, since
	// the marker already ends in one.
	afterCheckbox bool
	// afterFragment is set right after a <span> or <label> closes, so the
	// next one that follows without any text in between gets a separator.
	afterFragment bool
}

// block starts a new paragraph unless the output already ends with one.
func (w *ocrMarkdownWriter) block() {
	s := w.sb.String()
	if s == "" || strings.HasSuffix(s, "\n\n") {
		return
	}
	if strings.HasSuffix(s, "\n") {
		w.sb.WriteString("\n")
		return
	}
	w.sb.WriteString("\n\n")
}

func (w *ocrMarkdownWriter) node(n *html.Node) {
	afterFragment := w.afterFragment
	w.afterFragment = false
	switch n.Type {
	case html.TextNode:
		w.text(n)
	case html.ElementNode:
		w.element(n, afterFragment)
	default:
		w.children(n)
	}
}

func (w *ocrMarkdownWriter) text(n *html.Node) {
	text := n.Data
	if w.afterCheckbox {
		text = strings.TrimLeft(text, " \t")
		w.afterCheckbox = text == ""
	}
	w.sb.WriteString(text)
}

// element writes one element; afterFragment tells whether the node right
// before it was an inline fragment (span, label).
func (w *ocrMarkdownWriter) element(n *html.Node, afterFragment bool) {
	switch n.DataAtom {
	case atom.Img, atom.Script, atom.Style:
		return
	case atom.Br:
		w.sb.WriteString("\n")
	case atom.Input:
		marker := checkboxMarker(n)
		w.sb.WriteString(marker)
		w.afterCheckbox = marker != ""
	case atom.Hr:
		w.block()
	case atom.Table:
		w.table(n)
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		w.block()
		level, _ := strconv.Atoi(n.Data[1:])
		w.sb.WriteString(strings.Repeat("#", level) + " ")
		w.sb.WriteString(strings.TrimSpace(strings.ReplaceAll(inlineText(n), "\n", " ")))
		w.block()
	case atom.Li:
		w.line()
		w.sb.WriteString("- ")
		w.sb.WriteString(strings.TrimSpace(inlineText(n)))
		w.sb.WriteString("\n")
	case atom.P, atom.Div, atom.Ul, atom.Ol, atom.Thead, atom.Tbody,
		atom.Section, atom.Article, atom.Header, atom.Footer, atom.Main, atom.Nav, atom.Aside,
		atom.Address, atom.Blockquote, atom.Pre, atom.Dl, atom.Figure, atom.Figcaption,
		atom.Form, atom.Fieldset, atom.Details, atom.Summary, atom.Center, atom.Caption:
		if hasClass(n, "img-desc") {
			return
		}
		w.block()
		w.children(n)
		w.block()
	case atom.Dt, atom.Dd, atom.Legend:
		// Terms and their values are separate lines, not separate paragraphs.
		w.line()
		w.children(n)
		w.line()
	case atom.Span, atom.Label:
		w.fragment(n, afterFragment)
	default:
		w.children(n)
	}
}

// table writes the caption above the table: it is text of the page, and the
// grid itself has no place for it.
func (w *ocrMarkdownWriter) table(n *html.Node) {
	w.block()
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || c.DataAtom != atom.Caption {
			continue
		}
		if caption := strings.TrimSpace(inlineText(c)); caption != "" {
			w.sb.WriteString(caption)
			w.block()
		}
	}
	w.sb.WriteString(renderOCRTable(n))
	w.block()
}

// fragment writes a span or label. Two fragments marked up one right after
// the other are two pieces of text; without a separator they would read as
// one word.
func (w *ocrMarkdownWriter) fragment(n *html.Node, afterFragment bool) {
	if afterFragment {
		if s := w.sb.String(); s != "" && !strings.HasSuffix(s, " ") && !strings.HasSuffix(s, "\n") {
			w.sb.WriteString(" ")
		}
	}
	w.children(n)
	w.afterFragment = true
}

// line moves to the start of a new line unless the output is already there.
func (w *ocrMarkdownWriter) line() {
	if s := w.sb.String(); s != "" && !strings.HasSuffix(s, "\n") {
		w.sb.WriteString("\n")
	}
}

func (w *ocrMarkdownWriter) children(n *html.Node) {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		w.node(c)
	}
}

// inlineText renders a subtree with the same rules as the document body,
// for content that must stay on one block (headings, list items, cells).
func inlineText(n *html.Node) string {
	w := &ocrMarkdownWriter{}
	w.children(n)
	return ocrBlankLineRun.ReplaceAllString(w.sb.String(), "\n\n")
}

func checkboxMarker(n *html.Node) string {
	if !strings.EqualFold(attr(n, "type"), "checkbox") {
		return ""
	}
	for _, a := range n.Attr {
		if a.Key == "checked" {
			return "[x] "
		}
	}
	return "[ ] "
}

type ocrTableCell struct {
	text   string
	header bool
}

// renderOCRTable lays the table out on a grid, honoring rowspan and colspan
// (and Surya's data-rs/data-cs with explicit "ghost" placeholder cells) so
// every Markdown row has the same column count.
func renderOCRTable(table *html.Node) string {
	var rows [][]*ocrTableCell
	pending := map[int]int{} // column -> rows still covered by a rowspan above

	for _, tr := range descendants(table, atom.Tr) {
		row := layoutOCRTableRow(tr, pending)
		// Blank form lines carry no text; keep the header row so the table
		// still has one.
		if len(row) > 0 && (len(rows) == 0 || !emptyRow(row)) {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return ""
	}
	return formatOCRTable(rows)
}

// layoutOCRTableRow places the cells of one row on the grid. pending carries
// the rowspans still open from the rows above and is updated for the rows
// below.
func layoutOCRTableRow(tr *html.Node, pending map[int]int) []*ocrTableCell {
	var row []*ocrTableCell
	col := 0
	// Rowspans opened in this row only cover the rows below it. Keeping
	// them apart until the row is done stops a parent cell's own ghost
	// cells from being mistaken for covered columns.
	opened := map[int]int{}
	skipCovered := func() {
		for pending[col] > 0 {
			pending[col]--
			row = append(row, &ocrTableCell{})
			col++
		}
	}
	for c := tr.FirstChild; c != nil; c = c.NextSibling {
		if c.Type != html.ElementNode || (c.DataAtom != atom.Td && c.DataAtom != atom.Th) {
			continue
		}
		ghost := attr(c, "data-kind") == "ghost"
		if ghost && pending[col] > 0 {
			// The model spelled out a slot that a rowspan above already
			// covers: the ghost is that slot, not an extra column.
			pending[col]--
			row = append(row, &ocrTableCell{header: c.DataAtom == atom.Th})
			col++
			continue
		}
		skipCovered()
		cell := &ocrTableCell{header: c.DataAtom == atom.Th}
		if !ghost {
			cell.text = cellText(c)
		}
		row = append(row, cell)

		colspan := spanAttr(c, "colspan", "data-cs")
		rowspan := spanAttr(c, "rowspan", "data-rs")
		// Surya follows a "parent" cell with ghost cells for the columns it
		// spans; only pad spans the model did not already spell out.
		pad := colspan - 1
		if attr(c, "data-kind") == "parent" {
			pad = 0
		}
		for i := 0; i < pad; i++ {
			row = append(row, &ocrTableCell{})
		}
		if rowspan > 1 {
			for i := 0; i < colspan; i++ {
				opened[col+i] = rowspan - 1
			}
		}
		col += 1 + pad
	}
	skipCovered()
	for c, n := range opened {
		pending[c] = n
	}
	return row
}

// formatOCRTable writes the grid as a Markdown table, padding short rows to
// the width of the widest one.
func formatOCRTable(rows [][]*ocrTableCell) string {
	width := 0
	for _, row := range rows {
		if len(row) > width {
			width = len(row)
		}
	}

	var sb strings.Builder
	writeRow := func(row []*ocrTableCell) {
		sb.WriteString("|")
		for i := 0; i < width; i++ {
			text := ""
			if i < len(row) {
				text = row[i].text
			}
			sb.WriteString(" " + text + " |")
		}
		sb.WriteString("\n")
	}
	writeRow(rows[0])
	sb.WriteString("|" + strings.Repeat(" --- |", width) + "\n")
	for _, row := range rows[1:] {
		writeRow(row)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func emptyRow(row []*ocrTableCell) bool {
	for _, c := range row {
		if c.text != "" {
			return false
		}
	}
	return true
}

func cellText(n *html.Node) string {
	text := inlineText(n)
	text = strings.Join(strings.Fields(text), " ")
	return strings.ReplaceAll(text, "|", `\|`)
}

func descendants(n *html.Node, a atom.Atom) []*html.Node {
	var out []*html.Node
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Type == html.ElementNode && c.DataAtom == a {
				out = append(out, c)
				continue // nested tables are flattened into their cell text
			}
			if c.Type == html.ElementNode && c.DataAtom == atom.Table {
				continue
			}
			walk(c)
		}
	}
	walk(n)
	return out
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func hasClass(n *html.Node, class string) bool {
	for _, c := range strings.Fields(attr(n, "class")) {
		if c == class {
			return true
		}
	}
	return false
}

// spanAttr reads the first positive span attribute, defaulting to 1.
func spanAttr(n *html.Node, keys ...string) int {
	for _, k := range keys {
		if v, err := strconv.Atoi(strings.TrimSpace(attr(n, k))); err == nil && v > 1 {
			if v > 64 {
				return 64
			}
			return v
		}
	}
	return 1
}
