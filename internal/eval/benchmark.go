// internal/eval/benchmark.go
// 自动化 Benchmark 跑分框架：用 Test-Driven Evaluation 把 Agent 从"玄学测试"拉入软件工程范畴。
//
// 设计要点：
//  1. TestCase 把任务拆成"靶机准备 + 任务指令 + 客观验收"三段，用 ValidateScript 的 exit code 作为最终断言。
//  2. 每次跑分都创建独立的沙箱目录 + 独立 Session + 独立引擎实例，保证用例间零干扰。
//  3. 复用现有 observability.CostTracker 与 Trace 机制，自动沉淀成本与决策树，供后续分析。
package eval

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"time"

	"github.com/BillZong/go-tiny-claw/internal/engine"
	"github.com/BillZong/go-tiny-claw/internal/observability"
	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/schema"
	"github.com/BillZong/go-tiny-claw/internal/tools"
)

// TestCase 定义了一个需要 Agent 去完成并验证的独立任务。
//
// 一次跑分的工作流：
//
//	SetupScript  -> 初始化靶机 (写入 bug、生成测试文件等)
//	TaskPrompt   -> 喂给 Agent 的自然语言指令
//	ValidateScript -> 验收 Agent 是否真把物理世界改对了 (exit 0 = 通过)
type TestCase struct {
	ID             string // 用例唯一标识
	Name           string // 用例名称
	SetupScript    string // 【可选】在 Agent 运行前执行的 bash 脚本（用于初始化靶机代码）
	TaskPrompt     string // 发送给 Agent 的任务指令
	ValidateScript string // 【核心】Agent 跑完后执行的 bash 验收脚本，exit 0 视为成功
	MaxTurns       int    // 允许 Agent 尝试的最大轮数（超时算失败；当前实现仅作预留位）
}

// TestResult 存放单次跑分结果。
// 字段命名遵循项目惯例：使用 CNY 与项目 Session 中的 TotalCostCNY 保持一致。
type TestResult struct {
	TestCaseID   string
	Passed       bool
	TotalCostCNY float64
	DurationMs   int64
	ErrorMsg     string
}

// BenchmarkRunner 负责调度一组 TestCase，按顺序跑分并汇总报告。
type BenchmarkRunner struct {
	modelName string
}

// NewBenchmarkRunner 构造一个跑分器。modelName 用于初始化 LLM Provider 与计费模型。
func NewBenchmarkRunner(model string) *BenchmarkRunner {
	return &BenchmarkRunner{modelName: model}
}

// RunSuite 顺序执行一组评测集，并在结束后打印汇总报告。
func (b *BenchmarkRunner) RunSuite(ctx context.Context, testcases []TestCase) {
	log.Println("==================================================")
	log.Printf("🚀 启动自动化 Harness Benchmark 评估... | 模型: %s\n", b.modelName)
	log.Println("==================================================")

	results := make([]TestResult, 0, len(testcases))
	passedCount := 0
	totalCost := 0.0

	for _, tc := range testcases {
		log.Printf("\n>>> ⏳ 正在执行用例 [%s]: %s\n", tc.ID, tc.Name)
		res := b.runSingleTest(ctx, tc)
		results = append(results, res)
		if res.Passed {
			passedCount++
			log.Printf(">>> ✅ 用例 [%s] 测试通过! | 耗时: %dms | 花费: ¥%.6f\n", tc.ID, res.DurationMs, res.TotalCostCNY)
		} else {
			log.Printf(">>> ❌ 用例 [%s] 测试失败! | 错误: %s\n", tc.ID, res.ErrorMsg)
		}
		totalCost += res.TotalCostCNY
	}

	// 终极报表
	log.Println("\n================ 🏆 跑分终极报告 ================")
	if len(testcases) > 0 {
		log.Printf("总用例数: %d | 成功数: %d | 成功率: %.2f%%\n",
			len(testcases), passedCount, float64(passedCount)/float64(len(testcases))*100)
	} else {
		log.Printf("总用例数: 0 (没有可跑的 TestCase)\n")
	}
	log.Printf("总消耗成本: ¥%.6f\n", totalCost)
	log.Println("==================================================")
}

// runSingleTest 执行单个 TestCase，按"靶机 -> 跑 Agent -> 验收"三步走。
func (b *BenchmarkRunner) runSingleTest(ctx context.Context, tc TestCase) TestResult {
	startTime := time.Now()

	// 1. 为每个用例创建绝对干净的沙箱目录 (物理隔离)
	workDir, _ := os.Getwd()
	workDir = fmt.Sprintf("%s/workspace/%s_%d", workDir, tc.ID, time.Now().Unix())
	if err := os.MkdirAll(workDir, 0o755); err != nil {
		return TestResult{
			TestCaseID: tc.ID,
			Passed:     false,
			ErrorMsg:   fmt.Sprintf("沙箱创建失败: %v", err),
		}
	}

	// 2. (可选) 执行 Setup 脚本准备靶机代码
	if tc.SetupScript != "" {
		cmd := exec.Command("bash", "-c", tc.SetupScript)
		cmd.Dir = workDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return TestResult{
				TestCaseID: tc.ID,
				Passed:     false,
				ErrorMsg:   fmt.Sprintf("靶机 Setup 失败: %v | 输出: %s", err, string(out)),
			}
		}
	}

	// 3. 组装具备打点能力 (CostTracker) 的引擎
	realProvider, err := provider.NewMiniMaxOpenAIProvider(b.modelName)
	if err != nil {
		return TestResult{
			TestCaseID: tc.ID,
			Passed:     false,
			ErrorMsg:   fmt.Sprintf("Provider 初始化失败: %v", err),
		}
	}
	// 为本次跑分单独建一个 Session 记账（直接 new，不走 GlobalSessionMgr，避免被其他用例串味）
	session := engine.NewSession(tc.ID, workDir)
	trackedProvider := observability.NewCostTracker(realProvider, b.modelName, session)

	registry := tools.NewRegistry()
	if err := registerDefaultTools(registry, workDir); err != nil {
		return TestResult{
			TestCaseID: tc.ID,
			Passed:     false,
			ErrorMsg:   fmt.Sprintf("工具注册失败: %v", err),
		}
	}

	eng := engine.NewAgentEngine(trackedProvider, registry, false, false)

	// 4. 让 Agent 开始干活
	session.Append(schema.Message{Role: schema.RoleUser, Content: tc.TaskPrompt})
	// 传入 nil reporter 屏蔽普通日志，防止刷屏
	if err := eng.Run(ctx, session, nil); err != nil {
		return TestResult{
			TestCaseID:   tc.ID,
			Passed:       false,
			TotalCostCNY: session.TotalCostCNY,
			DurationMs:   time.Since(startTime).Milliseconds(),
			ErrorMsg:     fmt.Sprintf("Agent 崩溃: %v", err),
		}
	}

	// 5. 【核心断言】Agent 跑完了，我们来验收成果
	validateCmd := exec.Command("bash", "-c", tc.ValidateScript)
	validateCmd.Dir = workDir
	out, err := validateCmd.CombinedOutput()
	duration := time.Since(startTime).Milliseconds()
	if err != nil {
		return TestResult{
			TestCaseID:   tc.ID,
			Passed:       false,
			TotalCostCNY: session.TotalCostCNY,
			DurationMs:   duration,
			ErrorMsg:     fmt.Sprintf("验证脚本执行失败: %s", string(out)),
		}
	}

	return TestResult{
		TestCaseID:   tc.ID,
		Passed:       true,
		TotalCostCNY: session.TotalCostCNY,
		DurationMs:   duration,
	}
}

// registerDefaultTools 给 Benchmark 沙箱挂载默认工具集（读、写、Bash、模糊编辑）。
func registerDefaultTools(registry tools.Registry, workDir string) error {
	rt, err := tools.NewReadFileTool(workDir)
	if err != nil {
		return err
	}
	if err := registry.Register(rt); err != nil {
		return err
	}

	wt, err := tools.NewWriteFileTool(workDir)
	if err != nil {
		return err
	}
	if err := registry.Register(wt); err != nil {
		return err
	}

	bt, err := tools.NewBashTool(workDir)
	if err != nil {
		return err
	}
	if err := registry.Register(bt); err != nil {
		return err
	}

	et, err := tools.NewEditFileTool(workDir)
	if err != nil {
		return err
	}
	if err := registry.Register(et); err != nil {
		return err
	}

	return nil
}
