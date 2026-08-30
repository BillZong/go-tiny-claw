// internal/engine/loop.go
package engine

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"

	ctxpkg "github.com/BillZong/go-tiny-claw/internal/context"
	"github.com/BillZong/go-tiny-claw/internal/observability"
	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/schema"
	"github.com/BillZong/go-tiny-claw/internal/tools"
)

type AgentEngine struct {
	provider       provider.LLMProvider
	registry       tools.Registry
	enableThinking bool
	planMode       bool
	compactor      *ctxpkg.Compactor
	recovery       *ctxpkg.RecoveryManager // 【新增】自愈管理器
	injector       *ReminderInjector
}

func NewAgentEngine(p provider.LLMProvider, r tools.Registry, enableThinking bool, planMode bool) *AgentEngine {
	return &AgentEngine{
		provider:       p,
		registry:       r,
		enableThinking: enableThinking,
		planMode:       planMode,
		compactor:      ctxpkg.NewCompactor(20000, 6),
		recovery:       ctxpkg.NewRecoveryManager(), // 初始化 Recovery
		injector:       NewReminderInector(),
	}
}

func (e *AgentEngine) Run(ctx context.Context, session *Session, reporter Reporter) error {
	log.Printf("[Engine] 唤醒会话 [%s]，锁定工作区: %s (PlanMode: %v)\n", session.ID, session.WorkDir, e.planMode)

	// 【埋点 1】开启 Root Span，记录整个任务的生命周期
	ctx, rootSpan := observability.StartSpan(ctx, "Agent.Run")
	rootSpan.AddAttribute("SessionID", session.ID)
	rootSpan.AddAttribute("WorkDir", session.WorkDir)

	// defer 保证引擎退出时（无论成功失败）都能结束根 Span 并落盘 Trace
	defer func() {
		rootSpan.EndSpan()
		if err := observability.ExportTraceToFile(rootSpan, session.WorkDir, session.ID); err != nil {
			log.Printf("[Tracing] ⚠️ Trace 导出失败: %v\n", err)
		} else {
			log.Printf("📊 [Tracing] 本次任务的执行回放链路已保存至工作区的 .claw/traces 目录下\n")
		}
	}()

	composer := ctxpkg.NewPromptComposer(session.WorkDir, e.planMode)
	systemMsg := composer.Build()
	turnCount := 0

	for {
		turnCount++
		// 【埋点 2】记录单次 Turn 循环
		// 注意：defer turnSpan.EndSpan() 不能直接放在 for 体内，会被累积到 Run 退出时才执行。
		// 因此将 Turn 主体逻辑抽到独立方法 runOneTurn，让 defer 落在函数作用域。
		turnCtx, turnSpan := observability.StartSpan(ctx, fmt.Sprintf("Turn-%d", turnCount))

		shouldStop, err := e.runOneTurn(turnCtx, turnSpan, session, reporter, systemMsg)
		if err != nil {
			return err
		}
		if shouldStop {
			break
		}
	}

	return nil
}

// runOneTurn 跑一轮完整的 ReAct 循环。
// 返回 (shouldStop, err)：
//   - shouldStop=true 表示模型已输出最终答复、主循环应该 break。
//   - err 非空表示发生不可恢复错误，由 Run 决定如何处理。
//
// turnSpan.EndSpan() 在此函数内通过 defer 安全执行，避开了 for 循环中 defer 累积的经典坑。
func (e *AgentEngine) runOneTurn(
	ctx context.Context,
	turnSpan *observability.Span,
	session *Session,
	reporter Reporter,
	systemMsg schema.Message,
) (bool, error) {
	defer turnSpan.EndSpan()

	availableTools := e.registry.GetAvailableTools()
	workingMemory := session.GetWorkingMemory(20)

	var contextHistory []schema.Message
	contextHistory = append(contextHistory, systemMsg)
	contextHistory = append(contextHistory, workingMemory...)
	compactedContext := e.compactor.Compact(contextHistory)

	// 记录发给模型的实际上下文大小，非常有助于排查幻觉
	turnSpan.AddAttribute("context_message_count", len(compactedContext))

	var currentTurnThinkingContent string

	// ================= Phase 1: Thinking =================
	if e.enableThinking {
		if reporter != nil {
			reporter.OnThinking(ctx)
		}

		// 【埋点 3】记录 Thinking 调用
		thinkCtx, thinkSpan := observability.StartSpan(ctx, "LLM.Thinking")
		thinkResp, err := e.provider.Generate(thinkCtx, compactedContext, nil)
		thinkSpan.EndSpan()
		if err != nil {
			return false, fmt.Errorf("Thinking 阶段失败: %w", err)
		}
		if thinkResp.Content != "" {
			currentTurnThinkingContent = thinkResp.Content
			compactedContext = append(compactedContext, *thinkResp)
		}
	}

	// ================= Phase 2: Action =================
	// 【埋点 4】记录 Action 调用
	actCtx, actSpan := observability.StartSpan(ctx, "LLM.Action")
	actionResp, err := e.provider.Generate(actCtx, compactedContext, availableTools)
	actSpan.EndSpan()
	if err != nil {
		return false, fmt.Errorf("Action 阶段失败: %w", err)
	}

	// (上一讲修复 1214 的关键代码：合并为合法的单条 Assistant 消息)
	finalAssistantMsg := schema.Message{
		Role:      schema.RoleAssistant,
		Content:   strings.TrimSpace(currentTurnThinkingContent + "\n" + actionResp.Content),
		ToolCalls: actionResp.ToolCalls,
	}
	session.Append(finalAssistantMsg)

	if actionResp.Content != "" && reporter != nil {
		reporter.OnMessage(ctx, actionResp.Content)
	}

	if len(actionResp.ToolCalls) == 0 {
		return true, nil // 模型决定收尾，主循环 break
	}

	// ================= 执行工具并注入自愈模板 =================
	observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
	var wg sync.WaitGroup

	// 用于收集本轮执行的最后一个工具，供 Reminder 探测器分析
	// (在真实的工业级架构中，如果并发调用了多个工具，我们可以逐个分析或仅分析报错的那个。这里简化为取第一个)
	var lastToolCall schema.ToolCall
	var lastToolResult schema.ToolResult

	for i, toolCall := range actionResp.ToolCalls {
		wg.Add(1)

		go func(idx int, call schema.ToolCall) {
			defer wg.Done()

			if reporter != nil {
				reporter.OnToolCall(ctx, call.Name, string(call.Arguments))
			}

			// 底层物理执行工具
			// 此时传给 Registry 的 ctx 是带有当前 Turn 上下文的，
			// 由于是并发执行，多个工具的 Span 会平行地挂在 Turn 节点下。
			result := e.registry.Execute(ctx, call)

			// 【核心拦截与注入】
			finalOutput := result.Output
			if result.IsError {
				// 发生错误，交由 RecoveryManager 诊断并注入“锦囊妙计”
				finalOutput = e.recovery.AnalyzeAndInject(call.Name, result.Output)
				log.Printf("  -> [Go-%d] ❌ 注入救援指南: %s\n", idx, finalOutput)
			} else {
				log.Printf("  -> [Go-%d] ✅ 工具执行成功 (返回 %d 字节)\n", idx, len(result.Output))
			}

			if reporter != nil {
				displayOutput := finalOutput
				if len(displayOutput) > 200 {
					displayOutput = displayOutput[:200] + "... (已截断)"
				}
				reporter.OnToolResult(ctx, call.Name, displayOutput, result.IsError)
			}

			// 将注入过 Recovery Hint 的最终结果写入上下文历史
			observationMsgs[idx] = schema.Message{
				Role:       schema.RoleUser,
				Content:    finalOutput,
				ToolCallID: call.ID,
			}

			if i == 0 {
				lastToolCall = call
				lastToolResult = result
			}
		}(i, toolCall)
	}

	wg.Wait()

	// 1. 先将普通的工具执行结果存入 Session
	session.Append(observationMsgs...)

	// 2. 【核心防线】：在准备进入下一轮之前，进行死循环探测！
	reminderMsg := e.injector.CheckAndInject(lastToolCall, lastToolResult)
	if reminderMsg != nil {
		// 如果触发了干预规则，将这条严厉的提醒作为 User 消息，强制追加到 Session 的最末尾！
		// 大模型在下一轮被唤醒时，第一眼就会看到这句话，从而打破局部执念。
		session.Append(*reminderMsg)
	}

	return false, nil // 继续下一轮
}

// RunSub 是专为 Subagent 拉起的一次性受限循环。它不依赖外部 Session，打完就跑。
// Reporter：为了让用户在终端看到子智能体的工作轨迹，我们将主线程的 Reporter 透传进来，并打上特殊标记。
func (e *AgentEngine) RunSub(ctx context.Context, taskPrompt string, readOnlyRegistry tools.Registry, reporter any) (string, error) {
	// 子智能体极其容易偷懒。我们必须在 System Prompt 中严厉警告它必须使用工具
	contextHistory := []schema.Message{
		{
			Role: schema.RoleSystem,
			Content: `你是一个专门负责深度探索的探路者 (Explorer Subagent)。
你的任务是根据主架构师的指令，在当前工作区内仔细阅读代码、查阅日志，搜集足够的信息。

【核心纪律】
1. 你必须、且只能依靠内置工具（如 bash 的 find/grep，或 read_file）去寻找答案。绝对不允许凭空捏造或猜测！
2. 如果你没有找到确切的答案，你必须继续使用工具深入搜索。
3. 当且仅当你找到了确切的线索后，停止调用工具，直接输出一段纯文本作为你的终极汇报。主架构师会根据你的汇报来做下一步决策。`,
		},
		{
			Role:    schema.RoleUser,
			Content: taskPrompt,
		},
	}

	// 限制子智能体最多只能跑 10 个 Turn，防止它自己卡死
	const maxSubTurns = 10
	turnCount := 0

	for {
		turnCount++
		if turnCount > maxSubTurns {
			return "", fmt.Errorf("")
		}

		// 子智能体只能获取传入的只读工具注册表
		avaiableTools := readOnlyRegistry.GetAvailableTools()

		// 压缩上下文
		compactedContext := e.compactor.Compact(contextHistory)

		actionResp, err := e.provider.Generate(ctx, compactedContext, avaiableTools)
		if err != nil {
			return "", fmt.Errorf("子智能体推理失败: %w", err)
		}

		contextHistory = append(contextHistory, *actionResp)

		if len(actionResp.ToolCalls) == 0 {
			return actionResp.Content, nil
		}

		// 执行只读工具的并发循环
		observationMsgs := make([]schema.Message, len(actionResp.ToolCalls))
		var wg sync.WaitGroup

		for i, call := range actionResp.ToolCalls {
			wg.Add(1)
			go func(i int, toolCall schema.ToolCall) {
				defer wg.Done()

				result := readOnlyRegistry.Execute(ctx, toolCall)

				finalOutput := result.Output
				if result.IsError {
					finalOutput = e.recovery.AnalyzeAndInject(toolCall.Name, result.Output)
				}

				// 可视化：仅当 reporter 接口兼容时才转发事件；nil 或类型不匹配都不 panic
				if reporter != nil {
					if r, ok := reporter.(Reporter); ok {
						display := finalOutput
						if len(display) > 200 {
							display = display[:200] + "...（已截断）"
						}
						r.OnToolResult(ctx, fmt.Sprintf("[Subagent] %s", toolCall.Name), display, result.IsError)
					}
				}

				observationMsgs[i] = schema.Message{
					Role:       schema.RoleUser,
					Content:    finalOutput,
					ToolCallID: toolCall.ID,
				}
			}(i, call)
		}

		wg.Wait()
		contextHistory = append(contextHistory, observationMsgs...)
	}
}
