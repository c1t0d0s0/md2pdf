// Package pdfpost inspects and finalizes browser-printed PDFs: it resolves
// heading destinations to page numbers and rewrites document metadata.
package pdfpost

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"time"
	"unicode/utf16"

	"github.com/pdfcpu/pdfcpu/pkg/api"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/model"
	"github.com/pdfcpu/pdfcpu/pkg/pdfcpu/types"

	"github.com/c1t0d0s0/md2pdf/internal/errs"
)

func read(pdf []byte) (*model.Context, error) {
	conf := model.NewDefaultConfiguration()
	conf.ValidationMode = model.ValidationRelaxed
	ctx, err := api.ReadContext(context.Background(), bytes.NewReader(pdf), conf)
	if err != nil {
		return nil, errs.Runtime("PDF structural check failed: %v", err)
	}
	if err := ctx.EnsurePageCount(); err != nil {
		return nil, errs.Runtime("PDF structural check failed: %v", err)
	}
	if ctx.PageCount < 1 {
		return nil, errs.Runtime("renderer output has no pages")
	}
	return ctx, nil
}

// DestinationPages maps each named destination (Chromium emits one per
// linked element ID) to its 1-based page number.
func DestinationPages(pdf []byte) (map[string]int, error) {
	ctx, err := read(pdf)
	if err != nil {
		return nil, err
	}
	pageOf := map[int]int{}
	for i := 1; i <= ctx.PageCount; i++ {
		_, ref, _, err := ctx.PageDict(context.Background(), i, false)
		if err != nil {
			return nil, errs.Runtime("PDF page %d: %v", i, err)
		}
		if ref != nil {
			pageOf[ref.ObjectNumber.Value()] = i
		}
	}
	root, err := ctx.Catalog()
	if err != nil {
		return nil, errs.Runtime("PDF catalog: %v", err)
	}
	out := map[string]int{}
	obj, ok := root.Find("Dests")
	if !ok {
		return out, nil
	}
	dests, err := ctx.DereferenceDict(obj)
	if err != nil || dests == nil {
		return out, nil
	}
	for name, v := range dests {
		arr, err := ctx.DereferenceArray(v)
		if err != nil || len(arr) == 0 {
			if d, err := ctx.DereferenceDict(v); err == nil && d != nil {
				arr = d.ArrayEntry("D")
			}
		}
		if len(arr) == 0 {
			continue
		}
		if ref, ok := arr[0].(types.IndirectRef); ok {
			if p, ok := pageOf[ref.ObjectNumber.Value()]; ok {
				// Chromium percent-encodes non-ASCII element IDs.
				if decoded, err := url.PathUnescape(name); err == nil {
					name = decoded
				}
				out[name] = p
			}
		}
	}
	return out, nil
}

// Metadata is written to the document information dictionary.
type Metadata struct {
	Title  string
	Author string
	Lang   string
	Time   time.Time
}

func textString(s string) types.HexLiteral {
	units := utf16.Encode([]rune(s))
	b := make([]byte, 2, 2+2*len(units))
	b[0], b[1] = 0xfe, 0xff
	for _, u := range units {
		b = append(b, byte(u>>8), byte(u))
	}
	return types.NewHexLiteral(b)
}

var (
	dateRe      = regexp.MustCompile(`/(CreationDate|ModDate) ?\(D:[0-9]{14}[^)]*\)`)
	startxrefRe = regexp.MustCompile(`startxref\s+([0-9]+)\s+%%EOF\s*$`)
	nodeIDRe    = regexp.MustCompile(`\(node([0-9]{8})\)`)
)

// normalizeNodeIDs renumbers the Blink DOM node IDs that tagged PDF uses as
// structure element IDs. Raw values depend on allocation history, so they
// are replaced by rank, preserving length, uniqueness, and sort order
// (IDTree requires sorted keys).
func normalizeNodeIDs(pdf []byte) []byte {
	var ids []string
	seen := map[string]bool{}
	for _, m := range nodeIDRe.FindAllSubmatch(pdf, -1) {
		if id := string(m[1]); !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	if len(ids) == 0 || len(ids) > 99999999 {
		return pdf
	}
	sort.Strings(ids)
	rank := make(map[string]string, len(ids))
	for i, id := range ids {
		rank[id] = fmt.Sprintf("%08d", i+1)
	}
	return nodeIDRe.ReplaceAllFunc(pdf, func(m []byte) []byte {
		return []byte("(node" + rank[string(m[5:13])] + ")")
	})
}

// Finalize validates the PDF and sets metadata without rewriting the
// browser's object graph. The browser's own timestamps are overwritten in
// place (same length), and new Info and Catalog dictionaries are appended
// as an incremental update, so identical input yields byte-identical output.
func Finalize(pdf []byte, md Metadata) ([]byte, error) {
	if !bytes.HasPrefix(pdf, []byte("%PDF-")) {
		return nil, errs.Runtime("renderer output does not start with a PDF header")
	}
	date := types.DateString(md.Time)
	pdf = normalizeNodeIDs(bytes.Clone(pdf))
	pdf = dateRe.ReplaceAllFunc(pdf, func(m []byte) []byte {
		key := m[:bytes.IndexByte(m, '(')]
		repl := append(append(bytes.Clone(key), '('), date...)
		repl = append(repl, ')')
		if len(repl) > len(m) {
			return m
		}
		// Pad within the dictionary to keep every xref offset valid.
		return append(repl, bytes.Repeat([]byte{' '}, len(m)-len(repl))...)
	})

	ctx, err := read(pdf)
	if err != nil {
		return nil, err
	}
	if ctx.Encrypt != nil {
		return nil, errs.Runtime("renderer output is encrypted")
	}
	m := startxrefRe.FindSubmatch(pdf)
	if m == nil || ctx.Root == nil || ctx.Size == nil {
		return nil, errs.Runtime("renderer output has an unexpected trailer")
	}
	prev := string(m[1])

	info := types.NewDict()
	if ctx.Info != nil {
		if d, err := ctx.DereferenceDict(*ctx.Info); err == nil && d != nil {
			info = d.Clone().(types.Dict)
		}
	}
	info.Update("Title", textString(md.Title))
	if md.Author != "" {
		info.Update("Author", textString(md.Author))
	} else {
		info.Delete("Author")
	}
	info.Update("Creator", types.StringLiteral("md2pdf"))
	info.Update("Producer", types.StringLiteral("md2pdf"))
	info.Update("CreationDate", types.StringLiteral(date))
	info.Update("ModDate", types.StringLiteral(date))

	root, err := ctx.Catalog()
	if err != nil {
		return nil, errs.Runtime("PDF catalog: %v", err)
	}
	root = root.Clone().(types.Dict)
	root.Update("Lang", types.StringLiteral(md.Lang))

	size := *ctx.Size
	rootNr := ctx.Root.ObjectNumber.Value()
	infoNr := size
	if ctx.Info != nil {
		infoNr = ctx.Info.ObjectNumber.Value()
	} else {
		size++
	}

	var out bytes.Buffer
	out.Write(pdf)
	if !bytes.HasSuffix(pdf, []byte("\n")) {
		out.WriteByte('\n')
	}
	type object struct {
		nr     int
		offset int
	}
	var objects []object
	for _, o := range []struct {
		nr int
		d  types.Dict
	}{{rootNr, root}, {infoNr, info}} {
		objects = append(objects, object{o.nr, out.Len()})
		fmt.Fprintf(&out, "%d 0 obj\n%s\nendobj\n", o.nr, o.d.PDFString())
	}
	sort.Slice(objects, func(i, j int) bool { return objects[i].nr < objects[j].nr })
	xref := out.Len()
	out.WriteString("xref\n0 1\n0000000000 65535 f\r\n")
	for _, o := range objects {
		fmt.Fprintf(&out, "%d 1\n%010d 00000 n\r\n", o.nr, o.offset)
	}
	fmt.Fprintf(&out, "trailer\n<</Size %d/Root %d 0 R/Info %d 0 R/Prev %s>>\nstartxref\n%d\n%%%%EOF\n",
		size, rootNr, infoNr, prev, xref)

	// Re-validate the final bytes.
	if _, err := read(out.Bytes()); err != nil {
		return nil, errs.Runtime("post-processed PDF failed structural re-check: %v", err)
	}
	return out.Bytes(), nil
}

// PageCount returns the number of pages in pdf.
func PageCount(pdf []byte) (int, error) {
	ctx, err := read(pdf)
	if err != nil {
		return 0, err
	}
	return ctx.PageCount, nil
}
