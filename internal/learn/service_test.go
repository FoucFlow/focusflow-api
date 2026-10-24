package learn

import (
	"fmt"
	"net"
	"net/url"
	"strings"
	"testing"
)

func TestUnlockedOpensUnitsInOrder(t *testing.T) {
	units := []Unit{{ID: 30, Position: 3}, {ID: 10, Position: 1}, {ID: 20, Position: 2}}
	open := unlocked(units, map[int]bool{})
	if !open[10] || open[20] || open[30] {
		t.Fatalf("only the first unit opens at start: %v", open)
	}
	open = unlocked(units, map[int]bool{10: true})
	if !open[10] || !open[20] || open[30] {
		t.Fatalf("finishing unit 1 opens unit 2 only: %v", open)
	}
	// Finishing a later unit does not skip an unfinished earlier one
	open = unlocked(units, map[int]bool{20: true})
	if open[20] || open[30] {
		t.Fatalf("unit 2 and 3 stay locked until unit 1 is done: %v", open)
	}
	open = unlocked(units, map[int]bool{10: true, 20: true, 30: true})
	if !(open[10] && open[20] && open[30]) {
		t.Fatalf("all open when all done: %v", open)
	}
}

func TestYouTubeID(t *testing.T) {
	cases := map[string]string{
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ":          "dQw4w9WgXcQ",
		"https://youtu.be/dQw4w9WgXcQ?t=42":                    "dQw4w9WgXcQ",
		"https://m.youtube.com/watch?v=dQw4w9WgXcQ&list=x":     "dQw4w9WgXcQ",
		"https://www.youtube.com/embed/dQw4w9WgXcQ":            "dQw4w9WgXcQ",
		"https://www.youtube.com/shorts/dQw4w9WgXcQ":           "dQw4w9WgXcQ",
		"https://vimeo.com/123456":                             "",
		"https://www.youtube.com/watch?v=short":                "",
		"https://evil.com/watch?v=dQw4w9WgXcQ":                 "",
		"https://www.youtube.com/watch?v=dQw4w9WgXcQ%3Cscript": "",
	}
	for raw, want := range cases {
		u, _ := url.Parse(raw)
		if got := YouTubeID(u); got != want {
			t.Errorf("YouTubeID(%s) = %q, want %q", raw, got, want)
		}
	}
}

func TestResolveSourcesDropsUnknownAndMergesPerMaterial(t *testing.T) {
	refs := map[string]ConceptSource{
		"M1":    {MaterialID: 1},
		"M1-C1": {MaterialID: 1, Pages: []int{4, 5}},
		"M1-C2": {MaterialID: 1, Pages: []int{5, 6}},
		"M2-C1": {MaterialID: 2, Slides: []int{7}},
		"M3":    {MaterialID: 3},
	}
	got := resolveSources([]string{"m1-c1", "M9-C9", "M2-C1", "M1-C2", " M3 ", "M1-C2"}, refs)
	if len(got) != 3 {
		t.Fatalf("want 3 sources (materials 1, 2, 3), got %+v", got)
	}
	if got[0].MaterialID != 1 || fmt.Sprint(got[0].Pages) != "[4 5 6]" {
		t.Fatalf("material 1 pages must merge to 4–6: %+v", got[0])
	}
	if got[1].MaterialID != 2 || fmt.Sprint(got[1].Slides) != "[7]" {
		t.Fatalf("material 2: %+v", got[1])
	}
	if got[2].MaterialID != 3 || len(got[2].Pages)+len(got[2].Slides) != 0 {
		t.Fatalf("material 3 (whole video): %+v", got[2])
	}
}

func TestSourceHintsAndLinks(t *testing.T) {
	if h := sourceHint(ConceptSource{Pages: []int{4, 5, 6, 9}}); h != "pages 4–6, 9" {
		t.Fatalf("hint = %q", h)
	}
	if h := sourceHint(ConceptSource{Slides: []int{3}}); h != "slide 3" {
		t.Fatalf("hint = %q", h)
	}
	pdf := Material{Kind: KindPDF, URL: "https://x/m.pdf"}
	if u := sourceURL(pdf, ConceptSource{Pages: []int{12, 13}}); u != "https://x/m.pdf#page=12" {
		t.Fatalf("pdf link = %q", u)
	}
	start := 750
	vid := Material{Kind: KindVideo, YouTubeID: "dQw4w9WgXcQ"}
	if u := sourceURL(vid, ConceptSource{StartSeconds: &start}); u != "https://www.youtube.com/watch?v=dQw4w9WgXcQ&t=750s" {
		t.Fatalf("video link = %q", u)
	}
}

func TestExtractJSONAndURLMatching(t *testing.T) {
	for _, in := range []string{
		"Here you go:\n```json\n{\"concepts\":[]}\n```\nDone.",
		"{\"concepts\":[]}",
		"Sure! {\"concepts\":[]} Hope this helps.",
	} {
		if got := extractJSON(in); got != `{"concepts":[]}` {
			t.Errorf("extractJSON(%q) = %q", in, got)
		}
	}
	if extractJSON("no json here") != "" {
		t.Error("no object should give empty string")
	}
	same := []string{"https://www.example.com/a/", "http://example.com/a", "https://EXAMPLE.com/a#intro"}
	for _, u := range same {
		if normaliseURL(u) != normaliseURL(same[0]) {
			t.Errorf("%q should match %q", u, same[0])
		}
	}
	if normaliseURL("https://example.com/a?x=1") == normaliseURL("https://example.com/a?x=2") {
		t.Error("different queries are different pages")
	}
}

func TestSplitConcepts(t *testing.T) {
	text := "### CONCEPT 1\n**What it is**: A.\n```python\nprint(\"hi \\\\ there\")\n```\n\n### CONCEPT 2\nB text\n## CONCEPT 3\nC\n### CONCEPT 9\nignored"
	got := splitConcepts(text, 3)
	if !strings.Contains(got[0], `print("hi \\ there")`) || got[1] != "B text" || got[2] != "C" {
		t.Fatalf("split = %#v", got)
	}
	if missing := splitConcepts("### CONCEPT 1\nonly one", 2); missing[1] != "" {
		t.Fatal("missing sections stay empty (then retried)")
	}
}

func TestIsPublicIP(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "10.1.2.3", "172.19.0.5", "192.168.1.1", "169.254.169.254", "::1", "fc00::1", "0.0.0.0"} {
		if isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s must be refused", s)
		}
	}
	for _, s := range []string{"8.8.8.8", "151.101.1.69", "2606:4700::6810:84e5"} {
		if !isPublicIP(net.ParseIP(s)) {
			t.Errorf("%s should be allowed", s)
		}
	}
}

func TestFillExplanations(t *testing.T) {
	unit := OutlineUnit{Concepts: []OutlineConcept{{Title: "A", Sources: []string{"M1-C1"}}, {Title: "B"}}}
	written, ok := fillExplanations(unit, []string{"text A", "text B"})
	if !ok || written[0].Explanation != "text A" || written[0].Sources[0] != "M1-C1" || written[1].Title != "B" {
		t.Fatalf("written = %+v ok=%v", written, ok)
	}
	if _, ok := fillExplanations(unit, []string{"text A", "  "}); ok {
		t.Fatal("a missing explanation must not count as complete")
	}
}
