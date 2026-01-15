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

const (
	drawingMLNS = "http://schemas.openxmlformats.org/drawingml/2006/main"
	vmlNS       = "urn:schemas-microsoft-com:vml"
)

type excelPart struct {
	name   string
	data   []byte
	method uint16
}

// translateExcel: Excel-only translation flow (no main here).
// This implementation rewrites only text-bearing XML parts and copies all
// other ZIP entries byte-for-byte to avoid re-generating the workbook.
func translateExcel(input, output string, dir Direction) error {
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

	parts := make([]excelPart, 0, len(zr.File))
	unique := make(map[string]struct{})
	var sheetNames []string

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

		switch {
		case f.Name == "xl/workbook.xml":
			if err := extractWorkbookSheetNames(buf, unique, &sheetNames); err != nil {
				return fmt.Errorf("parse workbook: %w", err)
			}
		case f.Name == "xl/sharedStrings.xml":
			if err := extractSharedStrings(buf, unique); err != nil {
				return fmt.Errorf("parse sharedStrings: %w", err)
			}
		case isWorksheetXML(f.Name):
			if err := extractInlineStrings(buf, unique); err != nil {
				return fmt.Errorf("parse worksheet %s: %w", f.Name, err)
			}
		case isDrawingXML(f.Name):
			if err := collectDrawingMLTexts(buf, unique); err != nil {
				return fmt.Errorf("parse drawing %s: %w", f.Name, err)
			}
		case isVMLDrawing(f.Name):
			if err := collectVMLTexts(buf, unique); err != nil {
				return fmt.Errorf("parse vml %s: %w", f.Name, err)
			}
		}

		parts = append(parts, excelPart{
			name:   f.Name,
			data:   buf,
			method: f.Method,
		})
	}

	if len(unique) == 0 {
		log.Println("nothing to translate")
		return copyExcelZip(parts, output)
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

	sheetNameMap := buildSheetNameMap(sheetNames, translations)

	for i, p := range parts {
		switch {
		case p.name == "xl/workbook.xml":
			newXML, err := rewriteWorkbookSheetNames(p.data, sheetNameMap)
			if err != nil {
				return fmt.Errorf("rewrite workbook: %w", err)
			}
			parts[i].data = newXML
		case p.name == "xl/sharedStrings.xml":
			newXML, err := rewriteSharedStrings(p.data, translations)
			if err != nil {
				return fmt.Errorf("rewrite sharedStrings: %w", err)
			}
			parts[i].data = newXML
		case isWorksheetXML(p.name):
			newXML, err := rewriteInlineStrings(p.data, translations)
			if err != nil {
				return fmt.Errorf("rewrite worksheet %s: %w", p.name, err)
			}
			parts[i].data = newXML
		case isDrawingXML(p.name):
			newXML, _, err := replaceDrawingMLTexts(p.data, translations)
			if err != nil {
				return fmt.Errorf("rewrite drawing %s: %w", p.name, err)
			}
			parts[i].data = newXML
		case isVMLDrawing(p.name):
			newXML, _, err := replaceVMLTexts(p.data, translations)
			if err != nil {
				return fmt.Errorf("rewrite vml %s: %w", p.name, err)
			}
			parts[i].data = newXML
		}
	}

	if err := copyExcelZip(parts, output); err != nil {
		return err
	}
	log.Printf("wrote: %s", output)
	return nil
}

func isWorksheetXML(name string) bool {
	return strings.HasPrefix(name, "xl/worksheets/sheet") && strings.HasSuffix(name, ".xml")
}

func isDrawingXML(name string) bool {
	return strings.HasPrefix(name, "xl/drawings/drawing") && strings.HasSuffix(name, ".xml")
}

func isVMLDrawing(name string) bool {
	return strings.HasPrefix(name, "xl/drawings/vmlDrawing") && strings.HasSuffix(name, ".vml")
}

func extractWorkbookSheetNames(data []byte, unique map[string]struct{}, names *[]string) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		start, ok := tok.(xml.StartElement)
		if !ok || start.Name.Local != "sheet" {
			continue
		}
		for _, attr := range start.Attr {
			if attr.Name.Local == "name" && attr.Value != "" {
				unique[attr.Value] = struct{}{}
				*names = append(*names, attr.Value)
				break
			}
		}
	}
}

func rewriteWorkbookSheetNames(data []byte, mapping map[string]string) ([]byte, error) {
	s := string(data)
	sheetTagRe := regexp.MustCompile(`(?s)<sheet\b[^>]*>`)
	nameAttrRe := regexp.MustCompile(`\bname="([^"]*)"`)
	out := sheetTagRe.ReplaceAllStringFunc(s, func(tag string) string {
		loc := nameAttrRe.FindStringSubmatchIndex(tag)
		if loc == nil {
			return tag
		}
		name := tag[loc[2]:loc[3]]
		if v, ok := mapping[name]; ok && v != "" {
			return tag[:loc[2]] + v + tag[loc[3]:]
		}
		return tag
	})
	return []byte(out), nil
}

func extractSharedStrings(data []byte, unique map[string]struct{}) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	inSI := false
	inT := false
	var text strings.Builder
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "si":
				inSI = true
				inT = false
				text.Reset()
			case "t":
				if inSI {
					inT = true
				}
			}
		case xml.EndElement:
			switch t.Name.Local {
			case "t":
				if inSI {
					inT = false
				}
			case "si":
				if inSI {
					s := text.String()
					if s != "" {
						unique[s] = struct{}{}
					}
				}
				inSI = false
			}
		case xml.CharData:
			if inSI && inT {
				text.Write([]byte(t))
			}
		}
	}
}

func rewriteSharedStrings(data []byte, translations map[string]string) ([]byte, error) {
	siRe := regexp.MustCompile(`(?s)<si\b[^>]*>.*?</si>`)
	tRe := regexp.MustCompile(`(?s)<t\b[^>]*>(.*?)</t>`)
	s := string(data)
	out := siRe.ReplaceAllStringFunc(s, func(block string) string {
		runs, ranges := extractTagRuns(block, tRe)
		if len(runs) == 0 {
			return block
		}
		orig := strings.Join(runs, "")
		v, ok := translations[orig]
		if !ok || v == "" {
			return block
		}
		runOutputs := splitTranslatedRunsExcel(runs, v)
		return replaceRanges(block, ranges, runOutputs)
	})
	return []byte(out), nil
}

func extractInlineStrings(data []byte, unique map[string]struct{}) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	inCell := false
	cellInline := false
	cellHasFormula := false
	inIS := false
	inT := false
	var text strings.Builder

	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "c" {
				inCell = true
				cellInline = false
				cellHasFormula = false
				inIS = false
				inT = false
				text.Reset()
				for _, attr := range t.Attr {
					if attr.Name.Local == "t" && attr.Value == "inlineStr" {
						cellInline = true
						break
					}
				}
				continue
			}
			if !inCell {
				continue
			}
			switch t.Name.Local {
			case "f":
				cellHasFormula = true
			case "is":
				inIS = true
			case "t":
				if inIS {
					inT = true
				}
			}
		case xml.EndElement:
			if !inCell {
				continue
			}
			switch t.Name.Local {
			case "t":
				inT = false
			case "is":
				inIS = false
			case "c":
				if cellInline && !cellHasFormula {
					s := text.String()
					if s != "" {
						unique[s] = struct{}{}
					}
				}
				inCell = false
			}
		case xml.CharData:
			if inCell && inIS && inT {
				text.Write([]byte(t))
			}
		}
	}
}

func rewriteInlineStrings(data []byte, translations map[string]string) ([]byte, error) {
	cellRe := regexp.MustCompile(`(?s)<c\b[^>]*\bt="inlineStr"[^>]*>.*?</c>`)
	tRe := regexp.MustCompile(`(?s)<t\b[^>]*>(.*?)</t>`)
	s := string(data)
	out := cellRe.ReplaceAllStringFunc(s, func(cell string) string {
		if strings.Contains(cell, "<f") {
			return cell
		}
		runs, ranges := extractTagRuns(cell, tRe)
		if len(runs) == 0 {
			return cell
		}
		orig := strings.Join(runs, "")
		v, ok := translations[orig]
		if !ok || v == "" {
			return cell
		}
		runOutputs := splitTranslatedRunsExcel(runs, v)
		return replaceRanges(cell, ranges, runOutputs)
	})
	return []byte(out), nil
}

func collectDrawingMLTexts(data []byte, unique map[string]struct{}) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	inText := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" && t.Name.Space == drawingMLNS {
				inText = true
			}
		case xml.EndElement:
			if t.Name.Local == "t" && t.Name.Space == drawingMLNS {
				inText = false
			}
		case xml.CharData:
			if inText {
				s := string(t)
				if s != "" {
					unique[s] = struct{}{}
				}
			}
		}
	}
}

func collectVMLTexts(data []byte, unique map[string]struct{}) error {
	dec := xml.NewDecoder(bytes.NewReader(data))
	inTextBox := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "textbox" && (t.Name.Space == vmlNS || t.Name.Space == "") {
				inTextBox = true
			}
		case xml.EndElement:
			if t.Name.Local == "textbox" && (t.Name.Space == vmlNS || t.Name.Space == "") {
				inTextBox = false
			}
		case xml.CharData:
			if inTextBox {
				s := strings.TrimSpace(string(t))
				if s != "" {
					unique[s] = struct{}{}
				}
			}
		}
	}
}

func replaceDrawingMLTexts(data []byte, translations map[string]string) ([]byte, bool, error) {
	tRe := regexp.MustCompile(`(?s)<a:t\b[^>]*>(.*?)</a:t>`)
	s := string(data)
	matches := tRe.FindAllStringSubmatchIndex(s, -1)
	if len(matches) == 0 {
		return data, false, nil
	}
	var buf strings.Builder
	last := 0
	changed := false
	for _, m := range matches {
		buf.WriteString(s[last:m[2]])
		origRaw := s[m[2]:m[3]]
		orig := html.UnescapeString(origRaw)
		if v, ok := translations[orig]; ok && v != "" {
			buf.WriteString(escapeXMLText(v))
			changed = true
		} else {
			buf.WriteString(origRaw)
		}
		last = m[3]
	}
	buf.WriteString(s[last:])
	return []byte(buf.String()), changed, nil
}

func replaceVMLTexts(data []byte, translations map[string]string) ([]byte, bool, error) {
	boxRe := regexp.MustCompile(`(?s)<v:textbox\b[^>]*>.*?</v:textbox>`)
	s := string(data)
	changed := false
	out := boxRe.ReplaceAllStringFunc(s, func(box string) string {
		start := strings.Index(box, ">")
		end := strings.LastIndex(box, "</v:textbox>")
		if start == -1 || end == -1 || start+1 > end {
			return box
		}
		inner := box[start+1 : end]
		runs, ranges := extractTextRuns(inner)
		if len(runs) == 0 {
			return box
		}
		orig := strings.Join(runs, "")
		v, ok := translations[orig]
		if !ok || v == "" {
			return box
		}
		runOutputs := splitTranslatedRunsExcel(runs, v)
		newInner := replaceRanges(inner, ranges, runOutputs)
		if newInner != inner {
			changed = true
		}
		return box[:start+1] + newInner + box[end:]
	})
	return []byte(out), changed, nil
}

func buildSheetNameMap(names []string, translations map[string]string) map[string]string {
	mapping := make(map[string]string, len(names))
	used := make(map[string]struct{})
	for _, name := range names {
		target := name
		if v, ok := translations[name]; ok && v != "" {
			v = sanitizeSheetName(v)
			if v != "" {
				target = v
			}
		}
		target = makeUniqueSheetName(target, used)
		mapping[name] = target
		used[target] = struct{}{}
	}
	return mapping
}

func sanitizeSheetName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	for _, ch := range []string{":", "\\", "/", "?", "*", "[", "]"} {
		name = strings.ReplaceAll(name, ch, "")
	}
	return truncateRunes(name, 31)
}

func makeUniqueSheetName(name string, used map[string]struct{}) string {
	if _, exists := used[name]; !exists {
		return name
	}
	for i := 1; i < 1000; i++ {
		suffix := fmt.Sprintf("_%d", i)
		base := truncateRunes(name, 31-len([]rune(suffix)))
		candidate := base + suffix
		if _, exists := used[candidate]; !exists {
			return candidate
		}
	}
	return truncateRunes(name, 31)
}

func truncateRunes(s string, max int) string {
	if max <= 0 {
		return ""
	}
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max])
}

func splitTranslatedRunsExcel(origRuns []string, translated string) [][]byte {
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

func escapeXMLText(s string) string {
	var buf bytes.Buffer
	_ = xml.EscapeText(&buf, []byte(s))
	return buf.String()
}

func extractTagRuns(block string, tRe *regexp.Regexp) ([]string, [][]int) {
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

func extractTextRuns(block string) ([]string, [][]int) {
	var runs []string
	var ranges [][]int
	inTag := false
	start := 0
	for i := 0; i < len(block); i++ {
		switch block[i] {
		case '<':
			if !inTag && start < i {
				raw := block[start:i]
				runs = append(runs, html.UnescapeString(raw))
				ranges = append(ranges, []int{start, i})
			}
			inTag = true
		case '>':
			if inTag {
				inTag = false
				start = i + 1
			}
		}
	}
	if !inTag && start < len(block) {
		raw := block[start:]
		runs = append(runs, html.UnescapeString(raw))
		ranges = append(ranges, []int{start, len(block)})
	}
	return runs, ranges
}

func replaceRanges(block string, ranges [][]int, runs [][]byte) string {
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

func copyExcelZip(parts []excelPart, output string) error {
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
