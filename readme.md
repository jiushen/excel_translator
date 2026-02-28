# excel_translator

这是一个基于 Go 的文档翻译工具，支持以下文件类型：

- Excel：`.xlsx`、`.xlsm`
- Word：`.docx`
- PowerPoint：`.pptx`

程序通过 LLM 提供方（`deepseek` 或 `openai`）翻译文档文本，并尽量保留原始文件结构与格式。

## 功能说明

- 可根据输入文件扩展名自动识别类型
- 支持翻译方向：
  - `zh2ja`：中文转日文
  - `ja2zh`：日文转中文
- 支持 `deepseek` 和 `openai`
- 运行时从 `config.yaml` 加载配置
- 支持 `-debug` 输出调试日志
- PPTX 翻译仅替换已有 `<a:t>` 节点中的文本，不再重编码整页 XML

## 环境要求

- Go `1.25.1` 或更高版本
- 可用的 `config.yaml`
- 已配置对应提供方的 API 凭证

## 本地构建

构建可执行文件：

```bash
go build -o translate .
```

## 运行方法

基本用法：

```bash
./translate [-type excel|word|pptx] [-direction zh2ja|ja2zh] [-provider deepseek|openai] [-config config.yaml] <输入文件> <输出文件>
```

示例：

```bash
./translate input.xlsx output.xlsx
./translate -direction ja2zh input.docx output.docx
./translate -type pptx -direction zh2ja input.pptx output.pptx
./translate -config myconfig.yaml -provider openai input.docx output.docx
./translate -debug input.xlsx output.xlsx
```

## 配置文件

默认配置文件为 `config.yaml`。

程序会从该文件中读取：

- 提供方配置
- API Key
- 模型名称
- 批处理大小
- 提示词（Prompt）

如果你要使用其他配置文件：

```bash
./translate -config myconfig.yaml input.xlsx output.xlsx
```

## 发布流程

仓库中已经包含以下发布相关文件：

- 本地 WSL / Linux 发版脚本：`scripts/release.sh`
- GitHub Actions 发布工作流：`.github/workflows/release.yml`

### 本地发版脚本

在 WSL 中执行：

```bash
chmod +x scripts/release.sh
./scripts/release.sh v1.0.0
```

脚本会自动执行以下步骤：

1. 检查当前分支状态
2. 推送当前分支的已提交内容到 `origin`
3. 基于当前 `HEAD` 创建一个带注释的 tag
4. 将该 tag 推送到 `origin`

注意：

- 脚本不会执行 `git add` 或 `git commit`
- 如果本地还有未提交修改，脚本会提示，但发版仍然只基于当前已经提交的 `HEAD`
- 目录中未跟踪或无关文件不会被自动提交到 git

### GitHub Actions 自动构建

当推送符合 `v*` 规则的 tag（例如 `v1.0.0`）后，GitHub Actions 会自动构建以下平台版本：

- Windows `amd64`
- Windows `arm64`
- Linux `amd64`
- Linux `arm64`
- macOS `amd64`
- macOS `arm64`（Apple Silicon / M 系列）

工作流会自动打包产物，并上传到对应的 GitHub Release。

## 说明

- PPTX 处理仅修改幻灯片和备注页中的文本节点，其他 ZIP 内部文件会原样复制。
- 跨平台构建使用 `CGO_ENABLED=0`，尽量生成静态二进制。
- 如果 `config.yaml` 中包含本地密钥或敏感信息，不要将真实凭证提交到仓库。
