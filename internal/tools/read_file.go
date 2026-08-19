package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/BillZong/go-tiny-claw/internal/schema"
)

type ReadFileTool struct {
	workDir string
}

func NewReadFileTool(workDir string) BaseTool {
	return &ReadFileTool{
		workDir: workDir,
	}
}

func (t *ReadFileTool) Name() string {
	return "read_file"
}

func (t *ReadFileTool) Definition() schema.ToolDefinition {
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

func (t *ReadFileTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// 1. 延迟解析：将大模型传过来的 JSON 参数解析为强类型结构体
	var input readFileArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析错误: %w", err)
	}

	// 2. 拼接绝对路径
	// TODO: 防路径穿越检测
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
