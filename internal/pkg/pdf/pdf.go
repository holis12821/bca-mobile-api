// Package pdf writes a minimal, single-page PDF document.
//
// GET /transactions/{id}/receipt/pdf is in the API spec and had no
// implementation. Rather than add a dependency for what is a page of text, this
// emits the PDF by hand: a catalog, one page, one content stream, and the two
// base-14 Helvetica fonts every reader is required to provide. That keeps the
// module free of a new third-party surface for something this small.
package pdf

import (
	"bytes"
	"fmt"
	"strings"
)

// A4 at 72dpi, and the margins the receipt is laid out within.
const (
	pageWidth  = 595
	pageHeight = 842
	marginLeft = 56
	marginTop  = 64
)

// Font selects one of the two embedded base-14 faces.
type Font int

const (
	Regular Font = iota
	Bold
)

// Line is one rendered row of the document.
type Line struct {
	Text string
	Font Font
	Size float64
	// GapBefore is extra vertical space above this line, in points.
	GapBefore float64
	// Rule draws a horizontal divider instead of text.
	Rule bool
}

// Doc accumulates lines and renders them.
type Doc struct {
	Title string
	Lines []Line
}

// New starts a document.
func New(title string) *Doc { return &Doc{Title: title} }

// Heading appends a bold line.
func (d *Doc) Heading(text string, size float64) {
	d.Lines = append(d.Lines, Line{Text: text, Font: Bold, Size: size, GapBefore: 6})
}

// Text appends a regular line.
func (d *Doc) Text(text string) {
	d.Lines = append(d.Lines, Line{Text: text, Font: Regular, Size: 10})
}

// Field appends a "label: value" row with the value in bold.
func (d *Doc) Field(label, value string) {
	d.Lines = append(d.Lines, Line{Text: label, Font: Regular, Size: 9})
	d.Lines = append(d.Lines, Line{Text: value, Font: Bold, Size: 11, GapBefore: 1})
}

// Rule appends a divider.
func (d *Doc) Rule() {
	d.Lines = append(d.Lines, Line{Rule: true, GapBefore: 8})
}

// Render returns the complete PDF bytes.
func (d *Doc) Render() []byte {
	content := d.contentStream()

	var objects []string
	objects = append(objects,
		"<< /Type /Catalog /Pages 2 0 R >>",
		"<< /Type /Pages /Kids [3 0 R] /Count 1 >>",
		fmt.Sprintf("<< /Type /Page /Parent 2 0 R /MediaBox [0 0 %d %d] /Contents 4 0 R "+
			"/Resources << /Font << /F1 5 0 R /F2 6 0 R >> >> >>", pageWidth, pageHeight),
		fmt.Sprintf("<< /Length %d >>\nstream\n%s\nendstream", len(content), content),
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica /Encoding /WinAnsiEncoding >>",
		"<< /Type /Font /Subtype /Type1 /BaseFont /Helvetica-Bold /Encoding /WinAnsiEncoding >>",
	)

	var buf bytes.Buffer
	buf.WriteString("%PDF-1.4\n")

	offsets := make([]int, len(objects)+1)
	for i, body := range objects {
		offsets[i+1] = buf.Len()
		fmt.Fprintf(&buf, "%d 0 obj\n%s\nendobj\n", i+1, body)
	}

	xrefOffset := buf.Len()
	fmt.Fprintf(&buf, "xref\n0 %d\n", len(objects)+1)
	buf.WriteString("0000000000 65535 f \n")
	for i := 1; i <= len(objects); i++ {
		fmt.Fprintf(&buf, "%010d 00000 n \n", offsets[i])
	}
	fmt.Fprintf(&buf, "trailer\n<< /Size %d /Root 1 0 R >>\nstartxref\n%d\n%%%%EOF\n",
		len(objects)+1, xrefOffset)

	return buf.Bytes()
}

func (d *Doc) contentStream() string {
	var b strings.Builder
	y := float64(pageHeight - marginTop)

	for _, line := range d.Lines {
		y -= line.GapBefore

		if line.Rule {
			y -= 4
			fmt.Fprintf(&b, "0.8 0.8 0.8 RG 0.7 w %d %.2f m %d %.2f l S\n",
				marginLeft, y, pageWidth-marginLeft, y)
			y -= 8
			continue
		}

		size := line.Size
		if size == 0 {
			size = 10
		}
		y -= size + 3

		font := "/F1"
		if line.Font == Bold {
			font = "/F2"
		}
		fmt.Fprintf(&b, "BT %s %.2f Tf 0 0 0 rg %d %.2f Td (%s) Tj ET\n",
			font, size, marginLeft, y, escape(line.Text))
	}

	return b.String()
}

// escape makes a Go string safe inside a PDF literal string and drops anything
// outside WinAnsi's printable range, which the base-14 fonts cannot show.
func escape(s string) string {
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case r == '(' || r == ')' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 32 || r > 255:
			b.WriteByte(' ')
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}
