已添加方向切换和外置 Prompt 配置：

新增 -direction 选项：zh2ja（中文→日文，默认）或 ja2zh（日文→中文）。  
新增 -config 选项（默认 config.yaml），可在 YAML 中配置 provider/keys/models/BatchSize 及两套 Prompt。不存在则使用内置默认值。  
调整主程序：translateExcel/translateWord 接受方向参数；translateBatch 使用方向选择对应 Prompt；调试输出仍可用 -debug。  
引入 YAML 解析依赖 gopkg.in/yaml.v3 v3.0.1（已写入 go.mod）。  
运行示例：  

中文→日文：./translate -direction zh2ja input.docx output.docx  
日文→中文：./translate -direction ja2zh input.docx output.docx  
指定配置：./translate -config myconfig.yaml -direction ja2zh input.xlsx output.xlsx  
