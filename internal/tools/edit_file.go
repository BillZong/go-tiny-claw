package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/BillZong/go-tiny-claw/internal/schema"
)

type editFileTool struct {
	workDir string
}

func NewEditFileToll(workDir string) (BaseTool, error) {
	if workDir == "" {
		return nil, fmt.Errorf("必须提供工作路径")
	}
	return &editFileTool{
		workDir: workDir,
	}, nil
}

func (t *editFileTool) Name() string {
	return "edit_file"
}

func (t *editFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "对现有文件进行局部的字符串替换。这比重写整个文件更安全、更快速。请提供足够的 old_text 上下文以确保匹配的唯一性。",
		InputSchema: map[string]interface{}{
			"type":     "object",
			"required": []string{"path", "old_text", "new_text"},
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要修改的文件路径",
				},
				"old_text": map[string]interface{}{
					"type":        "string",
					"description": "文件中原有的文本。必须包含足够的上下文（建议上下各多包含几行），以确保在文件中的唯一性。",
				},
				"new_text": map[string]interface{}{
					"type":        "string",
					"description": "要替换成的新文本",
				},
			},
		},
	}
}

// fuzzyReplace 实现了四级容错降级替换算法
func fuzzyReplace(originalContent, oldText, newText string) (string, error) {
	if originalContent == "" || oldText == "" {
		return "", fmt.Errorf("文件内容或需要替换的内容不能为空")
	}

	// L1: 精确匹配
	count := strings.Count(originalContent, oldText)
	if count == 1 {
		return strings.Replace(originalContent, oldText, newText, 1), nil
	}

	// L2: 换行符归一化
	normalizedContent := strings.ReplaceAll(originalContent, "\r\n", "\n")
	normalizedOld := strings.ReplaceAll(oldText, "\r\n", "\n")

	count = strings.Count(normalizedContent, normalizedOld)
	if count == 1 {
		return strings.Replace(normalizedContent, normalizedOld, newText, 1), nil
	}

	// L3: Trim space 匹配
	trimmedOld := strings.TrimSpace(normalizedOld)
	if trimmedOld != "" {
		count := strings.Count(normalizedContent, trimmedOld)
		if count == 1 {
			// 注意：这里替换时，我们只能替换被 Trim 后的部分，不能直接用 newText 破坏原本的缩进
			// 为了保持本专栏代码不过于冗长复杂，当触发 L3/L4 时，如果 newText 没有带有正确的缩进，
			// 可能会导致替换后代码格式不美观。但这总比直接报错让 Agent 死循环要好。
			return strings.Replace(normalizedContent, trimmedOld, newText, 1), nil
		}
	}

	// L4: 逐行去缩进匹配
	return lineByLineReplace(normalizedContent, normalizedOld, newText)
}

// 核心代码
func lineByLineReplace(content, oldText, newText string) (string, error) {
	contentLines := strings.Split(content, "\n")
	oldLines := strings.Split(oldText, "\n")

	if len(oldLines) == 0 || len(contentLines) < len(oldLines) {
		return "", fmt.Errorf("找不到该代码片段")
	}

	// 清理 oldLines 的每行首尾空白
	for i := range oldLines {
		oldLines[i] = strings.TrimSpace(oldLines[i])
	}

	matchCount := 0
	matchStartIndex := -1
	matchEndIndex := -1

	// 滑动窗口在原始文件中寻找匹配块
	for i := 0; i <= len(contentLines)-len(oldLines); i++ {
		isMatch := true
		for j := 0; j < len(oldLines); j++ {
			if strings.TrimSpace(contentLines[i+j]) != oldLines[j] {
				isMatch = false
				break
			}
		}

		if isMatch {
			matchCount++
			matchStartIndex = i
			matchEndIndex = i + len(oldLines)
		}
	}

	if matchCount == 0 {
		return "", fmt.Errorf("在文件中未找到 old_text，请大模型先调用 read_file 仔细确认文件内容和缩进")
	}
	if matchCount > 1 {
		return "", fmt.Errorf("模糊匹配到了 %d 处相似代码，请提供更多上下行代码以精确定位", matchCount)
	}

	// 逐行替换：将匹配到的原始行范围替换为 newText 拆分后的行（这里简单处理，将 newText 直接作为整体替换进去）
	var newContentLines []string
	newContentLines = append(newContentLines, contentLines[:matchStartIndex]...)
	newContentLines = append(newContentLines, newText) // 插入新内容
	newContentLines = append(newContentLines, contentLines[matchEndIndex:]...)

	return strings.Join(newContentLines, "\n"), nil
}

type editFileArgs struct {
	Path    string `json:"path"`
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

func (t *editFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input editFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	fullPath := filepath.Join(t.workDir, input.Path)

	// 读取原文件内容
	contentBytes, err := os.ReadFile(fullPath)
	if err != nil {
		return "", fmt.Errorf("读取文件失败: %w", err)
	}
	originContent := string(contentBytes)

	// 调用多级模糊替换算法
	newContent, err := fuzzyReplace(originContent, input.OldText, input.NewText)
	if err != nil {
		return "", err
	}

	// 将新内容安全地写回磁盘
	if err := os.WriteFile(fullPath, []byte(newContent), 0644); err != nil {
		return "", fmt.Errorf("回写文件失败: %w", err)
	}

	return fmt.Sprintf("✅ 成功修改文件: %s", input.Path), nil
}
