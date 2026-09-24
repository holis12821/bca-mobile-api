package pdf_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/holis12821/bca-mobile-api/internal/pkg/pdf"
)

func TestRender_ProducesAWellFormedDocument(t *testing.T) {
	doc := pdf.New("Bukti Transaksi")
	doc.Heading("BCA mobile", 18)
	doc.Rule()
	doc.Field("Nomor Referensi", "BCA20260922000123")
	doc.Field("Total", "IDR 1500000.00")
	doc.Text("Dokumen ini sah tanpa tanda tangan.")

	out := doc.Render()

	if !bytes.HasPrefix(out, []byte("%PDF-1.4")) {
		t.Fatalf("missing PDF header, got %q", out[:16])
	}
	if !bytes.HasSuffix(bytes.TrimSpace(out), []byte("%%EOF")) {
		t.Fatal("missing EOF trailer")
	}
	for _, want := range []string{"/Type /Catalog", "/Type /Pages", "/Type /Page", "xref", "startxref"} {
		if !bytes.Contains(out, []byte(want)) {
			t.Fatalf("missing %q in output", want)
		}
	}
	if !bytes.Contains(out, []byte("BCA20260922000123")) {
		t.Fatal("receipt content did not reach the page")
	}
}

// The xref offsets must point at the actual object positions, or readers
// reject the file.
func TestRender_XrefOffsetsPointAtObjects(t *testing.T) {
	doc := pdf.New("t")
	doc.Text("hello")
	out := doc.Render()

	idx := bytes.LastIndex(out, []byte("startxref"))
	if idx < 0 {
		t.Fatal("no startxref")
	}
	var xrefOffset int
	if _, err := fmtSscan(string(out[idx+len("startxref"):]), &xrefOffset); err != nil {
		t.Fatalf("unreadable startxref: %v", err)
	}
	if xrefOffset <= 0 || xrefOffset >= len(out) {
		t.Fatalf("startxref %d is outside the file (%d bytes)", xrefOffset, len(out))
	}
	if !bytes.HasPrefix(out[xrefOffset:], []byte("xref")) {
		t.Fatalf("startxref does not point at the xref table")
	}
}

// Parentheses and backslashes must be escaped or the content stream breaks.
func TestRender_EscapesStringDelimiters(t *testing.T) {
	doc := pdf.New("t")
	doc.Text(`Transfer (utama) \ cabang`)
	out := string(doc.Render())

	if !strings.Contains(out, `Transfer \(utama\) \\ cabang`) {
		t.Fatalf("delimiters were not escaped:\n%s", out)
	}
}

func fmtSscan(s string, v *int) (int, error) {
	s = strings.TrimSpace(s)
	n := 0
	read := 0
	for _, r := range s {
		if r < '0' || r > '9' {
			break
		}
		n = n*10 + int(r-'0')
		read++
	}
	*v = n
	return read, nil
}
