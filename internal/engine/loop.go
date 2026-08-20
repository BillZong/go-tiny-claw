package engine

import (
	"context"
	"fmt"
	"log"
	"sync"

	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/schema"
	"github.com/BillZong/go-tiny-claw/internal/tools"
)

// AgentEngine 是微型 OS 的核心驱动
type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	WorkDir        string // WorkDir (工作区): 借鉴 OpenClaw 的理念，Agent 必须有一个明确的物理边界
	EnableThinking bool   // EnableThinking (是否启用思考): 允许模型在执行工具前进行推理
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, workDir string, enableThinking bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		WorkDir:        workDir,
		EnableThinking: enableThinking,
	}
}

// Run 启动 Agent 的生命周期
func (e *AgentEngine) Run(ctx context.Context, userPrompt string, reporter Reporter) error {
	log.Printf("[Engine] 引擎启动，锁定工作区: %s\n", e.WorkDir)
	log.Printf("[Engine] 慢思考模式: %v\n", e.EnableThinking)

	// 1. 初始化会话的 Context (上下文内存)
	// 在真实的场景中，这里会由动态 Prompt 组装器加载 AGENTS.md。目前我们先硬编码。
	contextHistory := []schema.Message{
		{
			Role:    schema.RoleSystem,
			Content: "You are go-tiny-claw, an expert coding assistant. You have full access to tools in the workspace.",
		},
		{
			Role:    schema.RoleUser,
			Content: userPrompt,
		},
	}

	turnCount := 0

	// 2. The Main Loop: 心跳开始 (标准的 ReAct 循环)
	for {
		turnCount++
		log.Printf("========== [Turn %d] 开始 ==========\n", turnCount)

		// 获取当前挂载的所有工具定义
		availableTools := e.registry.GetAvailableTools()

		// ===================================================================
		// Phase 1: 慢思考阶段 - 剥夺工具，强制规划
		// ===================================================================
		if e.EnableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}

			thinkResponse, err := e.provider.Generate(ctx, contextHistory, nil)
			if err != nil {
				return fmt.Errorf("Thinking 阶段生成失败: %w", err)
			}

			if thinkResponse.Content != "" {
				fmt.Printf("[内部思考 Trace]: %s\n", thinkResponse.Content)
				contextHistory = append(contextHistory, *thinkResponse)
			}
		}

		// 向大模型发起推理请求 (包含 Reasoning)
		actionResp, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("模型生成失败: %w", err)
		}

		// 将模型的响应完整追加到上下文历史中
		contextHistory = append(contextHistory, *actionResp)

		// 如果模型回复了纯文本，打印出来 (这通常是它的思考过程，或是最终结果)
		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(ctx, actionResp.Content)
		}

		// 3. 退出条件判断
		// 如果模型没有请求任何工具调用，说明它认为任务已经完成，跳出循环。
		if len(actionResp.ToolCalls) == 0 {
			break
		}

		// 4. 执行行动 (Action) 与 获取观察结果 (Observation)

		// 1. 预分配一个固定长度的切片，用于安全地存放各个并发工具的执行结果（Observation） // 长度与 ToolCalls 的数量完全一致 observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup
		wg.Add(len(actionResp.ToolCalls))

		for i, toolCall := range actionResp.ToolCalls {
			// 开启协程。注意：一定要将索引 i 和 toolCall 作为参数传入匿名函数，防止闭包变量捕获陷阱！
			go func(i int, tc schema.ToolCall) {
				defer wg.Done()

				if reporter != nil {
					reporter.OnToolCall(ctx, tc.Name, string(tc.Arguments))
				}

				result := e.registry.Execute(ctx, tc)
				if reporter != nil {
					// 为了防止大文件读取导致飞书消息过长被截断，我们仅汇报工具执行状态
					// 注意：传递给大模型的 observationMsgs 依然是完整数据，只是人类看到的 Reporter 是缩略版
					displayOutput := result.Output
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "...（已截断）"
					}
					reporter.OnToolResult(ctx, tc.Name, displayOutput, result.IsError)
				}

				// 【线程安全】: 由于每个 Goroutine 操作的是预分配切片的不同索引，这里不需要加锁 (Mutex)，性能极高！
				observationMsgs[i] = schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: tc.ID,
				}
			}(i, toolCall)
		}

		wg.Wait()

		// 聚合观察结果
		for _, obs := range observationMsgs {
			contextHistory = append(contextHistory, obs)
		}

		// 循环回到开头，模型将带着新加入的 Observation 继续它的下一轮思考...
	}

	return nil
}
