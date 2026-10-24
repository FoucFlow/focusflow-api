package learn

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"strings"
	"testing"
	"time"
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

func TestBrokenPDFDoesNotPanic(t *testing.T) {
	broken := []byte("%PDF-1.4\n1 0 obj<</Type/Catalog>>endobj\ntrailer<</Root 1 0 R>>\n%%EOF\n")
	if n := pdfPageCount(broken); n != 0 {
		t.Fatalf("unreadable PDF has no page count, got %d", n)
	}
	if _, err := pdfPages(broken, 1, 2); err == nil {
		t.Fatal("splitting an unreadable PDF must fail")
	}
}

func TestSplitPDF(t *testing.T) {
	data, err := os.ReadFile("testdata/five-pages.pdf")
	if err != nil {
		t.Fatal(err)
	}
	if n := pdfPageCount(data); n != 5 {
		t.Fatalf("want 5 pages, got %d", n)
	}
	part, err := pdfPages(data, 3, 4)
	if err != nil {
		t.Fatal(err)
	}
	if n := pdfPageCount(part); n != 2 {
		t.Fatalf("pages 3-4 make a 2-page part, got %d", n)
	}
}

func TestParseISODurationAndClock(t *testing.T) {
	cases := map[string]int{"PT1H17M3S": 4623, "PT45S": 45, "PT10M": 600, "P1DT2H": 93600, "P0D": 0, "live": 0}
	for in, want := range cases {
		if got := parseISODuration(in); got != want {
			t.Errorf("%s: want %d, got %d", in, want, got)
		}
	}
	if clock(4623) != "1:17:03" || clock(605) != "10:05" {
		t.Fatalf("clock: %s %s", clock(4623), clock(605))
	}
}

func TestNoQuotaIsNotRetried(t *testing.T) {
	free := errors.New("gemini: Error 429, Message: You exceeded your current quota... limit: 0, model: gemini-3.1-pro")
	busy := errors.New("gemini: Error 429, RESOURCE_EXHAUSTED, limit: 15")
	if !noQuota(free) || retryableGemini(free) {
		t.Fatal("a model with no quota on the plan must not be retried")
	}
	if noQuota(busy) || !retryableGemini(busy) {
		t.Fatal("a busy model is retried")
	}
}

func TestParseGoogleLimits(t *testing.T) {
	msg := "gemini: Error 429, Message: You exceeded your current quota, please check your plan and billing details.\n" +
		"* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_input_token_count, limit: 0, model: gemini-3.1-pro, description: Generate content input tokens per day limit exceeded on Free Tier. See https://x\n" +
		"* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 250, model: gemini-2.5-flash, description: Generate content requests per day limit exceeded on Free Tier. See https://x\n" +
		"* Quota exceeded for metric: generativelanguage.googleapis.com/generate_content_free_tier_requests, limit: 10, model: gemini-2.5-flash, description: Generate content requests per minute limit exceeded on Free Tier. See https://x"
	got := parseGoogleLimits(msg)
	want := []GoogleLimit{{"input tokens per day (free tier)", 0}, {"requests per day (free tier)", 250}, {"requests per minute (free tier)", 10}}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %+v, want %+v", got, want)
		}
	}
}

func TestQuotaDayStartsAtPacificMidnight(t *testing.T) {
	now := time.Date(2026, 10, 25, 6, 30, 0, 0, time.UTC) // 23:30 on the 24th in California (PDT)
	start, next := quotaDayStart(now)
	if start.UTC() != time.Date(2026, 10, 24, 7, 0, 0, 0, time.UTC) || next.Sub(start) != 24*time.Hour {
		t.Fatalf("start %v next %v", start.UTC(), next.UTC())
	}
}
