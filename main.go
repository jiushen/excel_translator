package main

import (
	"flag"
	"fmt"
	"log"
	"path/filepath"
	"strings"
)

// entrypoint chooses translator by extension or -type flag.
func main() {
	mode := flag.String("type", "", "translate type: excel|word|pptx (auto by input extension if empty)")
	cfgPath := flag.String("config", "config.yaml", "config file path (YAML; required)")
	dirFlag := flag.String("direction", "zh2ja", "direction: zh2ja (zh->ja) | ja2zh (ja->zh)")
	providerFlag := flag.String("provider", "deepseek", "LLM provider: deepseek|openai (overrides config.yaml)")
	flag.BoolVar(&debug, "debug", false, "enable debug logging for LLM requests/responses")
	flag.Parse()

	if err := loadConfig(*cfgPath); err != nil {
		log.Fatalf("load config failed: %v", err)
	}
	if providerFlag != nil && *providerFlag != "" {
		config.Provider = Provider(*providerFlag)
	}
	if config.Provider == "" {
		config.Provider = ProviderDeepSeek
	}
	if err := ensureProviderConfig(); err != nil {
		log.Fatalf("config invalid: %v", err)
	}

	dir := parseDirection(*dirFlag)

	if flag.NArg() < 2 {
		fmt.Println("usage: translate [-type excel|word|pptx] [-direction zh2ja|ja2zh] [-provider deepseek|openai] [-config config.yaml] <input> <output>")
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
		case ".pptx":
			kind = "pptx"
		default:
			log.Fatalf("unknown input extension (%s), please specify -type excel|word|pptx", ext)
		}
	}

	var err error
	switch kind {
	case "excel":
		err = translateExcel(input, output, dir)
	case "word":
		err = translateWord(input, output, dir)
	case "pptx":
		err = translatePPTX(input, output, dir)
	default:
		log.Fatalf("unknown type: %s", kind)
	}
	if err != nil {
		log.Fatalf("process failed: %v", err)
	}
	log.Println("done")
}

func parseDirection(s string) Direction {
	switch strings.ToLower(s) {
	case "ja2zh":
		return DirectionJA2ZH
	default:
		return DirectionZH2JA
	}
}
