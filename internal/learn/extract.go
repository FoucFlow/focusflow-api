package learn

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

const (
	maxPptxEntries   = 2000
	maxSlideXMLBytes = 5 << 20 // per slide, guards against zip bombs
	maxSlidesText    = 400_000 // characters sent to the AI
)

var slideName = regexp.MustCompile(`^ppt/slides/slide(\d+)\.xml$`)

// ExtractPptxText returns the text of each slide, in order, as
// "Slide N:\n<text>" blocks. Speaker notes are not included.
func ExtractPptxText(data []byte) (string, int, error) {
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return "", 0, errors.New("this is not a valid .pptx file")
	}
	if len(zr.File) > maxPptxEntries {
		return "", 0, errors.New("this .pptx file has too many parts")
	}
	type slide struct {
		n int
		f *zip.File
	}
	var slides []slide
	for _, f := range zr.File {
		if m := slideName.FindStringSubmatch(f.Name); m != nil {
			n, _ := strconv.Atoi(m[1])
			slides = append(slides, slide{n, f})
		}
	}
	if len(slides) == 0 {
		return "", 0, errors.New("no slides found in this .pptx file")
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].n < slides[j].n })

	var out strings.Builder
	for _, s := range slides {
		text, err := slideText(s.f)
		if err != nil {
			return "", 0, fmt.Errorf("slide %d: %w", s.n, err)
		}
		if text == "" {
			continue
		}
		fmt.Fprintf(&out, "Slide %d:\n%s\n\n", s.n, text)
		if out.Len() > maxSlidesText {
			break
		}
	}
	return strings.TrimSpace(out.String()), len(slides), nil
}

// slideText collects <a:t> runs, one line per paragraph <a:p>.
func slideText(f *zip.File) (string, error) {
	rc, err := f.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	dec := xml.NewDecoder(io.LimitReader(rc, maxSlideXMLBytes))
	var lines []string
	var para strings.Builder
	inText := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", errors.New("unreadable slide")
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				if line := strings.TrimSpace(para.String()); line != "" {
					lines = append(lines, line)
				}
				para.Reset()
			}
		case xml.CharData:
			if inText {
				para.Write(t)
			}
		}
	}
	return strings.Join(lines, "\n"), nil
}
