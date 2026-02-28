package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"html"
	"io"
	"log"
	"os"
	"path"
	"regexp"
	"sort"
	"strings"
)

type pptPart struct {
	name   string
	data   []byte
	method uint16
}

// translatePPTX rewrites PPTX by only changing <a:t> text nodes in slides/notes.
// All other parts/relationships are copied byte-for-byte to preserve images/OLE/custom parts.
func translatePPTX(input, output string, dir Direction) error {
	inFile, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("open input: %w", err)
	}
	defer inFile.Close()

	stat, err := inFile.Stat()
	if err != nil {
		return fmt.Errorf("stat input: %w", err)
	}

	zr, err := zip.NewReader(inFile, stat.Size())
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	targetFiles := func(name string) bool {
		// slides and notesSlides
		return strings.HasPrefix(name, "ppt/slides/slide") && strings.HasSuffix(name, ".xml") ||
			strings.HasPrefix(name, "ppt/notesSlides/notesSlide") && strings.HasSuffix(name, ".xml")
	}

	parts := make([]pptPart, 0, len(zr.File))
	unique := make(map[string]struct{})

	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			return fmt.Errorf("read %s: %w", f.Name, err)
		}
		buf, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			return fmt.Errorf("read %s: %w", f.Name, err)
		}

		if targetFiles(f.Name) {
			extractTexts(buf, unique)
		}

		parts = append(parts, pptPart{
			name:   f.Name,
			data:   buf,
			method: f.Method,
		})
	}

	if len(unique) == 0 {
		log.Println("nothing to translate")
		return copyZip(parts, output)
	}

	originals := make([]string, 0, len(unique))
	for s := range unique {
		originals = append(originals, s)
	}
	sort.Strings(originals)
	log.Printf("unique texts: %d\n", len(originals))

	translations := make(map[string]string)
	ctx := context.Background()
	for i := 0; i < len(originals); i += config.BatchSize {
		end := i + config.BatchSize
		if end > len(originals) {
			end = len(originals)
		}
		batch := originals[i:end]
		log.Printf("translating %d ~ %d ...", i+1, end)
		part, err := translateBatch(ctx, batch, dir)
		if err != nil {
			return fmt.Errorf("translate: %w", err)
		}
		for k, v := range part {
			translations[k] = v
		}
	}

	// rewrite target xml parts
	for i, p := range parts {
		if !targetFiles(p.name) {
			continue
		}
		newXML, err := rewriteTexts(p.data, translations)
		if err != nil {
			return fmt.Errorf("rewrite %s: %w", p.name, err)
		}
		parts[i].data = newXML
	}

	if err := copyZip(parts, output); err != nil {
		return err
	}
	log.Printf("wrote: %s", output)
	return nil
}

// extractTexts collects paragraph-level text (concatenated runs in <a:p>) into the set.
func extractTexts(xmlData []byte, dst map[string]struct{}) {
	dec := xml.NewDecoder(bytes.NewReader(xmlData))
	inP := false
	inText := false
	var buf strings.Builder
	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				return
			}
			return
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "p":
				inP = true
				buf.Reset()
			case "t":
				inText = true
			case "br":
				if inP {
					buf.WriteString("\n")
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				inText = false
			case "p":
				inP = false
				txt := strings.TrimSpace(buf.String())
				if txt != "" {
					dst[txt] = struct{}{}
				}
			}
		case xml.CharData:
			if inP && inText {
				buf.Write([]byte(t))
			}
		}
	}
}

// rewriteTexts replaces only the text inside existing <a:t> nodes.
// It preserves the original XML bytes outside those text ranges.
func rewriteTexts(xmlData []byte, translations map[string]string) ([]byte, error) {
	pRe := regexp.MustCompile(`(?s)<a:p\b[^>]*>.*?</a:p>`)
	tRe := regexp.MustCompile(`(?s)<a:t\b[^>]*>(.*?)</a:t>`)
	brRe := regexp.MustCompile(`(?s)<a:br\b[^>]*/>|<a:br\b[^>]*>\s*</a:br>`)
	s := string(xmlData)
	out := pRe.ReplaceAllStringFunc(s, func(block string) string {
		runs, ranges := extractPPTTagRuns(block, tRe)
		if len(runs) == 0 {
			return block
		}
		key := pptParagraphKey(block, tRe, brRe)
		v, ok := translations[key]
		if !ok || v == "" {
			return block
		}
		runOutputs := splitTranslatedRuns(runs, v)
		return replacePPTRanges(block, ranges, runOutputs)
	})
	return []byte(out), nil
}

func pptParagraphKey(block string, tRe, brRe *regexp.Regexp) string {
	normalized := brRe.ReplaceAllString(block, "\n")
	runs, _ := extractPPTTagRuns(normalized, tRe)
	return strings.TrimSpace(strings.Join(runs, ""))
}

func extractPPTTagRuns(block string, tRe *regexp.Regexp) ([]string, [][]int) {
	matches := tRe.FindAllStringSubmatchIndex(block, -1)
	runs := make([]string, 0, len(matches))
	ranges := make([][]int, 0, len(matches))
	for _, m := range matches {
		if len(m) < 4 {
			continue
		}
		raw := block[m[2]:m[3]]
		runs = append(runs, html.UnescapeString(raw))
		ranges = append(ranges, []int{m[2], m[3]})
	}
	return runs, ranges
}

func replacePPTRanges(block string, ranges [][]int, runs [][]byte) string {
	var buf strings.Builder
	last := 0
	for i, r := range ranges {
		if len(r) < 2 {
			continue
		}
		buf.WriteString(block[last:r[0]])
		if i < len(runs) {
			buf.WriteString(escapeXMLText(string(runs[i])))
		}
		last = r[1]
	}
	buf.WriteString(block[last:])
	return buf.String()
}

func splitTranslatedRuns(origRuns []string, translated string) [][]byte {
	if len(origRuns) == 0 {
		return nil
	}
	tr := []rune(translated)
	total := 0
	runLens := make([]int, len(origRuns))
	for i, s := range origRuns {
		runLens[i] = len([]rune(s))
		total += runLens[i]
	}
	if total == 0 {
		for i := range runLens {
			runLens[i] = 1
		}
		total = len(origRuns)
	}

	res := make([][]byte, len(origRuns))
	offset := 0
	for i, ln := range runLens {
		share := len(tr) * ln / total
		if i == len(runLens)-1 {
			share = len(tr) - offset
			if share < 0 {
				share = 0
			}
		}
		res[i] = []byte(string(tr[offset : offset+share]))
		offset += share
	}
	return res
}

// copyZip writes all parts to a new zip file preserving names and raw data.
func copyZip(parts []pptPart, output string) error {
	outFile, err := os.Create(output)
	if err != nil {
		return fmt.Errorf("create output: %w", err)
	}
	defer outFile.Close()

	zw := zip.NewWriter(outFile)
	for _, p := range parts {
		h := &zip.FileHeader{
			Name:   p.name,
			Method: p.method,
		}
		// Preserve directory entries as stored entries.
		if strings.HasSuffix(p.name, "/") || path.Base(p.name) == "" {
			h.Method = zip.Store
		}
		w, err := zw.CreateHeader(h)
		if err != nil {
			zw.Close()
			return fmt.Errorf("write %s header: %w", p.name, err)
		}
		if _, err := w.Write(p.data); err != nil {
			zw.Close()
			return fmt.Errorf("write %s data: %w", p.name, err)
		}
	}
	if err := zw.Close(); err != nil {
		return fmt.Errorf("finalize zip: %w", err)
	}
	return nil
}
