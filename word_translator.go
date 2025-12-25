package main

import (
	"context"
	"fmt"
	"log"
	"strings"

	"baliance.com/gooxml/document"
)

// translateWord: DOCX-only translation flow (no main here).
func translateWord(input, output string, dir Direction) error {
	doc, err := document.Open(input)
	if err != nil {
		return fmt.Errorf("open docx: %w", err)
	}

	unique := make(map[string]struct{})

	collectParagraphs := func(paragraphs []document.Paragraph) {
		for _, p := range paragraphs {
			txt := paragraphText(p)
			if txt != "" {
				unique[txt] = struct{}{}
			}
		}
	}
	collectTables := func(tables []document.Table) {
		for _, tbl := range tables {
			for _, row := range tbl.Rows() {
				for _, cell := range row.Cells() {
					collectParagraphs(cell.Paragraphs())
				}
			}
		}
	}

	collectParagraphs(doc.Paragraphs())
	collectTables(doc.Tables())
	for _, h := range doc.Headers() {
		collectParagraphs(h.Paragraphs())
	}
	for _, f := range doc.Footers() {
		collectParagraphs(f.Paragraphs())
	}

	if len(unique) == 0 {
		log.Println("nothing to translate")
		return nil
	}

	originals := make([]string, 0, len(unique))
	for s := range unique {
		originals = append(originals, s)
	}
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

	applyParagraphs := func(paragraphs []document.Paragraph) error {
		for _, p := range paragraphs {
			orig := paragraphText(p)
			if orig == "" {
				continue
			}
			if tr, ok := translations[orig]; ok {
				if err := applyParagraphTranslation(p, tr); err != nil {
					return err
				}
			}
		}
		return nil
	}
	applyTables := func(tables []document.Table) error {
		for _, tbl := range tables {
			for _, row := range tbl.Rows() {
				for _, cell := range row.Cells() {
					if err := applyParagraphs(cell.Paragraphs()); err != nil {
						return err
					}
				}
			}
		}
		return nil
	}

	if err := applyParagraphs(doc.Paragraphs()); err != nil {
		return err
	}
	if err := applyTables(doc.Tables()); err != nil {
		return err
	}
	for _, h := range doc.Headers() {
		if err := applyParagraphs(h.Paragraphs()); err != nil {
			return err
		}
	}
	for _, f := range doc.Footers() {
		if err := applyParagraphs(f.Paragraphs()); err != nil {
			return err
		}
	}

	if err := doc.SaveToFile(output); err != nil {
		return fmt.Errorf("save: %w", err)
	}
	log.Printf("wrote: %s", output)
	return nil
}

// paragraphText concatenates all run texts in a paragraph (without formatting) preserving line breaks.
func paragraphText(p document.Paragraph) string {
	var b strings.Builder
	for i, r := range p.Runs() {
		b.WriteString(r.Text())
		if i < len(p.Runs())-1 {
			// Runs may correspond to line breaks; Text() already includes newlines if present in XML.
		}
	}
	return strings.TrimSpace(b.String())
}

// applyParagraphTranslation splits translated text across existing runs proportionally to original run lengths.
func applyParagraphTranslation(p document.Paragraph, translated string) error {
	runs := p.Runs()
	if len(runs) == 0 {
		return nil
	}
	origLens := make([]int, len(runs))
	total := 0
	for i, r := range runs {
		ln := len([]rune(r.Text()))
		origLens[i] = ln
		total += ln
	}
	if total == 0 {
		for i := range origLens {
			origLens[i] = 1
		}
		total = len(origLens)
	}
	tr := []rune(translated)
	offset := 0
	for i, r := range runs {
		share := len(tr) * origLens[i] / total
		if i == len(runs)-1 {
			share = len(tr) - offset
			if share < 0 {
				share = 0
			}
		}
		part := string(tr[offset : offset+share])
		offset += share
		r.ClearContent()
		if part != "" {
			r.AddText(part)
		}
	}
	return nil
}
