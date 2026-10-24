package learn

import (
	"bytes"
	"fmt"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
)

func pdfConf() *model.Configuration {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	return conf
}

// pdfcpu can panic on malformed files; both helpers turn that into an error.

// pdfPageCount returns the number of pages, or 0 if the file can't be parsed
// (it is then read in one piece, as before).
func pdfPageCount(data []byte) (n int) {
	defer func() {
		if recover() != nil {
			n = 0
		}
	}()
	n, err := api.PageCount(bytes.NewReader(data), pdfConf())
	if err != nil {
		return 0
	}
	return n
}

// pdfPages cuts pages from..to (1-based, inclusive) into a new PDF.
func pdfPages(data []byte, from, to int) (part []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			part, err = nil, fmt.Errorf("pdf split: %v", r)
		}
	}()
	var out bytes.Buffer
	if err := api.Trim(bytes.NewReader(data), &out, []string{fmt.Sprintf("%d-%d", from, to)}, pdfConf()); err == nil {
		return out.Bytes(), nil
	} else if part, err2 := pdfPagesUnvalidated(data, from, to); err2 == nil {
		// Many real PDFs break the spec a little (e.g. a missing font entry):
		// copy the pages without validating
		return part, nil
	} else {
		return nil, err
	}
}

func pdfPagesUnvalidated(data []byte, from, to int) ([]byte, error) {
	ctx, err := api.ReadContext(bytes.NewReader(data), pdfConf())
	if err != nil {
		return nil, err
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, err
	}
	if to > ctx.PageCount {
		to = ctx.PageCount
	}
	var nrs []int
	for p := from; p <= to; p++ {
		nrs = append(nrs, p)
	}
	dest, err := pdfcpu.ExtractPages(ctx, nrs, false)
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	if err := api.WriteContext(dest, &out); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}
