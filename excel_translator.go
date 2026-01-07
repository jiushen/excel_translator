package main

import (
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"strings"

	"github.com/xuri/excelize/v2"
)

// translateExcel: Excel-only translation flow (no main here).
func translateExcel(input, output string, dir Direction) error {
	f, err := excelize.OpenFile(input)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	uniqueTexts := make(map[string]struct{})

	for _, sheet := range f.GetSheetList() {
		if sheet != "" {
			uniqueTexts[sheet] = struct{}{}
		}
	}

	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			return fmt.Errorf("读取 sheet %s 失败: %w", sheet, err)
		}
		for rIdx, row := range rows {
			for cIdx := range row {
				cellRef, _ := excelize.CoordinatesToCellName(cIdx+1, rIdx+1)

				if formula, _ := f.GetCellFormula(sheet, cellRef); formula != "" {
					continue
				}

				val, err := f.GetCellValue(sheet, cellRef)
				if err != nil {
					return fmt.Errorf("读取单元格 %s!%s 失败: %w", sheet, cellRef, err)
				}
				if val == "" {
					continue
				}
				uniqueTexts[val] = struct{}{}
			}
		}
	}

	if err := collectDrawingTexts(f, uniqueTexts); err != nil {
		logf(levelWarn, "read drawing text: %v", err)
	}

	if len(uniqueTexts) == 0 {
		log.Println("没有发现需要翻译的文本。")
		return nil
	}

	originals := make([]string, 0, len(uniqueTexts))
	for s := range uniqueTexts {
		originals = append(originals, s)
	}
	log.Printf("需要翻译的唯一文本数量: %d\n", len(originals))

	translations := make(map[string]string)
	ctx := context.Background()

	for i := 0; i < len(originals); i += config.BatchSize {
		end := i + config.BatchSize
		if end > len(originals) {
			end = len(originals)
		}
		batch := originals[i:end]
		log.Printf("翻译第 %d ~ %d 条...", i+1, end)

		part, err := translateBatch(ctx, batch, dir)
		if err != nil {
			return fmt.Errorf("翻译失败: %w", err)
		}
		for k, v := range part {
			translations[k] = v
		}
	}

	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			return fmt.Errorf("读取 sheet %s 失败: %w", sheet, err)
		}
		for rIdx, row := range rows {
			for cIdx := range row {
				cellRef, _ := excelize.CoordinatesToCellName(cIdx+1, rIdx+1)

				if formula, _ := f.GetCellFormula(sheet, cellRef); formula != "" {
					continue
				}

				val, err := f.GetCellValue(sheet, cellRef)
				if err != nil || val == "" {
					continue
				}
				if zh, ok := translations[val]; ok && zh != "" {
					if err := f.SetCellStr(sheet, cellRef, zh); err != nil {
						return fmt.Errorf("写入单元格 %s!%s 失败: %w", sheet, cellRef, err)
					}
				}
			}
		}
	}

	if err := applyDrawingTranslations(f, translations); err != nil {
		logf(levelWarn, "write drawing text: %v", err)
	}
	applySheetNameTranslations(f, translations)

	if err := f.SaveAs(output); err != nil {
		return fmt.Errorf("保存文件失败: %w", err)
	}
	log.Printf("已输出到: %s", output)
	return nil
}

const (
	drawingMLNS = "http://schemas.openxmlformats.org/drawingml/2006/main"
	vmlNS       = "urn:schemas-microsoft-com:vml"
)

func collectDrawingTexts(f *excelize.File, unique map[string]struct{}) error {
	var firstErr error
	f.Pkg.Range(func(k, v interface{}) bool {
		name, ok := k.(string)
		if !ok {
			return true
		}
		data, ok := v.([]byte)
		if !ok {
			return true
		}
		switch {
		case strings.HasPrefix(name, "xl/drawings/drawing") && strings.HasSuffix(name, ".xml"):
			if err := collectDrawingMLTexts(data, unique); err != nil && firstErr == nil {
				firstErr = err
			}
		case strings.HasPrefix(name, "xl/drawings/vmlDrawing") && strings.HasSuffix(name, ".vml"):
			if err := collectVMLTexts(data, unique); err != nil && firstErr == nil {
				firstErr = err
			}
		}
		return true
	})
	return firstErr
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

func applyDrawingTranslations(f *excelize.File, translations map[string]string) error {
	var firstErr error
	f.Pkg.Range(func(k, v interface{}) bool {
		name, ok := k.(string)
		if !ok {
			return true
		}
		data, ok := v.([]byte)
		if !ok {
			return true
		}
		switch {
		case strings.HasPrefix(name, "xl/drawings/drawing") && strings.HasSuffix(name, ".xml"):
			updated, changed, err := replaceDrawingMLTexts(data, translations)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return true
			}
			if changed {
				f.Pkg.Store(name, updated)
			}
		case strings.HasPrefix(name, "xl/drawings/vmlDrawing") && strings.HasSuffix(name, ".vml"):
			updated, changed, err := replaceVMLTexts(data, translations)
			if err != nil {
				if firstErr == nil {
					firstErr = err
				}
				return true
			}
			if changed {
				f.Pkg.Store(name, updated)
			}
		}
		return true
	})
	return firstErr
}

func replaceDrawingMLTexts(data []byte, translations map[string]string) ([]byte, bool, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	inText := false
	changed := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" && t.Name.Space == drawingMLNS {
				inText = true
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, false, err
			}
		case xml.EndElement:
			if t.Name.Local == "t" && t.Name.Space == drawingMLNS {
				inText = false
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, false, err
			}
		case xml.CharData:
			if inText {
				if v, ok := translations[string(t)]; ok && v != "" {
					t = xml.CharData([]byte(v))
					changed = true
				}
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, false, err
			}
		default:
			if err := enc.EncodeToken(t); err != nil {
				return nil, false, err
			}
		}
	}
	if err := enc.Flush(); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), changed, nil
}

func replaceVMLTexts(data []byte, translations map[string]string) ([]byte, bool, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	inTextBox := false
	changed := false
	for {
		tok, err := dec.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, false, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "textbox" && (t.Name.Space == vmlNS || t.Name.Space == "") {
				inTextBox = true
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, false, err
			}
		case xml.EndElement:
			if t.Name.Local == "textbox" && (t.Name.Space == vmlNS || t.Name.Space == "") {
				inTextBox = false
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, false, err
			}
		case xml.CharData:
			if inTextBox {
				trimmed := strings.TrimSpace(string(t))
				if trimmed != "" {
					if v, ok := translations[trimmed]; ok && v != "" {
						t = xml.CharData([]byte(v))
						changed = true
					}
				}
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, false, err
			}
		default:
			if err := enc.EncodeToken(t); err != nil {
				return nil, false, err
			}
		}
	}
	if err := enc.Flush(); err != nil {
		return nil, false, err
	}
	return buf.Bytes(), changed, nil
}

func applySheetNameTranslations(f *excelize.File, translations map[string]string) {
	used := make(map[string]struct{})
	for _, name := range f.GetSheetList() {
		used[name] = struct{}{}
	}

	for _, name := range f.GetSheetList() {
		newName, ok := translations[name]
		if !ok || newName == "" || newName == name {
			continue
		}
		newName = sanitizeSheetName(newName)
		if newName == "" || newName == name {
			continue
		}
		target := makeUniqueSheetName(newName, used)
		if target == name {
			continue
		}
		if err := f.SetSheetName(name, target); err != nil {
			logf(levelWarn, "rename sheet %s -> %s failed: %v", name, target, err)
			continue
		}
		delete(used, name)
		used[target] = struct{}{}
	}
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
