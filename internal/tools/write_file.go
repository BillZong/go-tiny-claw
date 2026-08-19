package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/BillZong/go-tiny-claw/internal/schema"
)

type writeFileTool struct {
	workDir string
}

func NewWriteFileTool(workDir string) (BaseTool, error) {
	if workDir == "" {
		return nil, fmt.Errorf("必须提供工作路径")
	}
	return &writeFileTool{
		workDir: workDir,
	}, nil
}

func (t *writeFileTool) Name() string {
	return "write_file"
}

func (t *writeFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "创建或覆盖写入一个文件。目录不存在会自动创建。仅支持相对路径",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "要写入的文件相对路径",
				},
				"content": map[string]interface{}{
					"type":        "string",
					"description": "要写入的完整文件内容",
				},
			},
			"required": []string{"path", "content"},
		},
	}
}

type writeFileArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *writeFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// 1. 解析参数
	var input writeFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析失败: %w", err)
	}

	// 防止路径穿越
	if strings.Contains(input.Path, "..") {
		return "", fmt.Errorf("不允许访问非工作路径内容")
	}

	// 2. 拼接路径
	absolutePath := path.Join(t.workDir, input.Path)

	// 3. 自动创建缺失的父目录
	if err := os.MkdirAll(filepath.Dir(absolutePath), 0755); err != nil {
		return "", fmt.Errorf("创建父目录失败: %w", err)
	}

	// 4. 写入文件内容
	if err := os.WriteFile(absolutePath, []byte(input.Content), 0644); err != nil {
		return "", fmt.Errorf("写入文件失败: %w", err)
	}

	return fmt.Sprintf("成功写入内容到文件: %s", input.Path), nil
}
