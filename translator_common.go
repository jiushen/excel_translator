package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// debug controls whether to dump request/response for troubleshooting.
var debug bool

type Provider string

const (
	ProviderOpenAI   Provider = "openai"
	ProviderDeepSeek Provider = "deepseek"
)

// TODO: 填写自己的 Key/Model/URL；建议改为环境变量读取。
var config = struct {
	Provider Provider

	OpenAIKey   string
	OpenAIModel string
	OpenAIURL   string

	DeepseekKey   string
	DeepseekModel string
	DeepseekURL   string

	BatchSize int
}{
	Provider: ProviderDeepSeek,

	OpenAIKey:   "***REMOVED***",
	OpenAIModel: "gpt-5.1",
	OpenAIURL:   "https://api.openai.com/v1/chat/completions",

	DeepseekKey:   "***REMOVED***",
	DeepseekModel: "deepseek-chat",
	DeepseekURL:   "https://api.deepseek.com/v1/chat/completions",

	BatchSize: 50,
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

func translateBatch(ctx context.Context, src []string) (map[string]string, error) {
	switch config.Provider {
	case ProviderOpenAI:
		return callChatAPI(ctx, src, config.OpenAIURL, config.OpenAIKey, config.OpenAIModel)
	case ProviderDeepSeek:
		return callChatAPI(ctx, src, config.DeepseekURL, config.DeepseekKey, config.DeepseekModel)
	default:
		return nil, fmt.Errorf("unknown provider: %s", config.Provider)
	}
}

func callChatAPI(ctx context.Context, texts []string, baseURL, apiKey, model string) (map[string]string, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API key missing")
	}

	payload := map[string]any{"texts": texts}
	jsonBytes, _ := json.Marshal(payload)

	system := `你是 Azure 技术文档的专业翻译助手，任务是将字符串中的【简体中文】翻译为【日文】，并保持所有英文、数字、符号、Azure 技术词原样不变。
规则：
1. 只翻译中文；英文必须完全保持原样。
2. 若字符串中混有英文与中文，英文保持不动，仅翻译中文，再组合成完整日文句子。例：
   - "在选择 Application Gateway 的情况下" -> "Application Gateway を選択した場合"
   - "请选择用于 ApplicationGateway 的子网" -> "ApplicationGateway 用サブネットを選択してください"
3. 若字符串完全没有中文字符，则原样返回。
4. 不得增删文本内容、不得意译、不得调整格式，仅替换中文部分。
5. 输出必须是 JSON 对象：{"原文": "译文", ...}，使用原文字符串作为键，禁止任何额外说明。`

	user := fmt.Sprintf(`请将下列 JSON 中 texts 数组里的所有字符串按上述规则翻译为日文，并返回 {"原文": "译文"} 的 JSON 对象（键为原文，值为译文）。
输入：%s`, string(jsonBytes))

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
		// 不打印敏感 header，仅打印 body 与批次大小
		fmt.Printf("[DEBUG] request url=%s model=%s batch=%d body=%s\n", baseURL, model, len(texts), string(bodyBytes))
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
