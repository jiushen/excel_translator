package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
)

// entrypoint chooses Excel or Word translator by file extension or -type flag.
func main() {
	mode := flag.String("type", "", "translate type: excel|word (auto by input extension if empty)")
	flag.BoolVar(&debug, "debug", false, "enable debug logging for LLM requests/responses")
	flag.Parse()

	if flag.NArg() < 2 {
		fmt.Println("usage: translate [-type excel|word] <input> <output>")
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
		err = translateExcel(input, output)
	case "word":
		err = translateWord(input, output)
	default:
		log.Fatalf("未知 type: %s", kind)
	}
	if err != nil {
		log.Fatalf("处理失败: %v", err)
	}
	log.Println("处理完成 ✅")
}
