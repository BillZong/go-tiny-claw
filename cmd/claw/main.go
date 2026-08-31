// cmd/claw/main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/BillZong/go-tiny-claw/internal/engine"
	"github.com/BillZong/go-tiny-claw/internal/observability"
	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/schema"
	"github.com/BillZong/go-tiny-claw/internal/tools"
)

func main() {
	// 1. 命令行解析
	promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述")
	workDirPtr := flag.String("dir", ".", "Agent 运行的工作区目录路径 (默认为当前目录)")
	sessionPtr := flag.String("session", "cli_default_session", "指定会话 ID，支持断点续传")
	flag.Parse()

	// 确保已设置 MINIMAX_API_KEY
	if os.Getenv("MINIMAX_API_KEY") == "" {
		log.Fatal("请先导出 MINIMAX_API_KEY 环境变量")
	}

	if *promptPtr == "" {
		log.Fatalf("用法: go-tiny-claw -prompt \"你的任务描述\" [-dir /path/to/workdir] [-session session_id]")
	}

	// 解析工作区绝对路径
	workDir, err := filepath.Abs(*workDirPtr)
	if err != nil {
		log.Fatalf("解析工作区路径失败: %v", err)
	}

	fmt.Println("==================================================")
	fmt.Printf("🚀 启动 go-tiny-claw CLI 引擎...\n")
	fmt.Printf("📁 锁定工作区: %s\n", workDir)
	fmt.Println("==================================================")

	// 2. 初始化核心基础服务
	var realProvider provider.LLMProvider
	const modelName = "MiniMax-M3"
	realProvider, err = provider.NewMiniMaxOpenAIProvider(modelName)
	if err != nil {
		log.Fatalf("报错了: %v", err)
	}

	// 获取持久化 session
	sess := engine.GlobalSessionMgr.GetOrCreate(*sessionPtr, workDir)

	// cost tracker 包装器
	trackedProvider := observability.NewCostTracker(realProvider, modelName, sess)

	// 3. 初始化工具与执行层
	registry := tools.NewRegistry()
	if err = registerTools(registry, workDir); err != nil {
		log.Fatalf("注册主工具失败: %v", err)
	}

	// 在 cli 模式下，我们默认开启 YOLO 模式（全权信任本地执行），因此这里暂时不挂载 Feishu 审批 Middleware。

	// 4. 初始化核心引擎（组装器内部会自动加载 Composer, Compactor, Recovery, Reminder）
	// 开启plan模式，暂不开启思考模式
	eng := engine.NewAgentEngine(trackedProvider, registry, false, true)

	// 启动链路追踪
	ctx, rootSpan := observability.StartSpan(context.Background(), "CLI.TaskRun")
	rootSpan.AddAttribute("Prompt", *promptPtr)
	defer func() {
		rootSpan.EndSpan()
		_ = observability.ExportTraceToFile(rootSpan, workDir, sess.ID)
	}()

	// 5. 初始化彩色终端输出器
	reporter := engine.NewTerminalReporter()

	fmt.Printf("\n🎯 收到任务: %s\n\n", *promptPtr)

	// 将用户的 Prompt 压入 Session 记忆
	sess.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: *promptPtr,
	})

	// 启动 Main Loop
	err = eng.Run(ctx, sess, reporter)
	if err != nil {
		log.Fatalf("\n💥 引擎运行崩溃: %v", err)
	}

	fmt.Println("\n==================================================")
	fmt.Printf("✨ 任务圆满结束。总耗时: %v\n", time.Since(rootSpan.StartTime))
	fmt.Printf("💰 Session 累计消耗: ￥%.6f | Token: Input %d, Output %d\n", sess.TotalCostCNY, sess.TotalPromptTokens, sess.TotalCompletionTokens)
	fmt.Println("==================================================")
}

func registerReadOnlyTools(registry tools.Registry, workDir string) error {
	rt, err := tools.NewReadFileTool(workDir)
	if err != nil {
		return err
	}
	if err := registry.Register(rt); err != nil {
		return err
	}

	bt, err := tools.NewBashTool(workDir)
	if err != nil {
		return err
	}
	if err := registry.Register(bt); err != nil {
		return err
	}

	return nil
}

func registerTools(registry tools.Registry, workDir string) error {
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
