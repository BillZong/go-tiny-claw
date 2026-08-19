package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"time"

	"github.com/BillZong/go-tiny-claw/internal/schema"
)

type bashTool struct {
	workDir string
}

func NewBashTool(workDir string) (BaseTool, error) {
	if workDir == "" {
		return nil, fmt.Errorf("必须提供工作路径")
	}
	return &bashTool{
		workDir: workDir,
	}, nil
}

func (t *bashTool) Name() string {
	return "bash"
}

func (t *bashTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "在当前工作区执行任意的 bash 命令。支持链式命令(如 &&)。返回标准输出(stdout)和标准错误(stderr)。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]interface{}{
					"type":        "string",
					"description": "要执行的 bash 命令，例如: ls -la 或 go test ./...",
				},
			},
			"required": []string{"command"},
		},
	}
}

type bashArgs struct {
	Command string `json:"command"`
}

func (t *bashTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	// 解析参数
	var input bashArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("参数解析失败, %w", err)
	}

	// 【驾驭底线 1】：Time Budgeting（时间预算与超时控制）
	// 给予 bash 命令一个最大执行时间，防止大模型卡死进程（比如运行了 top 或持续监听的 Web 服务
	timeoutCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 在 MacOS/Linux 中，我们需要将指令包裹在 `bash -c` 中执行，以支持环境变量、管道和逻辑与(&&)等复杂 Shell 语法。
	cmd := exec.CommandContext(timeoutCtx, "bash", "-c", input.Command)

	// 【驾驭底线 2】：绑定执行的工作区目录
	// 确保命令默认在用户指定的 workdir 下执行，而不是引擎启动时的绝对路径
	cmd.Dir = t.workDir

	// 执行并捕获 CombiedOutput（合并 stdout 和 stderr）
	out, err := cmd.CombinedOutput()
	outputStr := string(out)

	switch timeoutCtx.Err() {
	case context.Canceled:
		return "", fmt.Errorf("用户取消了请求，停止继续执行")
	case context.DeadlineExceeded:
		return outputStr + "\n[警告: 命令执行超时(30s)，已被系统强制终止。如果是启动常驻服务，请尝试将其转入后台。]", nil
	}

	// 【驾驭底线 3】：错误原样回传（Self-Correction 自愈机制）
	// 当 bash 报错时，我们绝对不能返回 go 的 error 阻断程序，而是必须把 err 和 outputStr 拼接成字符串返回，利用大模型自纠错能力自己分析报错！
	if err != nil {
		return fmt.Sprintf("执行报错: %v\n输出:\n%s", err, outputStr), nil
	}

	// 【驾驭底线 4】：长度截断保护（防 OOM）
	const maxLen = 8000
	if len(outputStr) > maxLen {
		return fmt.Sprintf("%s\n\n...[终端输出过长，已截断至前 %d 字节]...", outputStr[:maxLen], maxLen), nil
	}

	return outputStr, nil
}
