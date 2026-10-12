package convert

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/go-pdf/fpdf"
)

type docxImage struct {
	typ  string
	data []byte
}

// DocxToPDF renders a .docx into a PDF: its text, then each embedded image on its own page.
func DocxToPDF(src, dst string) error {
	text, images, err := docxContent(src)
	if err != nil {
		return err
	}
	pdf := fpdf.New("P", "mm", "A4", "")
	pdf.SetMargins(18, 18, 18)
	pdf.SetAutoPageBreak(true, 18)
	pdf.SetFont("Helvetica", "", 11)
	tr := pdf.UnicodeTranslatorFromDescriptor("")
	if strings.TrimSpace(text) != "" {
		pdf.AddPage()
		for _, line := range strings.Split(text, "\n") {
			if strings.TrimSpace(line) == "" {
				pdf.Ln(5)
				continue
			}
			pdf.MultiCell(0, 5, tr(line), "", "", false)
		}
	}
	for i, im := range images {
		name := fmt.Sprintf("img%d", i)
		opt := fpdf.ImageOptions{ImageType: im.typ}
		pdf.RegisterImageOptionsReader(name, opt, bytes.NewReader(im.data))
		pdf.AddPage()
		pdf.ImageOptions(name, 18, 18, 174, 0, false, opt, 0, "")
	}
	if pdf.PageCount() == 0 {
		pdf.AddPage()
		pdf.MultiCell(0, 5, "(no extractable content)", "", "", false)
	}
	return pdf.OutputFileAndClose(dst)
}

// PDFToDocx extracts PDF text (via pdftotext) and wraps it into a minimal .docx (text only).
func PDFToDocx(src, dst string) error {
	out, err := exec.Command("pdftotext", "-layout", src, "-").Output()
	if err != nil {
		return fmt.Errorf("pdftotext: %w", err)
	}
	return writeDocx(dst, string(out))
}

func docxContent(path string) (string, []docxImage, error) {
	zr, err := zip.OpenReader(path)
	if err != nil {
		return "", nil, fmt.Errorf("open docx: %w", err)
	}
	defer zr.Close()
	var b strings.Builder
	var images []docxImage
	for _, f := range zr.File {
		switch {
		case f.Name == "word/document.xml" || strings.HasPrefix(f.Name, "word/header") || strings.HasPrefix(f.Name, "word/footer"):
			rc, err := f.Open()
			if err != nil {
				return "", nil, err
			}
			err = decodeDocxText(rc, &b)
			_ = rc.Close()
			if err != nil {
				return "", nil, err
			}
		case strings.HasPrefix(f.Name, "word/media/"):
			typ := ""
			switch strings.ToLower(filepath.Ext(f.Name)) {
			case ".png":
				typ = "PNG"
			case ".jpg", ".jpeg":
				typ = "JPG"
			}
			if typ == "" {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				return "", nil, err
			}
			data, err := io.ReadAll(io.LimitReader(rc, 50<<20))
			_ = rc.Close()
			if err != nil {
				return "", nil, err
			}
			images = append(images, docxImage{typ: typ, data: data})
		}
	}
	return strings.TrimSpace(b.String()), images, nil
}

func decodeDocxText(r io.Reader, b *strings.Builder) error {
	dec := xml.NewDecoder(r)
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "t":
				var s string
				if err := dec.DecodeElement(&s, &t); err == nil {
					b.WriteString(s)
				}
			case "tab":
				b.WriteByte('\t')
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "p", "br", "cr":
				b.WriteByte('\n')
			}
		}
	}
}

// PLACEHOLDER_WRITEDOCX
const contentTypesXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Types xmlns="http://schemas.openxmlformats.org/package/2006/content-types"><Default Extension="rels" ContentType="application/vnd.openxmlformats-package.relationships+xml"/><Default Extension="xml" ContentType="application/xml"/><Override PartName="/word/document.xml" ContentType="application/vnd.openxmlformats-officedocument.wordprocessingml.document.main+xml"/></Types>`

const relsXML = `<?xml version="1.0" encoding="UTF-8" standalone="yes"?>
<Relationships xmlns="http://schemas.openxmlformats.org/package/2006/relationships"><Relationship Id="rId1" Type="http://schemas.openxmlformats.org/officeDocument/2006/relationships/officeDocument" Target="word/document.xml"/></Relationships>`

var xmlEscaper = strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;")

func writeDocx(dst, text string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(f)
	parts := []struct{ name, content string }{
		{"[Content_Types].xml", contentTypesXML},
		{"_rels/.rels", relsXML},
		{"word/document.xml", documentXML(text)},
	}
	for _, p := range parts {
		w, err := zw.Create(p.name)
		if err != nil {
			_ = zw.Close()
			_ = f.Close()
			return err
		}
		if _, err := io.WriteString(w, p.content); err != nil {
			_ = zw.Close()
			_ = f.Close()
			return err
		}
	}
	if err := zw.Close(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func documentXML(text string) string {
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="UTF-8" standalone="yes"?>` + "\n")
	b.WriteString(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`)
	for _, line := range strings.Split(text, "\n") {
		b.WriteString(`<w:p><w:r><w:t xml:space="preserve">`)
		b.WriteString(xmlEscaper.Replace(line))
		b.WriteString(`</w:t></w:r></w:p>`)
	}
	b.WriteString(`</w:body></w:document>`)
	return b.String()
}
