package engine

import (
	"context"
	"fmt"
	"strings"
)

type terminalReporter struct{}

func NewTerminalReporter() Reporter {
	return &terminalReporter{}
}

func (r *terminalReporter) OnThinking(ctx context.Context) {
	fmt.Printf("\n[🤔 思考中] 模型正在推理...\n")
}

func (r *terminalReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	fmt.Printf("[🛠️ 调用工具] %s\n", toolName)
	// 截断过长的参数显示，保持终端清爽
	displayArgs := strings.ReplaceAll(args, "\n", "\\n")
	displayArgs = strings.ReplaceAll(displayArgs, "\r", "\\r")
	if len(displayArgs) > 150 {
		displayArgs = displayArgs[:150] + "... (已截断)"
	}
	fmt.Printf(" 参数: %s\n", displayArgs)
}

func (r *terminalReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		fmt.Printf("[🛠️ 执行失败] %s\n", toolName)
		// 显示错误信息
		if result != "" {
			fmt.Printf("	错误: %s\n", result)
		}
	} else {
		fmt.Printf("[🛠️ 执行成功] %s\n", toolName)
	}
}

func (r *terminalReporter) OnMessage(ctx context.Context, content string) {
	if content == "" {
		return
	}
	fmt.Printf("\n🤖 Agent 回复:\n%s\n\n", content)
}
