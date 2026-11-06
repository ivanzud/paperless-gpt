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
var ocrHTMLTagPattern = regexp.MustCompile(`(?i)</?(?:p|br|table|thead|tbody|tr|td|th|h[1-6]|ul|ol|li|b|strong|i|em|u|img|input|a|div|span|sup|sub)(?:\s[^<>]*)?/?>`)

var (
	ocrBlankLineRun    = regexp.MustCompile(`\n{3,}`)
	ocrMarkdownTableBr = regexp.MustCompile(`(?im)^(\s*\|.*)$`)
	ocrBrTag           = regexp.MustCompile(`(?i)<br\s*/?>`)
)

// convertOCRHTML rewrites HTML OCR output as the plain text and Markdown
// tables paperless-ngx stores, so document content, full-text search and the
// suggestion prompts never see raw markup. Tables become Markdown tables with
// merged cells padded so columns stay aligned. Model-written image
// descriptions are dropped because they describe the page rather than
// transcribe it; text printed inside images is kept.
func convertOCRHTML(content string) string {
	if !ocrHTMLTagPattern.MatchString(content) {
		return content
	}

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
	switch n.Type {
	case html.TextNode:
		text := n.Data
		if w.afterCheckbox {
			text = strings.TrimLeft(text, " \t")
			w.afterCheckbox = text == ""
		}
		w.sb.WriteString(text)
		return
	case html.ElementNode:
	default:
		w.children(n)
		return
	}

	switch n.DataAtom {
	case atom.Img, atom.Script, atom.Style:
		return
	case atom.Br:
		w.sb.WriteString("\n")
	case atom.Input:
		marker := checkboxMarker(n)
		w.sb.WriteString(marker)
		w.afterCheckbox = marker != ""
	case atom.Table:
		w.block()
		w.sb.WriteString(renderOCRTable(n))
		w.block()
	case atom.H1, atom.H2, atom.H3, atom.H4, atom.H5, atom.H6:
		w.block()
		level, _ := strconv.Atoi(n.Data[1:])
		w.sb.WriteString(strings.Repeat("#", level) + " ")
		w.sb.WriteString(strings.TrimSpace(strings.ReplaceAll(inlineText(n), "\n", " ")))
		w.block()
	case atom.Li:
		if !strings.HasSuffix(w.sb.String(), "\n") && w.sb.Len() > 0 {
			w.sb.WriteString("\n")
		}
		w.sb.WriteString("- ")
		w.sb.WriteString(strings.TrimSpace(inlineText(n)))
		w.sb.WriteString("\n")
	case atom.P, atom.Div, atom.Ul, atom.Ol, atom.Thead, atom.Tbody:
		if hasClass(n, "img-desc") {
			return
		}
		w.block()
		w.children(n)
		w.block()
	default:
		w.children(n)
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
		var row []*ocrTableCell
		col := 0
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
			skipCovered()
			cell := &ocrTableCell{header: c.DataAtom == atom.Th}
			ghost := attr(c, "data-kind") == "ghost"
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
					pending[col+i] = rowspan - 1
				}
			}
			col += 1 + pad
		}
		skipCovered()
		// Blank form lines carry no text; keep the header row so the table
		// still has one.
		if len(row) > 0 && (len(rows) == 0 || !emptyRow(row)) {
			rows = append(rows, row)
		}
	}
	if len(rows) == 0 {
		return ""
	}

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
