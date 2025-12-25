package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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

	PromptZH2JA string `yaml:"prompt_zh2ja"`
	PromptJA2ZH string `yaml:"prompt_ja2zh"`
}

// populated from config file (and CLI flags)
var config Config

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
	if config.BatchSize <= 0 {
		config.BatchSize = 50
	}
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

	if debug {
		fmt.Printf("[DEBUG] request url=%s model=%s dir=%s batch=%d body=%s\n", baseURL, model, dir, len(texts), string(bodyBytes))
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+apiKey)
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 60 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	respBytes, _ := io.ReadAll(resp.Body)

	if debug {
		fmt.Printf("[DEBUG] response status=%s body=%s\n", resp.Status, string(respBytes))
	}

	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("API error: %s\n%s", resp.Status, string(respBytes))
	}

	var cr chatResponse
	if err := json.Unmarshal(respBytes, &cr); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("no choices in response")
	}

	content := cr.Choices[0].Message.Content
	out := make(map[string]string)
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return nil, fmt.Errorf("unmarshal model JSON: %w\nraw: %s", err, content)
	}
	return out, nil
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
