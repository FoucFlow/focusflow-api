package learn

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
)

// Page editor (F3.6): the teacher fixes a slide page's text, hides it, or
// marks it "see the original". Pictures can be kept, moved or removed but not
// added (only the deck's own pictures are allowed).

const (
	maxPageBlocks = 80
	maxBlockText  = 20000
)

type pageIn struct {
	Title   *string       `json:"title"`
	Hidden  *bool         `json:"hidden"`
	Diagram *bool         `json:"diagram"` // show the "see the original" card
	Blocks  *[]SlideBlock `json:"blocks"`
}

// PUT /api/learn/slide-pages/:pid {title?, hidden?, diagram?, blocks?}
func (h *Handler) UpdateSlidePage(c *gin.Context) {
	pid, ok := paramID(c, "pid")
	if !ok {
		return
	}
	var p SlidePage
	if err := h.s.db.First(&p, pid).Error; err != nil {
		fail(c, http.StatusNotFound, "Page not found.")
		return
	}
	c.Params = append(c.Params, gin.Param{Key: "mid", Value: itoa(p.MaterialID)})
	if _, ok := h.ownedMaterial(c); !ok {
		return
	}
	var in pageIn
	if err := c.ShouldBindJSON(&in); err != nil {
		fail(c, http.StatusBadRequest, "The request body must be valid JSON.")
		return
	}
	if in.Title != nil {
		p.Title = clip(strings.TrimSpace(*in.Title), 500)
	}
	if in.Hidden != nil {
		p.Hidden = *in.Hidden
	}
	if in.Blocks != nil {
		blocks, msg := cleanBlocks(*in.Blocks, p.Blocks)
		if msg != "" {
			fail(c, http.StatusUnprocessableEntity, msg)
			return
		}
		p.Blocks = blocks
	}
	if in.Diagram != nil {
		p.Blocks = setDiagram(p.Blocks, *in.Diagram)
	}
	p.HasDiagram = false
	for _, b := range p.Blocks {
		if b.Type == "diagram" {
			p.HasDiagram = true
		}
	}
	p.Edited = true
	if err := h.s.db.Model(&p).Select("title", "hidden", "blocks", "has_diagram", "edited").Updates(&p).Error; err != nil {
		fail(c, http.StatusInternalServerError, "Could not save the page.")
		return
	}
	c.JSON(http.StatusOK, gin.H{"data": p})
}

// cleanBlocks validates edited blocks. Pictures must be ones the page had.
func cleanBlocks(in []SlideBlock, old []SlideBlock) ([]SlideBlock, string) {
	if len(in) > maxPageBlocks {
		return nil, "A page can have at most 80 blocks."
	}
	pics := map[string]bool{}
	for _, b := range old {
		if b.Type == "image" {
			pics[b.Src] = true
		}
	}
	out := make([]SlideBlock, 0, len(in))
	for _, b := range in {
		switch b.Type {
		case "text":
			var lines []SlideLine
			size := 0
			for _, l := range b.Lines {
				if l.Level < 0 || l.Level > 8 {
					l.Level = 0
				}
				var runs []SlideRun
				for _, r := range l.Runs {
					if r.Text == "" {
						continue
					}
					size += len(r.Text)
					if r.Link != "" && !safeLink(r.Link) {
						r.Link = ""
					}
					runs = append(runs, SlideRun{Text: r.Text, Bold: r.Bold, Italic: r.Italic, Code: r.Code, Link: r.Link})
				}
				if len(runs) > 0 {
					lines = append(lines, SlideLine{Level: l.Level, Bullet: l.Bullet, Runs: runs})
				}
			}
			if size > maxBlockText {
				return nil, "A text block is too long."
			}
			if len(lines) > 0 {
				out = append(out, SlideBlock{Type: "text", Lines: lines})
			}
		case "code":
			if len(b.Code) > maxBlockText {
				return nil, "A code block is too long."
			}
			if strings.TrimSpace(b.Code) != "" {
				out = append(out, SlideBlock{Type: "code", Code: b.Code})
			}
		case "image":
			if !pics[b.Src] {
				return nil, "Only the slide's own pictures can be used."
			}
			out = append(out, SlideBlock{Type: "image", Src: b.Src, Alt: clip(strings.TrimSpace(b.Alt), 500)})
		case "table":
			if len(b.Rows) > 100 {
				return nil, "A table can have at most 100 rows."
			}
			var rows [][]string
			for _, r := range b.Rows {
				if len(r) > 20 {
					return nil, "A table can have at most 20 columns."
				}
				row := make([]string, len(r))
				for i, cell := range r {
					row[i] = clip(cell, 2000)
				}
				rows = append(rows, row)
			}
			if len(rows) > 0 {
				out = append(out, SlideBlock{Type: "table", Rows: rows, Header: b.Header})
			}
		case "diagram":
			out = append(out, SlideBlock{Type: "diagram"})
		default:
			return nil, "Unknown block type: " + b.Type
		}
	}
	return out, ""
}

// setDiagram adds the "see the original" card at the end, or removes it.
func setDiagram(blocks []SlideBlock, on bool) []SlideBlock {
	out := blocks[:0:0]
	for _, b := range blocks {
		if b.Type != "diagram" {
			out = append(out, b)
		}
	}
	if on {
		out = append(out, SlideBlock{Type: "diagram"})
	}
	return out
}

func itoa(n int) string { return strconv.Itoa(n) }
