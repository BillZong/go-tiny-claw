package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

	"github.com/BillZong/go-tiny-claw/internal/schema"
)

type AgentRunner interface {
	RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry Registry, reporter interface{}) (string, error)
}

type SubagentTool struct {
	runner AgentRunner

	readOnlyRegistry Registry
	reporter         interface{}
}

func NewSubagentTool(runner AgentRunner, readOnlyRegistry Registry, reporter interface{}) *SubagentTool {
	return &SubagentTool{
		runner:           runner,
		readOnlyRegistry: readOnlyRegistry,
		reporter:         reporter,
	}
}

func (t *SubagentTool) Name() string {
	return "spawn_subagent"
}

// Definition 向主 Agent 暴露这个工具的能力
func (t *SubagentTool) Definition() schema.ToolDefinition {
	return schema.ToolDefinition{
		Name:        t.Name(),
		Description: "派出一个专门用于深度探索（Exploration）的子智能体。当你需要阅读大量代码、跨文件查找逻辑时请调用此工具。它在探索完毕后，会给你返回一份极度精炼的摘要报告。",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"task_prompt": map[string]interface{}{
					"type":        "string",
					"description": "给子智能体下大的明确指令。",
				},
			},
			"required": []string{"task_prompt"},
		},
	}
}

type subAgentArgs struct {
	TaskPrompt string `json:"task_prompt"`
}

func (t *SubagentTool) Execute(ctx context.Context, args json.RawMessage) (string, error) {
	var input subAgentArgs
	if err := json.Unmarshal(args, &input); err != nil {
		return "", fmt.Errorf("解析参数失败: %w", err)
	}

	log.Printf("[Subagent] 🚀 主 Agent 发起委派！正在拉起探路者: [%s]...\n", input.TaskPrompt)

	// 拉起一个完全物理隔离的子循环，我们把针对该任务的专项指令传给子智能体，并仅提供 readOnlyRegistry。
	summary, err := t.runner.RunSub(ctx, input.TaskPrompt, t.readOnlyRegistry, t.reporter)
	if err != nil {
		return fmt.Errorf("子智能体执行失败: %v", err).Error(), nil
	}

	log.Printf("[Subagent] ✅ 子智能体任务结束。报告返回给主干...")

	return fmt.Sprintf("【子智能体探索报告】:\n%s", summary), nil
}
