package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/BillZong/go-tiny-claw/internal/schema"
)

type readFileTool struct {
	workDir string
}

func NewReadFileTool(workDir string) (BaseTool, error) {
	if workDir == "" {
		return nil, fmt.Errorf("必须提供工作路径")
	}
	return &readFileTool{
		workDir: workDir,
	}, nil
}

func (t *readFileTool) Name() string {
	return "read_file"
}

func (t *readFileTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "读取指定路径的文件内容",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "object",
					"description": "要读取的文件路径，如 cmd/claw/main.go",
				},
			},
			"required": []string{"path"},
		},
	}
}

type readFileArgs struct {
	Path string `json:"path"`
}

func (t *readFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// 1. 延迟解析：将大模型传过来的 JSON 参数解析为强类型结构体
	var input readFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析错误: %w", err)
	}

	// 防止路径穿越
	if strings.Contains(input.Path, "..") {
		return "", fmt.Errorf("不允许访问非工作路径内容")
	}

	// 2. 拼接绝对路径
	absolutePath := filepath.Join(t.workDir, input.Path)

	// 3. 执行物理 IO 操作
	file, err := os.Open(absolutePath)
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	defer file.Close()
	info, err := os.Stat(absolutePath)
	if err != nil {
		return "", err
	}

	// 4. 【核心防线】长度阶段保护
	// 为了防止大模型读取几百 MB 的日志文件导致 Context 瞬间爆炸（OOM），我们在工具内部直接进行物理截断
	const maxLen = 8000
	limitFile := io.LimitReader(file, int64(maxLen))
	content, err := io.ReadAll(limitFile)
	if err != nil {
		return "", fmt.Errorf("读取文件内容失败: %w", err)
	}
	if info.Size() > maxLen {
		_ = fmt.Appendf(content, "...[由于内容过长，已被系统截断至前 %d 字节]...", maxLen)
	}

	return string(content), nil
}
