package main

import (
	"context"
	"fmt"
	"log"

	"github.com/xuri/excelize/v2"
)

// translateExcel: Excel-only translation flow (no main here).
func translateExcel(input, output string) error {
	f, err := excelize.OpenFile(input)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	uniqueTexts := make(map[string]struct{})

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

		part, err := translateBatch(ctx, batch)
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

	if err := f.SaveAs(output); err != nil {
		return fmt.Errorf("保存文件失败: %w", err)
	}
	log.Printf("已输出到: %s", output)
	return nil
}
