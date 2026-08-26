package engine

import (
	"context"
	"fmt"
	"log"
	"sync"

	ctxpkg "github.com/BillZong/go-tiny-claw/internal/context"
	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/schema"
	"github.com/BillZong/go-tiny-claw/internal/tools"
)

// AgentEngine 是微型 OS 的核心驱动
type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	enableThinking bool // EnableThinking (是否启用思考): 允许模型在执行工具前进行推理
	planMode       bool // PlanMode (是否启用计划模式): 允许模型在执行工具前进行推理
	compatcor      *ctxpkg.Compactor
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool, planMode bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		enableThinking: enableThinking,
		planMode:       planMode,
		// 【初始化压缩器】：为了便于今天的极端测试，我们将水位线阈值设积极（例如 3000 字符），
		// 并保护最近的 6 条消息（大约两轮 Turn 的交互）
		compatcor: ctxpkg.NewCompactor(40000, 6),
	}
}

// Run 启动 Agent 的生命周期
func (e *AgentEngine) Run(ctx context.Context, session *Session, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区: %s\n", session.ID, session.WorkDir)

	// 动态组装 system prompt
	composer := ctxpkg.NewPromptComposer(session.WorkDir, e.planMode)
	systemMsg := composer.Build()

	for {
		// 工具和上下文组装
		availableTools := e.registry.GetAvailableTools()
		workingMemory := session.GetWorkingMemory(20)

		var contextHistory []schema.Message
		contextHistory = append(contextHistory, systemMsg)
		contextHistory = append(contextHistory, workingMemory...)

		compactedContext := e.compatcor.Compact(contextHistory)

		// reasoning
		if e.enableThinking {
			if reporter != nil {
				reporter.OnThinking(ctx)
			}

			thinkResponse, err := e.provider.Generate(ctx, compactedContext, nil)
			if err != nil {
				return fmt.Errorf("Thinking 阶段生成失败: %w", err)
			}
			if thinkResponse.Content != "" {
				session.Append(*thinkResponse)
				compactedContext = append(compactedContext, *thinkResponse)
			}
		}

		// action
		actionResp, err := e.provider.Generate(ctx, compactedContext, availableTools)
		if err != nil {
			return fmt.Errorf("Action 阶段生成失败: %w", err)
		}

		// 【驾驭精髓】：注意，写入 Session（硬盘/全量内存）的永远是全量的真实响应，不受 Compact 影响！
		// Compact 只作用于本轮发给大模型的那个临时 Context。
		session.Append(*actionResp)
		compactedContext = append(compactedContext, *actionResp)

		if actionResp.Content != "" && reporter != nil {
			reporter.OnMessage(ctx, actionResp.Content)
		}

		if len(actionResp.ToolCalls) == 0 {
			break
		}

		// parallel tool calls
		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup

		for i, toolCall := range actionResp.ToolCalls {
			wg.Add(1)
			go func(i int, tc schema.ToolCall) {
				defer wg.Done()

				if reporter != nil {
					reporter.OnToolCall(ctx, tc.Name, string(tc.Arguments))
				}

				result := e.registry.Execute(ctx, tc)
				if reporter != nil {
					displayOutput := result.Output
					if len(displayOutput) > 200 {
						displayOutput = displayOutput[:200] + "...（已截断）"
					}
					reporter.OnToolResult(ctx, tc.Name, displayOutput, result.IsError)
				}

				observationMsgs[i] = schema.Message{
					Role:       schema.RoleUser,
					Content:    result.Output,
					ToolCallID: tc.ID,
				}
			}(i, toolCall)
		}

		wg.Wait()

		// aggregate observation
		session.Append(observationMsgs...)
	}

	return nil
}
