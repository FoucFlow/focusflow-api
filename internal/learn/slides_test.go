package learn

import (
	"os"
	"strings"
	"testing"
)

// testdata/lecture.pptx is made with python-pptx (real PowerPoint XML): the
// code slide is file slide3.xml but moved to the end in presentation.xml,
// there are two sections, a hidden slide, notes, a tiny icon picture, a
// drawn diagram (shapes + connector) and a chart.
func lectureDeck(t *testing.T) *Deck {
	t.Helper()
	data, err := os.ReadFile("testdata/lecture.pptx")
	if err != nil {
		t.Fatal(err)
	}
	d, err := ParseSlides(data)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestParseSlidesPresentationOrderAndSections(t *testing.T) {
	d := lectureDeck(t)
	want := []struct{ title, section string }{
		{"Recursion", "Basics"}, {"Why we need a base case", "Basics"}, {"The call stack", "Basics"},
		{"Calls for fact(3)", "Going deeper"}, {"How recursion unwinds", "Going deeper"},
		{"Calls grow with n", "Going deeper"}, {"Extra: memoisation", "Going deeper"},
		{"Factorial in Python", "Going deeper"},
	}
	if len(d.Slides) != len(want) {
		t.Fatalf("got %d slides, want %d", len(d.Slides), len(want))
	}
	for i, w := range want {
		s := d.Slides[i]
		if s.Number != i+1 || s.Title != w.title || s.Section != w.section {
			t.Errorf("slide %d = #%d %q in %q, want %q in %q", i+1, s.Number, s.Title, s.Section, w.title, w.section)
		}
	}
	if !d.Slides[6].Hidden || d.Slides[1].Hidden {
		t.Error("only slide 7 should be hidden")
	}
}

func TestParseSlidesKeepsTextWordForWord(t *testing.T) {
	s := lectureDeck(t).Slides[1]
	if len(s.Blocks) != 1 || s.Blocks[0].Type != "text" {
		t.Fatalf("blocks = %+v", s.Blocks)
	}
	lines := s.Blocks[0].Lines
	if len(lines) != 5 {
		t.Fatalf("got %d lines", len(lines))
	}
	if got := joinRuns(lines[1].Runs); got != "The base case answers the smallest problem directly" {
		t.Errorf("line 2 = %q", got)
	}
	if r := lines[1].Runs[1]; r.Text != "base case" || !r.Bold {
		t.Errorf("bold run = %+v", r)
	}
	if lines[2].Level != 1 || !lines[2].Bullet || lines[0].Level != 0 {
		t.Errorf("levels: %+v %+v", lines[0], lines[2])
	}
	if r := lines[3].Runs[1]; r.Text != "stack overflow" || !r.Italic {
		t.Errorf("italic run = %+v", r)
	}
	if r := lines[4].Runs[0]; r.Link != "https://docs.python.org/3/tutorial/" {
		t.Errorf("link = %+v", r)
	}
	if s.Notes != "Ask students what happens without a base case." {
		t.Errorf("notes = %q", s.Notes)
	}
	// Subtitle on the title slide is text, not a bullet.
	if l := lectureDeck(t).Slides[0].Blocks[0].Lines[0]; l.Bullet {
		t.Error("subtitle should not be a bullet")
	}
}

func TestParseSlidesCodeTablePicture(t *testing.T) {
	d := lectureDeck(t)
	code := d.Slides[7].Blocks
	if len(code) != 1 || code[0].Type != "code" ||
		code[0].Code != "def fact(n):\n    if n == 0:\n        return 1\n    return n * fact(n - 1)" {
		t.Errorf("code = %+v", code)
	}
	tbl := d.Slides[3].Blocks
	if len(tbl) != 1 || tbl[0].Type != "table" || !tbl[0].Header || len(tbl[0].Rows) != 3 || tbl[0].Rows[2][1] != "6" {
		t.Errorf("table = %+v", tbl)
	}
	pic := d.Slides[2].Blocks
	if len(pic) != 1 || pic[0].Type != "image" || pic[0].Alt != "Stack of four fact() calls" {
		t.Fatalf("picture = %+v (the tiny icon must be skipped)", pic)
	}
	if len(d.Media) != 1 || len(d.Media[pic[0].Media]) == 0 {
		t.Errorf("media = %d files", len(d.Media))
	}
}

func TestParseSlidesFlagsDiagrams(t *testing.T) {
	d := lectureDeck(t)
	drawn := d.Slides[4]
	if !drawn.HasDiagram || len(drawn.Blocks) != 2 || drawn.Blocks[0].Type != "text" || drawn.Blocks[1].Type != "diagram" {
		t.Fatalf("drawn diagram blocks = %+v", drawn.Blocks)
	}
	for _, b := range drawn.Blocks {
		for _, l := range b.Lines {
			if strings.Contains(joinRuns(l.Runs), "fact(") {
				t.Error("labels inside the drawing must not become text")
			}
		}
	}
	chart := d.Slides[5]
	if !chart.HasDiagram || len(chart.Blocks) != 1 || chart.Blocks[0].Type != "diagram" {
		t.Errorf("chart blocks = %+v", chart.Blocks)
	}
	for _, i := range []int{0, 1, 2, 3, 6, 7} {
		if d.Slides[i].HasDiagram {
			t.Errorf("slide %d flagged as diagram", i+1)
		}
	}
}

func TestParseSlidesFallsBackToFileOrder(t *testing.T) {
	// No presentation.xml (like the minimal decks in extract_test.go).
	data := makePptx(t, map[string]string{
		"ppt/slides/slide10.xml": strings.Replace(slideXML, "%s", "Ten", 1),
		"ppt/slides/slide2.xml":  strings.Replace(slideXML, "%s", "Two", 1),
	})
	d, err := ParseSlides(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(d.Slides) != 2 || joinRuns(d.Slides[0].Blocks[0].Lines[0].Runs) != "Two" {
		t.Errorf("slides = %+v", d.Slides)
	}
}

func TestParseSlidesRejectsNonPptx(t *testing.T) {
	if _, err := ParseSlides([]byte("%PDF-1.4")); err == nil {
		t.Error("want error for a PDF")
	}
}

func TestMonospaceAndLinks(t *testing.T) {
	if !monospace("Consolas") || !monospace("Courier New") || monospace("Calibri") || monospace("") {
		t.Error("monospace detection")
	}
	if safeLink("javascript:alert(1)") || !safeLink("https://x.org") {
		t.Error("only http(s) and mailto links are kept")
	}
}
