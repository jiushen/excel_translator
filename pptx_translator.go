package main

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/xml"
	"fmt"
	"io"
	"log"
	"os"
	"path"
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

// extractTexts collects text content inside <a:t> nodes into the set.
func extractTexts(xmlData []byte, dst map[string]struct{}) {
	dec := xml.NewDecoder(bytes.NewReader(xmlData))
	inText := false
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
			if t.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			}
		case xml.CharData:
			if inText {
				val := string([]byte(t))
				if strings.TrimSpace(val) != "" {
					dst[val] = struct{}{}
				}
			}
		}
	}
}

// rewriteTexts replaces <a:t> char data using translations.
func rewriteTexts(xmlData []byte, translations map[string]string) ([]byte, error) {
	dec := xml.NewDecoder(bytes.NewReader(xmlData))
	var buf bytes.Buffer
	enc := xml.NewEncoder(&buf)
	inText := false

	for {
		tok, err := dec.Token()
		if err != nil {
			if err == io.EOF {
				break
			}
			return nil, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			if t.Name.Local == "t" {
				inText = true
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, err
			}
		case xml.EndElement:
			if t.Name.Local == "t" {
				inText = false
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, err
			}
		case xml.CharData:
			if inText {
				orig := string([]byte(t))
				if newVal, ok := translations[orig]; ok && newVal != "" {
					t = xml.CharData([]byte(newVal))
				}
			}
			if err := enc.EncodeToken(t); err != nil {
				return nil, err
			}
		default:
			if err := enc.EncodeToken(t); err != nil {
				return nil, err
			}
		}
	}
	if err := enc.Flush(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
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
