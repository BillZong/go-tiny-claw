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

	// 1. 初始化真实的 Provider大脑
	// 这里你可以任意切换 openai 或 claude，效果完全一致！
	llmProvider, err := provider.NewMiniMaxOpenAIProvider("MiniMax-M3")
	if err != nil {
		log.Fatalf("报错了: %v", err)
	}

	reporter := engine.NewTerminalReporter()

	readOnlyRegistry := tools.NewRegistry()
	if err := registerReadOnlyTools(readOnlyRegistry, workDir); err != nil {
		log.Fatalf("注册只读工具失败: %v", err)
	}

	mainRegistry := tools.NewRegistry()
	if err = registerTools(mainRegistry, workDir); err != nil {
		log.Fatalf("注册主工具失败: %v", err)
	}

	// 启动主引擎，关闭 Plan 模式
	eng := engine.NewAgentEngine(llmProvider, mainRegistry, false, false)

	// 【核心装配】：将带有 Engine 引用和只读 Registry 的 Subagent 工具注册进主线
	mainRegistry.Register(tools.NewSubagentTool(eng, readOnlyRegistry, reporter))

	sessionID := "test_subagent_001"
	sess := engine.GlobalSessionMgr.GetOrCreate(sessionID, workDir)

	prompt := `
我需要你在这个遗留项目里，找到那个”核心密码“。
为了防止污染上下文，请你务必派出子智能体 (spawn_subagent) 去执行探索任务。
你可以让子智能体使用 bash 去查找当前目录（及其所有子目录）下名为 config.txt 的文件.
子智能体拿到密码向你汇报后，请你亲自使用 write_file 工具，将密码写在根目录的 answer.txt 里。
`
	log.Println("\n>>> 🚀 启动多智能体协同测试...")
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
