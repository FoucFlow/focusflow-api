package learn

import (
	"archive/zip"
	"bytes"
	"strings"
	"testing"
)

func makePptx(t *testing.T, slides map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range slides {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(body))
	}
	zw.Close()
	return buf.Bytes()
}

const slideXML = `<?xml version="1.0"?><p:sld xmlns:p="p" xmlns:a="a"><p:cSld><p:spTree>
<p:sp><p:txBody><a:p><a:r><a:t>%s</a:t></a:r></a:p><a:p><a:r><a:t>Point </a:t></a:r><a:r><a:t>two</a:t></a:r></a:p></p:txBody></p:sp>
</p:spTree></p:cSld></p:sld>`

func TestExtractPptxTextOrdersSlidesNumerically(t *testing.T) {
	data := makePptx(t, map[string]string{
		"ppt/slides/slide10.xml":          strings.Replace(slideXML, "%s", "Ten", 1),
		"ppt/slides/slide2.xml":           strings.Replace(slideXML, "%s", "Two", 1),
		"ppt/slides/slide1.xml":           strings.Replace(slideXML, "%s", "One", 1),
		"ppt/notesSlides/notesSlide1.xml": "<x><a:t>notes</a:t></x>",
	})
	text, n, err := ExtractPptxText(data)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Fatalf("slides = %d, want 3", n)
	}
	one, two, ten := strings.Index(text, "One"), strings.Index(text, "Two"), strings.Index(text, "Ten")
	if !(one < two && two < ten) {
		t.Fatalf("slides out of order:\n%s", text)
	}
	if !strings.Contains(text, "Point two") {
		t.Fatalf("runs in one paragraph must join: %q", text)
	}
	if strings.Contains(text, "notes") {
		t.Fatal("speaker notes must not be included")
	}
}

func TestExtractPptxTextRejectsNonPptx(t *testing.T) {
	if _, _, err := ExtractPptxText([]byte("%PDF-1.4 not a zip")); err == nil {
		t.Fatal("expected an error")
	}
	if _, _, err := ExtractPptxText(makePptx(t, map[string]string{"word/document.xml": "<x/>"})); err == nil {
		t.Fatal("a zip without slides must be rejected")
	}
}
