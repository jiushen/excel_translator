package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// debug controls whether to dump request/response for troubleshooting.
var debug bool

type Provider string
type Direction string

const (
	ProviderOpenAI   Provider = "openai"
	ProviderDeepSeek Provider = "deepseek"

	DirectionZH2JA Direction = "zh2ja"
	DirectionJA2ZH Direction = "ja2zh"
)

type Config struct {
	Provider Provider `yaml:"provider"`

	OpenAIKey   string `yaml:"openai_key"`
	OpenAIModel string `yaml:"openai_model"`
	OpenAIURL   string `yaml:"openai_url"`

	DeepseekKey   string `yaml:"deepseek_key"`
	DeepseekModel string `yaml:"deepseek_model"`
	DeepseekURL   string `yaml:"deepseek_url"`

	BatchSize int `yaml:"batch_size"`

	TimeoutSeconds int `yaml:"timeout_seconds"`

	PromptZH2JA string `yaml:"prompt_zh2ja"`
	PromptJA2ZH string `yaml:"prompt_ja2zh"`

	LogDir   string `yaml:"log_dir"`
	LogFile  string `yaml:"log_file"`
	LogLevel string `yaml:"log_level"` // debug|info|warn|error
}

// populated from config file (and CLI flags)
var config Config
var logLevel level

func loadConfig(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return err
	}
	config = cfg
	// Secrets come from environment first; config file values are only a fallback.
	if v := os.Getenv("OPENAI_API_KEY"); v != "" {
		config.OpenAIKey = v
	}
	if v := os.Getenv("DEEPSEEK_API_KEY"); v != "" {
		config.DeepseekKey = v
	}
	if config.BatchSize <= 0 {
		config.BatchSize = 50
	}
	if config.TimeoutSeconds <= 0 {
		config.TimeoutSeconds = 600
	}
	logLevel = parseLevel(config.LogLevel)
	return nil
}

func ensureProviderConfig() error {
	switch config.Provider {
	case ProviderOpenAI:
		if config.OpenAIKey == "" || config.OpenAIModel == "" || config.OpenAIURL == "" {
			return fmt.Errorf("openai provider requires openai_key/openai_model/openai_url in config.yaml")
		}
	case ProviderDeepSeek:
		if config.DeepseekKey == "" || config.DeepseekModel == "" || config.DeepseekURL == "" {
			return fmt.Errorf("deepseek provider requires deepseek_key/deepseek_model/deepseek_url in config.yaml")
		}
	default:
		return fmt.Errorf("unknown provider: %s", config.Provider)
	}
	if config.PromptZH2JA == "" || config.PromptJA2ZH == "" {
		return fmt.Errorf("prompt_zh2ja/prompt_ja2zh must be set in config.yaml")
	}
	return nil
}

type level int

const (
	levelDebug level = iota
	levelInfo
	levelWarn
	levelError
)

func parseLevel(s string) level {
	switch strings.ToLower(s) {
	case "debug":
		return levelDebug
	case "info":
		return levelInfo
	case "warn", "warning":
		return levelWarn
	case "error":
		return levelError
	default:
		return levelInfo
	}
}

func logf(lvl level, format string, args ...any) {
	if lvl < logLevel {
		return
	}
	prefix := ""
	switch lvl {
	case levelDebug:
		prefix = "[DEBUG] "
	case levelWarn:
		prefix = "[WARN] "
	case levelError:
		prefix = "[ERROR] "
	default:
		prefix = "[INFO] "
	}
	log.Printf(prefix+format, args...)
}

func translateBatch(ctx context.Context, src []string, dir Direction) (map[string]string, error) {
	switch config.Provider {
	case ProviderOpenAI:
		return callChatAPI(ctx, src, dir, config.OpenAIURL, config.OpenAIKey, config.OpenAIModel)
	case ProviderDeepSeek:
		return callChatAPI(ctx, src, dir, config.DeepseekURL, config.DeepseekKey, config.DeepseekModel)
	default:
		return nil, fmt.Errorf("unknown provider: %s", config.Provider)
	}
}

func selectSystemPrompt(dir Direction) string {
	switch dir {
	case DirectionJA2ZH:
		return config.PromptJA2ZH
	default:
		return config.PromptZH2JA
	}
}

func selectUserPrompt(dir Direction, jsonPayload string) string {
	if dir == DirectionJA2ZH {
		return fmt.Sprintf(`请将下列 JSON 中 texts 数组里的所有字符串按上述规则翻译为中文，并返回 {"原文": "译文"} 的 JSON 对象（键为原文，值为译文）。输入：%s`, jsonPayload)
	}
	return fmt.Sprintf(`请将下列 JSON 中 texts 数组里的所有字符串按上述规则翻译为日文，并返回 {"原文": "译文"} 的 JSON 对象（键为原文，值为译文）。输入：%s`, jsonPayload)
}

func callChatAPI(ctx context.Context, texts []string, dir Direction, baseURL, apiKey, model string) (map[string]string, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key missing")
	}

	payload := map[string]any{"texts": texts}
	jsonBytes, _ := json.Marshal(payload)

	system := selectSystemPrompt(dir)
	user := selectUserPrompt(dir, string(jsonBytes))

	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		ResponseFormat: &responseShape{Type: "json_object"},
	}

	bodyBytes, _ := json.Marshal(reqBody)

	logf(levelDebug, "LLM request url=%s model=%s dir=%s batch=%d", baseURL, model, dir, len(texts))
	logf(levelDebug, "LLM request body=%s", string(bodyBytes))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	timeout := time.Duration(config.TimeoutSeconds) * time.Second
	client := &http.Client{Timeout: timeout}
	logf(levelDebug, "HTTP timeout set to %v for provider %s (batch size: %d)", timeout, config.Provider, len(texts))
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	logf(levelDebug, "LLM response status=%s", resp.Status)
	logf(levelDebug, "LLM response body=%s", string(respBytes))

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error: %s\n%s", resp.Status, string(respBytes))
	}

	var cr chatResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return nil, fmt.Errorf("decode: %w\nraw=%s", err, string(respBytes))
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	content := cr.Choices[0].Message.Content
	out := make(map[string]string)
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		// LLM sometimes produces unescaped quotes inside JSON string values.
		// Attempt to fix by re-extracting from the raw JSON response where the
		// outer envelope already has proper escaping.
		fixed := tryFixLLMJSON(content)
		if fixed != "" {
			if err2 := json.Unmarshal([]byte(fixed), &out); err2 == nil {
				logf(levelWarn, "LLM returned malformed JSON, auto-fixed successfully")
				return out, nil
			}
		}
		return nil, fmt.Errorf("unmarshal model JSON: %w\nraw: %s", err, content)
	}
	return out, nil
}

// tryFixLLMJSON attempts to repair JSON where the LLM produced unescaped
// double-quotes inside string values. It uses a simple heuristic: if a `"`
// appears to close a string but the next non-whitespace char is NOT one of
// `,`, `}`, `]`, or `:`, then that `"` was likely an unescaped interior quote
// and should be escaped as `\"`.
func tryFixLLMJSON(raw string) string {
	runes := []rune(raw)
	n := len(runes)
	if n == 0 {
		return ""
	}

	var buf strings.Builder
	buf.Grow(len(raw) + 64)

	i := 0
	// skip leading whitespace
	for i < n && (runes[i] == ' ' || runes[i] == '\t' || runes[i] == '\n' || runes[i] == '\r') {
		buf.WriteRune(runes[i])
		i++
	}
	if i >= n || runes[i] != '{' {
		return ""
	}
	buf.WriteRune(runes[i])
	i++

	// state machine: walk through top-level object
	for i < n {
		// skip whitespace
		for i < n && isJSONWhitespace(runes[i]) {
			buf.WriteRune(runes[i])
			i++
		}
		if i >= n {
			break
		}
		if runes[i] == '}' {
			buf.WriteRune(runes[i])
			return buf.String()
		}
		// expect a key string
		if runes[i] != '"' {
			return "" // can't fix
		}
		i = copyJSONString(&buf, runes, i)
		if i < 0 {
			return ""
		}
		// skip whitespace, expect ':'
		for i < n && isJSONWhitespace(runes[i]) {
			buf.WriteRune(runes[i])
			i++
		}
		if i >= n || runes[i] != ':' {
			return ""
		}
		buf.WriteRune(':')
		i++
		// skip whitespace, expect value string
		for i < n && isJSONWhitespace(runes[i]) {
			buf.WriteRune(runes[i])
			i++
		}
		if i >= n || runes[i] != '"' {
			return "" // non-string value, bail
		}
		// For values, use lenient string copy that handles unescaped quotes
		i = copyJSONStringLenient(&buf, runes, i)
		if i < 0 {
			return ""
		}
		// skip whitespace, expect ',' or '}'
		for i < n && isJSONWhitespace(runes[i]) {
			buf.WriteRune(runes[i])
			i++
		}
		if i >= n {
			break
		}
		if runes[i] == ',' {
			buf.WriteRune(',')
			i++
		} else if runes[i] == '}' {
			buf.WriteRune('}')
			return buf.String()
		} else {
			return ""
		}
	}
	return ""
}

func isJSONWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

// copyJSONString copies a standard JSON string (key) from runes[start] into buf.
// Returns the index after the closing quote, or -1 on error.
func copyJSONString(buf *strings.Builder, runes []rune, start int) int {
	if runes[start] != '"' {
		return -1
	}
	buf.WriteRune('"')
	i := start + 1
	for i < len(runes) {
		if runes[i] == '\\' && i+1 < len(runes) {
			buf.WriteRune(runes[i])
			buf.WriteRune(runes[i+1])
			i += 2
		} else if runes[i] == '"' {
			buf.WriteRune('"')
			return i + 1
		} else {
			buf.WriteRune(runes[i])
			i++
		}
	}
	return -1
}

// copyJSONStringLenient copies a JSON string value, tolerating unescaped quotes.
// If a `"` is encountered and the next non-ws char is NOT `,`, `}`, or end-of-input,
// treat it as an interior quote that should be escaped.
func copyJSONStringLenient(buf *strings.Builder, runes []rune, start int) int {
	if runes[start] != '"' {
		return -1
	}
	buf.WriteRune('"')
	i := start + 1
	n := len(runes)
	for i < n {
		if runes[i] == '\\' && i+1 < n {
			buf.WriteRune(runes[i])
			buf.WriteRune(runes[i+1])
			i += 2
		} else if runes[i] == '"' {
			// Look ahead: is this the real end of the string?
			j := i + 1
			for j < n && isJSONWhitespace(runes[j]) {
				j++
			}
			if j >= n || runes[j] == ',' || runes[j] == '}' || runes[j] == ']' {
				// This is the real closing quote
				buf.WriteRune('"')
				return i + 1
			}
			// Check if it looks like a new key starts: "..." : (next key pattern)
			// Pattern: `"` <whitespace> `,` means end of value
			// Pattern: `"` <whitespace> `}` means end of object
			// Otherwise: it's an unescaped interior quote
			buf.WriteString(`\"`)
			i++
		} else {
			buf.WriteRune(runes[i])
			i++
		}
	}
	// Reached end without closing quote, force close
	buf.WriteRune('"')
	return n
}

type chatRequest struct {
	Model          string         `json:"model"`
	Messages       []chatMessage  `json:"messages"`
	ResponseFormat *responseShape `json:"response_format,omitempty"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type responseShape struct {
	Type string `json:"type"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func setupLogger() error {
	var outputs []io.Writer
	outputs = append(outputs, os.Stdout)

	if config.LogFile != "" {
		dir := config.LogDir
		if dir == "" {
			dir = "."
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		f, err := os.OpenFile(dir+string(os.PathSeparator)+config.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
		if err != nil {
			return err
		}
		outputs = append(outputs, f)
	}

	log.SetOutput(io.MultiWriter(outputs...))
	return nil
}
