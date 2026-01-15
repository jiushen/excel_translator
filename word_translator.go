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

type wordPart struct {
	name   string
	data   []byte
	method uint16
}

// translateWord: DOCX-only translation flow (no main here).
func translateWord(input, output string, dir Direction) error {
	inFile, err := os.Open(input)
	if err != nil {
		return fmt.Errorf("open docx: %w", err)
	}
	defer inFile.Close()

	stat, err := inFile.Stat()
	if err != nil {
		return fmt.Errorf("stat docx: %w", err)
	}

	zr, err := zip.NewReader(inFile, stat.Size())
	if err != nil {
		return fmt.Errorf("open zip: %w", err)
	}

	parts := make([]wordPart, 0, len(zr.File))
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

		if isWordTargetXML(f.Name) {
			extractWordParagraphTexts(buf, unique)
		}

		parts = append(parts, wordPart{
			name:   f.Name,
			data:   buf,
			method: f.Method,
		})
	}

	if len(unique) == 0 {
		log.Println("nothing to translate")
		return copyWordZip(parts, output)
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

	for i, p := range parts {
		if !isWordTargetXML(p.name) {
			continue
		}
		newXML := rewriteWordParagraphTexts(p.data, translations)
		parts[i].data = newXML
	}

	if err := copyWordZip(parts, output); err != nil {
		return err
	}
	log.Printf("wrote: %s", output)
	return nil
}

func isWordTargetXML(name string) bool {
	if name == "word/document.xml" {
		return true
	}
	if strings.HasPrefix(name, "word/header") && strings.HasSuffix(name, ".xml") {
		return true
	}
	if strings.HasPrefix(name, "word/footer") && strings.HasSuffix(name, ".xml") {
		return true
	}
	return false
}

func extractWordParagraphTexts(data []byte, unique map[string]struct{}) {
	pRe := regexp.MustCompile(`(?s)<w:p\b[^>]*>.*?</w:p>`)
	tRe := regexp.MustCompile(`(?s)<w:t\b[^>]*>(.*?)</w:t>`)
	s := string(data)
	pRe.ReplaceAllStringFunc(s, func(block string) string {
		runs, _ := extractWordTagRuns(block, tRe)
		if len(runs) == 0 {
			return block
		}
		txt := strings.TrimSpace(strings.Join(runs, ""))
		if txt != "" {
			unique[txt] = struct{}{}
		}
		return block
	})
}

func rewriteWordParagraphTexts(data []byte, translations map[string]string) []byte {
	pRe := regexp.MustCompile(`(?s)<w:p\b[^>]*>.*?</w:p>`)
	tRe := regexp.MustCompile(`(?s)<w:t\b[^>]*>(.*?)</w:t>`)
	s := string(data)
	out := pRe.ReplaceAllStringFunc(s, func(block string) string {
		runs, ranges := extractWordTagRuns(block, tRe)
		if len(runs) == 0 {
			return block
		}
		orig := strings.TrimSpace(strings.Join(runs, ""))
		v, ok := translations[orig]
		if !ok || v == "" {
			return block
		}
		runOutputs := splitTranslatedRunsWord(runs, v)
		return replaceWordRanges(block, ranges, runOutputs)
	})
	return []byte(out)
}

func extractWordTagRuns(block string, tRe *regexp.Regexp) ([]string, [][]int) {
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

func replaceWordRanges(block string, ranges [][]int, runs [][]byte) string {
	var buf strings.Builder
	last := 0
	for i, r := range ranges {
		if len(r) < 2 {
			continue
		}
		buf.WriteString(block[last:r[0]])
		if i < len(runs) {
			buf.WriteString(escapeWordXMLText(string(runs[i])))
		}
		last = r[1]
	}
	buf.WriteString(block[last:])
	return buf.String()
}

func escapeWordXMLText(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func splitTranslatedRunsWord(origRuns []string, translated string) [][]byte {
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

func copyWordZip(parts []wordPart, output string) error {
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
