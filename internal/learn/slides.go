package learn

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"io"
	"path"
	"sort"
	"strconv"
	"strings"
)

// Slides as written (F3.1): a .pptx becomes one page per slide, without AI.
// The teacher's text is kept word for word; only its structure (title,
// bullet levels, bold/italic/code, links, pictures, tables) is read.
// Slides drawn with shapes, SmartArt or charts are flagged as diagrams so the
// viewer can point learners to the original slide (F3.8).

const (
	maxDeckSlides     = 500
	maxSlideImage     = 15 << 20 // bytes per picture
	maxDeckImageBytes = 60 << 20 // all pictures of one deck
	emuPerInch        = 914400
)

// SlideBlock is one piece of a slide page, in reading order.
type SlideBlock struct {
	Type   string      `json:"type"`             // text | code | image | table | diagram
	Lines  []SlideLine `json:"lines,omitempty"`  // text
	Code   string      `json:"code,omitempty"`   // code
	Src    string      `json:"src,omitempty"`    // image: public URL
	Alt    string      `json:"alt,omitempty"`    // image: the teacher's alt text
	Rows   [][]string  `json:"rows,omitempty"`   // table
	Header bool        `json:"header,omitempty"` // table: first row is a header
	Media  string      `json:"-"`                // image: path inside the .pptx until uploaded

	fromShape bool // text written on a drawn shape (see slideReader.finish)
}

// SlideLine is one paragraph of text.
type SlideLine struct {
	Level  int        `json:"level"`  // 0 = top level
	Bullet bool       `json:"bullet"` // shown as a list item
	Runs   []SlideRun `json:"runs"`
}

// SlideRun is a stretch of text with one style.
type SlideRun struct {
	Text   string `json:"text"`
	Bold   bool   `json:"bold,omitempty"`
	Italic bool   `json:"italic,omitempty"`
	Code   bool   `json:"code,omitempty"`
	Link   string `json:"link,omitempty"`
}

// ParsedSlide is one slide as read from the file.
type ParsedSlide struct {
	Number     int // position in the presentation, from 1
	Title      string
	Blocks     []SlideBlock
	Notes      string
	Section    string // PowerPoint section the slide is in
	Hidden     bool
	HasDiagram bool
}

// Deck is a parsed .pptx. Media holds the bytes of every picture a block uses.
type Deck struct {
	Slides []ParsedSlide
	Media  map[string][]byte
}

// ── XML shapes ───────────────────────────────────────────────────────────
// Struct tags give local names only, so any namespace prefix matches.

type xRels struct {
	Rels []struct {
		ID         string `xml:"Id,attr"`
		Type       string `xml:"Type,attr"`
		Target     string `xml:"Target,attr"`
		TargetMode string `xml:"TargetMode,attr"`
	} `xml:"Relationship"`
}

type xNvPr struct {
	Ph *struct {
		Type string `xml:"type,attr"`
	} `xml:"ph"`
}

type xCNvPr struct {
	Descr string `xml:"descr,attr"`
	Title string `xml:"title,attr"`
}

type xShape struct {
	XMLName xml.Name
	NvSpPr  struct {
		CNvPr   xCNvPr `xml:"cNvPr"`
		CNvSpPr struct {
			TxBox string `xml:"txBox,attr"`
		} `xml:"cNvSpPr"`
		NvPr xNvPr `xml:"nvPr"`
	} `xml:"nvSpPr"`
	NvPicPr struct {
		CNvPr xCNvPr `xml:"cNvPr"`
		NvPr  xNvPr  `xml:"nvPr"`
	} `xml:"nvPicPr"`
	SpPr struct {
		Xfrm struct {
			Ext struct {
				Cx int64 `xml:"cx,attr"`
				Cy int64 `xml:"cy,attr"`
			} `xml:"ext"`
		} `xml:"xfrm"`
		PrstGeom struct {
			Prst string `xml:"prst,attr"`
		} `xml:"prstGeom"`
		CustGeom *struct{} `xml:"custGeom"`
	} `xml:"spPr"`
	TxBody   *xTxBody `xml:"txBody"`
	BlipFill struct {
		Blip struct {
			Embed string `xml:"embed,attr"`
		} `xml:"blip"`
	} `xml:"blipFill"`
	Graphic struct {
		Data struct {
			URI string  `xml:"uri,attr"`
			Tbl *xTable `xml:"tbl"`
		} `xml:"graphicData"`
	} `xml:"graphic"`
	// Children of a group, or of mc:AlternateContent > Choice/Fallback.
	// Other unknown child elements land here too and are ignored by name.
	Children []xShape `xml:",any"`
}

type xTxBody struct {
	Paras []xPara `xml:"p"`
}

type xPara struct {
	PPr struct {
		Lvl       string    `xml:"lvl,attr"`
		BuNone    *struct{} `xml:"buNone"`
		BuChar    *struct{} `xml:"buChar"`
		BuAutoNum *struct{} `xml:"buAutoNum"`
	} `xml:"pPr"`
	Items []xRunItem `xml:",any"`
}

// xRunItem is an a:r, a:fld or a:br inside a paragraph.
type xRunItem struct {
	XMLName xml.Name
	RPr     struct {
		B     string `xml:"b,attr"`
		I     string `xml:"i,attr"`
		Latin struct {
			Typeface string `xml:"typeface,attr"`
		} `xml:"latin"`
		Hlink struct {
			ID string `xml:"id,attr"`
		} `xml:"hlinkClick"`
	} `xml:"rPr"`
	T string `xml:"t"`
}

type xTable struct {
	TblPr struct {
		FirstRow string `xml:"firstRow,attr"`
	} `xml:"tblPr"`
	Rows []struct {
		Cells []struct {
			TxBody xTxBody `xml:"txBody"`
		} `xml:"tc"`
	} `xml:"tr"`
}

type xSlide struct {
	Show   string `xml:"show,attr"`
	Shapes struct {
		Children []xShape `xml:",any"`
	} `xml:"cSld>spTree"`
}

// ── parsing ──────────────────────────────────────────────────────────────

type pptx struct {
	files map[string]*zip.File
}

func (p *pptx) read(name string, limit int64) ([]byte, error) {
	f := p.files[name]
	if f == nil {
		return nil, errors.New("missing " + name)
	}
	rc, err := f.Open()
	if err != nil {
		return nil, err
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New(name + " is too large")
	}
	return data, nil
}

// rels reads the relationships of a part ("ppt/slides/slide1.xml") and
// returns id → relationship with targets resolved to zip paths.
func (p *pptx) rels(part string) map[string]rel {
	dir, base := path.Split(part)
	data, err := p.read(dir+"_rels/"+base+".rels", maxSlideXMLBytes)
	if err != nil {
		return nil
	}
	var x xRels
	if xml.Unmarshal(data, &x) != nil {
		return nil
	}
	out := map[string]rel{}
	for _, r := range x.Rels {
		target := r.Target
		external := strings.EqualFold(r.TargetMode, "External")
		if !external {
			if strings.HasPrefix(target, "/") {
				target = strings.TrimPrefix(target, "/")
			} else {
				target = path.Clean(dir + target)
			}
		}
		out[r.ID] = rel{typ: r.Type, target: target, external: external}
	}
	return out
}

type rel struct {
	typ, target string
	external    bool
}

// ParseSlides reads a .pptx into pages, in presentation order.
func ParseSlides(data []byte) (*Deck, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return nil, errors.New("this is not a valid .pptx file")
	}
	if len(zr.File) > maxPptxEntries {
		return nil, errors.New("this .pptx file has too many parts")
	}
	p := &pptx{files: map[string]*zip.File{}}
	for _, f := range zr.File {
		p.files[f.Name] = f
	}
	order, sections := p.slideOrder()
	if len(order) == 0 {
		return nil, errors.New("no slides found in this .pptx file")
	}
	if len(order) > maxDeckSlides {
		order = order[:maxDeckSlides]
	}
	deck := &Deck{Media: map[string][]byte{}}
	var mediaBytes int
	for i, part := range order {
		raw, err := p.read(part, maxSlideXMLBytes)
		if err != nil {
			return nil, errors.New("slide " + strconv.Itoa(i+1) + " could not be read")
		}
		var xs xSlide
		if err := xml.Unmarshal(raw, &xs); err != nil {
			return nil, errors.New("slide " + strconv.Itoa(i+1) + " could not be read")
		}
		rels := p.rels(part)
		s := ParsedSlide{Number: i + 1, Hidden: xs.Show == "0", Section: sections[part]}
		r := slideReader{rels: rels, slide: &s}
		r.shapes(xs.Shapes.Children, false)
		r.finish()
		for _, rl := range rels {
			if strings.HasSuffix(rl.typ, "/notesSlide") {
				s.Notes = p.notes(rl.target)
			}
		}
		// Pictures: keep the bytes once; drop formats browsers cannot show.
		kept := s.Blocks[:0]
		for _, b := range s.Blocks {
			if b.Type == "image" {
				if _, ok := deck.Media[b.Media]; !ok {
					img, err := p.read(b.Media, maxSlideImage)
					if err != nil || mediaBytes+len(img) > maxDeckImageBytes {
						if !s.HasDiagram { // shown via the original slide instead
							s.HasDiagram = true
							kept = append(kept, SlideBlock{Type: "diagram"})
						}
						continue
					}
					deck.Media[b.Media] = img
					mediaBytes += len(img)
				}
			}
			kept = append(kept, b)
		}
		s.Blocks = kept
		deck.Slides = append(deck.Slides, s)
	}
	return deck, nil
}

// slideOrder returns slide parts in presentation order (presentation.xml),
// falling back to file-name order, and the section name of each slide.
func (p *pptx) slideOrder() ([]string, map[string]string) {
	sections := map[string]string{}
	pres, err := p.read("ppt/presentation.xml", maxSlideXMLBytes)
	rels := p.rels("ppt/presentation.xml")
	var order []string
	if err == nil && rels != nil {
		// sldId carries id="256" and r:id="rId2"; read both by namespace.
		idToPart := map[string]string{}
		dec := xml.NewDecoder(bytes.NewReader(pres))
		var curSection string
		for {
			tok, err := dec.Token()
			if err != nil {
				break
			}
			st, ok := tok.(xml.StartElement)
			if !ok {
				continue
			}
			switch st.Name.Local {
			case "sldId":
				var id, rid string
				for _, a := range st.Attr {
					if a.Name.Local == "id" && a.Name.Space == "" {
						id = a.Value
					} else if a.Name.Local == "id" {
						rid = a.Value
					}
				}
				if curSection != "" {
					// p14:sldId inside a p14:section (no r:id)
					if part := idToPart[id]; part != "" {
						sections[part] = curSection
					}
					continue
				}
				if r, ok := rels[rid]; ok && strings.HasSuffix(r.typ, "/slide") && p.files[r.target] != nil {
					order = append(order, r.target)
					idToPart[id] = r.target
				}
			case "section":
				for _, a := range st.Attr {
					if a.Name.Local == "name" {
						curSection = a.Value
					}
				}
			}
		}
	}
	if len(order) > 0 {
		return order, sections
	}
	type numbered struct {
		n    int
		part string
	}
	var found []numbered
	for name := range p.files {
		if m := slideName.FindStringSubmatch(name); m != nil {
			n, _ := strconv.Atoi(m[1])
			found = append(found, numbered{n, name})
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].n < found[j].n })
	for _, f := range found {
		order = append(order, f.part)
	}
	return order, sections
}

// notes returns the speaker notes text (the notes body placeholder).
func (p *pptx) notes(part string) string {
	raw, err := p.read(part, maxSlideXMLBytes)
	if err != nil {
		return ""
	}
	var xs xSlide
	if xml.Unmarshal(raw, &xs) != nil {
		return ""
	}
	var lines []string
	var walk func([]xShape)
	walk = func(shapes []xShape) {
		for _, sh := range shapes {
			switch sh.XMLName.Local {
			case "sp":
				if ph := sh.NvSpPr.NvPr.Ph; ph != nil && ph.Type == "body" && sh.TxBody != nil {
					for _, para := range sh.TxBody.Paras {
						if t := strings.TrimSpace(plainText(para)); t != "" {
							lines = append(lines, t)
						}
					}
				}
			case "grpSp":
				walk(sh.Children)
			}
		}
	}
	walk(xs.Shapes.Children)
	return strings.Join(lines, "\n")
}

type slideReader struct {
	rels  map[string]rel
	slide *ParsedSlide
	drawn int // drawn shapes that are not placeholders, text boxes or pictures
}

// shapes walks a shape tree in order. inGroup: inside a group (a drawing).
func (r *slideReader) shapes(list []xShape, inGroup bool) {
	for _, sh := range list {
		switch sh.XMLName.Local {
		case "sp":
			r.shape(sh, inGroup)
		case "pic":
			if e := sh.BlipFill.Blip.Embed; e != "" {
				if rl, ok := r.rels[e]; ok && !rl.external && webImage(rl.target) {
					if tooSmall(sh) {
						continue
					}
					alt := strings.TrimSpace(sh.NvPicPr.CNvPr.Descr)
					if alt == "" {
						alt = strings.TrimSpace(sh.NvPicPr.CNvPr.Title)
					}
					r.slide.Blocks = append(r.slide.Blocks, SlideBlock{Type: "image", Media: rl.target, Alt: alt})
				} else {
					r.diagram() // EMF/WMF/TIFF or linked picture
				}
			}
		case "graphicFrame":
			uri := sh.Graphic.Data.URI
			switch {
			case sh.Graphic.Data.Tbl != nil:
				r.table(sh.Graphic.Data.Tbl)
			case strings.HasSuffix(uri, "/chart"), strings.HasSuffix(uri, "/diagram"),
				strings.Contains(uri, "chartex"), strings.HasSuffix(uri, "/ole"):
				r.diagram()
			}
		case "grpSp":
			r.diagram()
			r.shapes(sh.Children, true)
		case "cxnSp":
			r.diagram()
		case "AlternateContent":
			// Prefer the fallback: it is what older readers show.
			var choice, fallback []xShape
			for _, c := range sh.Children {
				switch c.XMLName.Local {
				case "Choice":
					if choice == nil {
						choice = c.Children
					}
				case "Fallback":
					fallback = c.Children
				}
			}
			if fallback != nil {
				r.shapes(fallback, inGroup)
			} else {
				r.shapes(choice, inGroup)
			}
		}
	}
}

func (r *slideReader) shape(sh xShape, inGroup bool) {
	ph := sh.NvSpPr.NvPr.Ph
	if ph != nil {
		switch ph.Type {
		case "sldNum", "dt", "ftr", "hdr", "sldImg":
			return
		case "title", "ctrTitle":
			if sh.TxBody != nil && r.slide.Title == "" {
				var parts []string
				for _, para := range sh.TxBody.Paras {
					if t := strings.TrimSpace(plainText(para)); t != "" {
						parts = append(parts, t)
					}
				}
				r.slide.Title = strings.Join(parts, " ")
				return
			}
		}
	}
	textBox := sh.NvSpPr.CNvSpPr.TxBox == "1"
	if ph == nil && !textBox {
		// A drawn shape (box, arrow, circle). Shapes with geometry other than
		// a plain rectangle, or several boxes, make a diagram.
		r.drawn++
		if g := sh.SpPr.PrstGeom.Prst; (g != "" && g != "rect") || sh.SpPr.CustGeom != nil || r.drawn >= 3 {
			r.diagram()
		}
	}
	if inGroup || sh.TxBody == nil {
		return // labels inside a drawing are shown on the original slide
	}
	// Body placeholders are bulleted unless a paragraph says otherwise;
	// text boxes and shapes only when the paragraph asks for a bullet.
	bulleted := ph != nil && ph.Type != "subTitle" && ph.Type != "title" && ph.Type != "ctrTitle"
	r.text(sh.TxBody, bulleted, ph == nil && !textBox)
}

// text appends the paragraphs of a text body as text and code blocks.
// fromShape: text written on a drawn shape (kept, but it may become plain
// text of a diagram once the whole slide is known; see finish).
func (r *slideReader) text(body *xTxBody, bulleted, fromShape bool) {
	var lines []SlideLine
	var code []string
	flushLines := func() {
		if len(lines) > 0 {
			r.slide.Blocks = append(r.slide.Blocks, SlideBlock{Type: "text", Lines: lines, fromShape: fromShape})
			lines = nil
		}
	}
	flushCode := func() {
		if len(code) > 0 {
			r.slide.Blocks = append(r.slide.Blocks, SlideBlock{Type: "code", Code: strings.Join(code, "\n"), fromShape: fromShape})
			code = nil
		}
	}
	for _, para := range body.Paras {
		line, isCode := r.line(para, bulleted)
		if isCode {
			flushLines()
			indent := strings.Repeat("    ", line.Level)
			code = append(code, indent+joinRuns(line.Runs))
			continue
		}
		if len(line.Runs) == 0 {
			continue // empty paragraph: spacing only
		}
		flushCode()
		lines = append(lines, line)
	}
	flushLines()
	flushCode()
}

// diagram flags the slide and places one "diagram" block where the drawing
// starts, so the viewer shows the "see the original" card there (F3.8).
func (r *slideReader) diagram() {
	if !r.slide.HasDiagram {
		r.slide.HasDiagram = true
		r.slide.Blocks = append(r.slide.Blocks, SlideBlock{Type: "diagram"})
	}
}

// finish drops text written on drawn shapes when the slide is a diagram
// (those words are labels of the drawing).
func (r *slideReader) finish() {
	kept := r.slide.Blocks[:0]
	for _, b := range r.slide.Blocks {
		if b.fromShape && r.slide.HasDiagram {
			continue
		}
		kept = append(kept, b)
	}
	r.slide.Blocks = kept
}

// line reads one paragraph. isCode: every run with text is in a monospace font.
func (r *slideReader) line(para xPara, bulleted bool) (SlideLine, bool) {
	l := SlideLine{}
	if n, err := strconv.Atoi(para.PPr.Lvl); err == nil && n > 0 {
		l.Level = n
	}
	switch {
	case para.PPr.BuNone != nil:
		l.Bullet = false
	case para.PPr.BuChar != nil, para.PPr.BuAutoNum != nil:
		l.Bullet = true
	default:
		l.Bullet = bulleted
	}
	mono, hasText := true, false
	for _, it := range para.Items {
		var run SlideRun
		switch it.XMLName.Local {
		case "r", "fld":
			run = SlideRun{Text: it.T, Bold: it.RPr.B == "1" || it.RPr.B == "true",
				Italic: it.RPr.I == "1" || it.RPr.I == "true", Code: monospace(it.RPr.Latin.Typeface)}
			if id := it.RPr.Hlink.ID; id != "" {
				if rl, ok := r.rels[id]; ok && rl.external && safeLink(rl.target) {
					run.Link = rl.target
				}
			}
		case "br":
			run = SlideRun{Text: "\n"}
		default:
			continue
		}
		if run.Text == "" {
			continue
		}
		if strings.TrimSpace(run.Text) != "" {
			hasText = true
			if !run.Code {
				mono = false
			}
		}
		// Merge with the previous run when the style is the same.
		if n := len(l.Runs); n > 0 {
			p := &l.Runs[n-1]
			if p.Bold == run.Bold && p.Italic == run.Italic && p.Code == run.Code && p.Link == run.Link {
				p.Text += run.Text
				continue
			}
		}
		l.Runs = append(l.Runs, run)
	}
	if !hasText {
		return SlideLine{Level: l.Level}, false
	}
	if mono {
		return l, true
	}
	return l, false
}

func (r *slideReader) table(t *xTable) {
	var rows [][]string
	for _, row := range t.Rows {
		var cells []string
		for _, c := range row.Cells {
			var parts []string
			for _, para := range c.TxBody.Paras {
				if s := strings.TrimSpace(plainText(para)); s != "" {
					parts = append(parts, s)
				}
			}
			cells = append(cells, strings.Join(parts, "\n"))
		}
		rows = append(rows, cells)
	}
	if len(rows) > 0 {
		r.slide.Blocks = append(r.slide.Blocks, SlideBlock{Type: "table", Rows: rows, Header: t.TblPr.FirstRow == "1"})
	}
}

func plainText(para xPara) string {
	var b strings.Builder
	for _, it := range para.Items {
		switch it.XMLName.Local {
		case "r", "fld":
			b.WriteString(it.T)
		case "br":
			b.WriteString("\n")
		}
	}
	return b.String()
}

func joinRuns(runs []SlideRun) string {
	var b strings.Builder
	for _, r := range runs {
		b.WriteString(r.Text)
	}
	return b.String()
}

var monoFonts = []string{"consolas", "courier", "menlo", "monaco", "lucida console", "source code",
	"fira code", "fira mono", "jetbrains mono", "roboto mono", "ubuntu mono", "dejavu sans mono",
	"sf mono", "cascadia", "inconsolata", "andale mono", "liberation mono", "ibm plex mono"}

func monospace(face string) bool {
	f := strings.ToLower(face)
	if f == "" {
		return false
	}
	for _, m := range monoFonts {
		if strings.Contains(f, m) {
			return true
		}
	}
	return false
}

func webImage(name string) bool {
	switch strings.ToLower(path.Ext(name)) {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp":
		return true // not SVG: it can carry scripts (PowerPoint keeps a PNG copy as the embed)
	}
	return false
}

// tooSmall skips icons and bullets drawn as tiny pictures (under 0.3 inch).
func tooSmall(sh xShape) bool {
	ext := sh.SpPr.Xfrm.Ext
	if ext.Cx == 0 || ext.Cy == 0 {
		return false // size not given (inherited from a placeholder)
	}
	min := int64(emuPerInch * 3 / 10)
	return ext.Cx < min && ext.Cy < min
}

func safeLink(u string) bool {
	l := strings.ToLower(u)
	return strings.HasPrefix(l, "https://") || strings.HasPrefix(l, "http://") || strings.HasPrefix(l, "mailto:")
}

// imageMime is the content type to store a slide picture with.
func imageMime(name string) string {
	switch strings.ToLower(path.Ext(name)) {
	case ".png":
		return "image/png"
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".bmp":
		return "image/bmp"
	}
	return "application/octet-stream"
}
