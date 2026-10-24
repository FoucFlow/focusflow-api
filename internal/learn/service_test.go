package learn

import (
	"net/url"
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
