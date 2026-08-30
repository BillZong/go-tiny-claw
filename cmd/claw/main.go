// cmd/claw/main.go
package main

import (
	"context"
	"log"
	"os"

	"github.com/BillZong/go-tiny-claw/internal/engine"
	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/schema"
	"github.com/BillZong/go-tiny-claw/internal/tools"
)

func main() {
	// 确保已设置 MINIMAX_API_KEY
	if os.Getenv("MINIMAX_API_KEY") == "" {
		log.Fatal("请先导出 MINIMAX_API_KEY 环境变量")
	}

	workDir, _ := os.Getwd()
	workDir += "/workspace"

	const modelName = "MiniMax-M3"

	// 1. 初始化真实的 Provider大脑
	// 这里你可以任意切换 openai 或 claude，效果完全一致！
	llmProvider, err := provider.NewMiniMaxOpenAIProvider(modelName)
	if err != nil {
		log.Fatalf("报错了: %v", err)
	}

	mainRegistry := tools.NewRegistry()
	if err = registerTools(mainRegistry, workDir); err != nil {
		log.Fatalf("注册主工具失败: %v", err)
	}

	// 启动主引擎，关闭 Plan 模式
	eng := engine.NewAgentEngine(llmProvider, mainRegistry, false, false)
	reporter := engine.NewTerminalReporter()
	sess := engine.GlobalSessionMgr.GetOrCreate("test_trace_001", workDir)

	// 触发一个跨工具类型的并发任务，便于在 Trace JSON 中观察到 Tool.Execute 节点平行挂在 Turn 节点下
	prompt := `为了加快执行速度，请你在一轮回复中，【同时并行】完成以下两件事：
1. 使用 bash 工具执行 'sleep 2 && echo "系统环境检查完毕"'
2. 使用 write_file 工具，在当前目录下创建一个 'trace_test.md'，内容写上 "测试并发的写入"。
请确保你是分别调用两个不同的工具，不要试图把它们合并成一个命令！`

	log.Println("\n>>> 🚀 启动带 Tracing 链路追踪的测试...")
	sess.Append(schema.Message{Role: schema.RoleUser, Content: prompt})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
	}
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

	et, err := tools.NewEditFileToll(workDir)
	if err != nil {
		return err
	}
	if err := registry.Register(et); err != nil {
		return err
	}

	return nil
}
