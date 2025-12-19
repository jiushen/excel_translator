package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"
)

// entrypoint chooses Excel or Word translator by file extension or -type flag.
func main() {
	mode := flag.String("type", "", "translate type: excel|word (auto by input extension if empty)")
	cfgPath := flag.String("config", "config.yaml", "config file path (YAML); defaults are used if missing")
	dirFlag := flag.String("direction", "zh2ja", "direction: zh2ja (中文->日文) | ja2zh (日文->中文)")
	flag.BoolVar(&debug, "debug", false, "enable debug logging for LLM requests/responses")
	flag.Parse()

	if err := loadConfig(*cfgPath); err != nil {
		log.Fatalf("加载配置失败: %v", err)
	}

	dir := parseDirection(*dirFlag)

	if flag.NArg() < 2 {
		fmt.Println("usage: translate [-type excel|word] [-direction zh2ja|ja2zh] [-config config.yaml] <input> <output>")
		return
	}
	input := flag.Arg(0)
	output := flag.Arg(1)

	kind := *mode
	if kind == "" {
		switch ext := filepath.Ext(input); ext {
		case ".xlsx", ".xlsm":
			kind = "excel"
		case ".docx":
			kind = "word"
		default:
			log.Fatalf("无法识别的输入类型（扩展名 %s），请显式指定 -type excel|word", ext)
		}
	}

	var err error
	switch kind {
	case "excel":
		err = translateExcel(input, output, dir)
	case "word":
		err = translateWord(input, output, dir)
	default:
		log.Fatalf("未知 type: %s", kind)
	}
	if err != nil {
		log.Fatalf("处理失败: %v", err)
	}
	log.Println("处理完成 ✅")
}

func parseDirection(s string) Direction {
	switch strings.ToLower(s) {
	case "ja2zh":
		return DirectionJA2ZH
	default:
		return DirectionZH2JA
	}
}
