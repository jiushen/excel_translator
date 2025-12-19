package main

import (
	"context"
	"fmt"
	"log"

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
			for _, r := range p.Runs() {
				t := r.Text()
				if t != "" {
					unique[t] = struct{}{}
				}
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
			for _, r := range p.Runs() {
				t := r.Text()
				if t == "" {
					continue
				}
				if zh, ok := translations[t]; ok && zh != "" {
					r.ClearContent()
					r.AddText(zh)
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
