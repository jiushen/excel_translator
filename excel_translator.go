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
	"time"

	"github.com/xuri/excelize/v2"
)

/**************** 配置区：按需修改 ****************/

type Provider string

const (
	ProviderOpenAI   Provider = "openai"
	ProviderDeepSeek Provider = "deepseek"
)

// 在这里配置要用哪个服务商 & 对应的 Key / 模型
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
	Provider: ProviderDeepSeek, // 改成 ProviderOpenAI / ProviderDeepSeek 切换

	// ===== OpenAI 配置 =====
	OpenAIKey:   "***REMOVED***",
	OpenAIModel: "gpt-5.1",
	OpenAIURL:   "https://api.openai.com/v1/chat/completions",

	// ===== DeepSeek 配置 =====
	DeepseekKey:   "***REMOVED***",
	DeepseekModel: "deepseek-chat",
	DeepseekURL:   "https://api.deepseek.com/v1/chat/completions",

	BatchSize: 50, // 每批最多翻译多少条文本
}

/*********************** 主程序 ************************/

func main() {
	if len(os.Args) < 3 {
		fmt.Println("用法: translate_excel input.xlsx output.xlsx")
		return
	}
	input := os.Args[1]
	output := os.Args[2]

	if err := run(input, output); err != nil {
		log.Fatalf("执行失败: %v", err)
	}
	log.Println("处理完成 ✅")
}

func run(input, output string) error {
	// 打开 Excel
	f, err := excelize.OpenFile(input)
	if err != nil {
		return fmt.Errorf("打开文件失败: %w", err)
	}
	defer f.Close()

	// 1. 收集所有要翻译的文本（去重）
	uniqueTexts := make(map[string]struct{})

	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			return fmt.Errorf("读取 sheet %s 失败: %w", sheet, err)
		}
		for rIdx, row := range rows {
			for cIdx := range row {
				cellRef, _ := excelize.CoordinatesToCellName(cIdx+1, rIdx+1)

				// 跳过公式
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

	// 转成切片
	originals := make([]string, 0, len(uniqueTexts))
	for s := range uniqueTexts {
		originals = append(originals, s)
	}
	log.Printf("需要翻译的唯一文本数量: %d\n", len(originals))

	// 2. 批量翻译
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

	// 3. 回写 Excel：命中原文就覆盖
	for _, sheet := range f.GetSheetList() {
		rows, err := f.GetRows(sheet)
		if err != nil {
			return fmt.Errorf("读取 sheet %s 失败: %w", sheet, err)
		}
		for rIdx, row := range rows {
			for cIdx := range row {
				cellRef, _ := excelize.CoordinatesToCellName(cIdx+1, rIdx+1)

				// 跳过公式
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

	// 4. 保存结果
	if err := f.SaveAs(output); err != nil {
		return fmt.Errorf("保存文件失败: %w", err)
	}
	log.Printf("已输出到: %s", output)
	return nil
}

/******************** 调用大模型翻译 ********************/

// Chat 请求 & 响应结构（OpenAI / DeepSeek 通用精简版）

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

// translateBatch：根据 config.Provider 选择不同服务商
func translateBatch(ctx context.Context, src []string) (map[string]string, error) {
	switch config.Provider {
	case ProviderOpenAI:
		return callChatAPI(ctx, src,
			config.OpenAIURL,
			config.OpenAIKey,
			config.OpenAIModel,
		)
	case ProviderDeepSeek:
		return callChatAPI(ctx, src,
			config.DeepseekURL,
			config.DeepseekKey,
			config.DeepseekModel,
		)
	default:
		return nil, fmt.Errorf("未知 Provider: %s", config.Provider)
	}
}

// callChatAPI：同时兼容 OpenAI / DeepSeek 的调用逻辑
func callChatAPI(ctx context.Context, texts []string, baseURL, apiKey, model string) (map[string]string, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("API Key 未配置")
	}

	// 把待翻译文本打包成 JSON，交给模型原样返回映射
	payload := map[string]any{
		"texts": texts,
	}
	jsonBytes, _ := json.Marshal(payload)

	system := `你是一个专门负责 Azure 技术文档翻译的助手，负责将「日文」翻译成「简体中文」。

【背景】
- 这些字符串来自 Azure 各类组件（例如 Application Gateway、Public IP、Virtual Network、Auto-scaling、SKU、可用性区域等）的「配置 / 购买参数说明」Excel 文档。
- 每一条字符串通常是：字段名、选项名称、帮助说明、参数解释、默认值说明等。

【必须严格遵守的规则】
1. 只翻译其中的「日文」部分为简体中文。
2. 所有英文单词必须原样保留，不要翻译、不要修改，也不要意译。
   （包括但不限于：Azure 服务名、SKU 名、协议名、IP 类型、参数名、选项值，如 Application Gateway、Public IP、Virtual Network、Standard_v2、WAF_v2、HTTP、HTTPS、TCP、ZoneRedundant 等）
3. 所有数字、符号、括号、标点、结构都必须保持不变（例如 (), [], {}, :, ., /, -, 空格 等）。
4. 不要增删内容，不要重排结构，只在原位置上将日文替换为对应的中文表达。
5. 如果某个字符串完全由英文、数字和符号构成（没有日文），请原样返回，不要改动。
6. 最终输出必须是合法的 JSON 对象：{"原文字符串": "翻译后的字符串", ...}，不要输出任何解释、注释或多余文字。`

	user := fmt.Sprintf(`下面是一个 JSON 对象，其中包含一个 "texts" 字段，里面是若干待翻译的字符串。

请对其中每个字符串进行处理：
- 只翻译日文为简体中文；
- 英文、数字、符号、Azure 技术名词、SKU、IP 等全部保持不变；
- 按「原文 → 译文」的方式，返回一个 JSON 对象。

输入 JSON 如下：
%s`, string(jsonBytes))

	reqBody := chatRequest{
		Model: model,
		Messages: []chatMessage{
			{Role: "system", Content: system},
			{Role: "user", Content: user},
		},
		// 要求模型直接返回 JSON 对象
		ResponseFormat: &responseShape{Type: "json_object"},
	}

	bodyBytes, _ := json.Marshal(reqBody)

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

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API 返回错误: %s\n%s", resp.Status, string(b))
	}

	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, fmt.Errorf("解析响应失败: %w", err)
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("响应中没有 choices")
	}

	content := cr.Choices[0].Message.Content

	// 解析 JSON 映射
	out := make(map[string]string)
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return nil, fmt.Errorf("解析模型返回 JSON 失败: %w\n原始内容: %s", err, content)
	}
	return out, nil
}
