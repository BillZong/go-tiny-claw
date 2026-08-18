package engine

import (
	"context"
	"fmt"
	"log"

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
func (e *AgentEngine) Run(ctx context.Context, userPrompt string) error {
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
			log.Printf("[Engine][Phase 1] 剥夺工具访问权，强制进入慢思考与规划阶段...\n")

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
		log.Println("[Engine][Phase 2] 恢复工具挂载，等待模型采取行动")
		responseMsg, err := e.provider.Generate(ctx, contextHistory, availableTools)
		if err != nil {
			return fmt.Errorf("模型生成失败: %w", err)
		}

		// 将模型的响应完整追加到上下文历史中
		contextHistory = append(contextHistory, *responseMsg)

		// 如果模型回复了纯文本，打印出来 (这通常是它的思考过程，或是最终结果)
		if responseMsg.Content != "" {
			fmt.Printf("🤖 [对外回复]: %s\n", responseMsg.Content)
		}

		// 3. 退出条件判断
		// 如果模型没有请求任何工具调用，说明它认为任务已经完成，跳出循环。
		if len(responseMsg.ToolCalls) == 0 {
			log.Println("[Engine] 任务完成，退出循环。")
			break
		}

		// 4. 执行行动 (Action) 与 获取观察结果 (Observation)
		log.Printf("[Engine] 模型请求调用 %d 个工具...\n", len(responseMsg.ToolCalls))

		// === 顺序执行工具调用（可选：可以改为并行执行）===
		for _, toolCall := range responseMsg.ToolCalls {
			log.Printf("  -> 🛠️ 执行工具: %s, 参数: %s\n", toolCall.Name, string(toolCall.Arguments))

			// 通过 Registry 路由并执行底层工具
			result := e.registry.Execute(ctx, toolCall)

			if result.IsError {
				log.Printf("  -> ❌ 工具执行报错: %s\n", result.Output)
			} else {
				log.Printf("  -> ✅ 工具执行成功 (返回 %d 字节)\n", len(result.Output))
			}

			// 将工具执行的观察结果 (Observation) 封装为 User Message 追加到上下文中
			// 注意：ToolCallID 必须携带！这是维系大模型推理链条的关键
			observationMsg := schema.Message{
				Role:       schema.RoleUser,
				Content:    result.Output,
				ToolCallID: toolCall.ID,
			}
			contextHistory = append(contextHistory, observationMsg)
		}

		// // === 并行执行模式 ===
		// var wg sync.WaitGroup
		// wg.Add(len(responseMsg.ToolCalls))
		// var lock sync.Mutex
		// for _, toolCall := range responseMsg.ToolCalls {
		// 	go func(tc schema.ToolCall) {
		// 		defer wg.Done()

		// 		log.Printf("  -> 🛠️ 执行工具: %s, 参数: %s\n", tc.Name, string(tc.Arguments))

		// 		// 并行执行工具调用，并将结果存入 results map
		// 		result := e.registry.Execute(ctx, tc)
		// 		if result.IsError {
		// 			log.Printf("  -> ❌ 工具执行报错: %s\n", result.Output)
		// 		} else {
		// 			log.Printf("  -> ✅ 工具执行成功 (返回 %d 字节)\n", len(result.Output))
		// 		}

		// 		observationMsg := schema.Message{
		// 			Role:       schema.RoleUser,
		// 			Content:    result.Output,
		// 			ToolCallID: tc.ID,
		// 		}

		// 		// 将 Observation 追加到上下文历史中
		// 		lock.Lock()
		// 		contextHistory = append(contextHistory, observationMsg)
		// 		lock.Unlock()
		// 	}(toolCall)
		// }
		// wg.Wait()

		// 循环回到开头，模型将带着新加入的 Observation 继续它的下一轮思考...
	}

	return nil
}
