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

	DirectionZH2JA Direction = "zh2ja" // 中文 -> 日文
	DirectionJA2ZH Direction = "ja2zh" // 日文 -> 中文
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

// defaults if config.yaml absent
var config = Config{
	Provider: ProviderDeepSeek,

	OpenAIKey:   "***REMOVED***",
	OpenAIModel: "gpt-5.1",
	OpenAIURL:   "https://api.openai.com/v1/chat/completions",

	DeepseekKey:   "***REMOVED***",
	DeepseekModel: "deepseek-chat",
	DeepseekURL:   "https://api.deepseek.com/v1/chat/completions",

	BatchSize: 50,

	PromptZH2JA: `你是 Azure 技术文档的专业翻译助手，任务是将字符串中的【简体中文】翻译为【日文】，并保持所有英文、数字、符号、Azure 技术词原样不变。
规则：
1. 只翻译中文；英文必须完全保持原样。
2. 若字符串中混有英文与中文，英文保持不动，仅翻译中文，再组合成完整日文句子。例：
   - "在选择 Application Gateway 的情况下" -> "Application Gateway を選択した場合"
   - "请选择用于 ApplicationGateway 的子网" -> "ApplicationGateway 用サブネットを選択してください"
3. 若字符串完全没有中文字符，则原样返回。
4. 不得增删文本内容、不得意译、不得调整格式，仅替换中文部分。
5. 输出必须是 JSON 对象：{"原文": "译文", ...}，使用原文字符串作为键，禁止任何额外说明。`,

	PromptJA2ZH: `你是 Azure 技术文档的专业翻译助手，任务是将字符串中的【日文】翻译为【简体中文】，并保持所有英文、数字、符号、Azure 技术词原样不变。
规则：
1. 只翻译日文；英文必须完全保持原样。
2. 若字符串中混有英文与日文，英文保持不动，仅翻译日文，再组合成完整中文句子。例：
   - "Application Gateway を選択した場合" -> "在选择 Application Gateway 的情况下"
   - "ApplicationGateway用サブネットを選択" -> "请选择用于 ApplicationGateway 的子网"
3. 若字符串完全没有日文字符，则原样返回。
4. 不得增删文本内容、不得意译、不得调整格式，仅替换日文部分。
5. 输出必须是 JSON 对象：{"原文": "译文", ...}，禁止任何额外说明。`,
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

func loadConfig(path string) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}
	var cfg Config
	if err := yaml.Unmarshal(b, &cfg); err != nil {
		return err
	}
	if cfg.Provider != "" {
		config.Provider = cfg.Provider
	}
	if cfg.OpenAIKey != "" {
		config.OpenAIKey = cfg.OpenAIKey
	}
	if cfg.OpenAIModel != "" {
		config.OpenAIModel = cfg.OpenAIModel
	}
	if cfg.OpenAIURL != "" {
		config.OpenAIURL = cfg.OpenAIURL
	}
	if cfg.DeepseekKey != "" {
		config.DeepseekKey = cfg.DeepseekKey
	}
	if cfg.DeepseekModel != "" {
		config.DeepseekModel = cfg.DeepseekModel
	}
	if cfg.DeepseekURL != "" {
		config.DeepseekURL = cfg.DeepseekURL
	}
	if cfg.BatchSize > 0 {
		config.BatchSize = cfg.BatchSize
	}
	if cfg.PromptZH2JA != "" {
		config.PromptZH2JA = cfg.PromptZH2JA
	}
	if cfg.PromptJA2ZH != "" {
		config.PromptJA2ZH = cfg.PromptJA2ZH
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
		return fmt.Sprintf(`请将下列 JSON 中 texts 数组里的所有字符串按上述规则翻译为中文，并返回 {"原文": "译文"} 的 JSON 对象（键为原文，值为译文）。
输入：%s`, jsonPayload)
	}
	return fmt.Sprintf(`请将下列 JSON 中 texts 数组里的所有字符串按上述规则翻译为日文，并返回 {"原文": "译文"} 的 JSON 对象（键为原文，值为译文）。
输入：%s`, jsonPayload)
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
