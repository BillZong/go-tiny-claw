// cmd/claw/main.go
package main

import (
	"context"
	"flag"
	"log"
	"os"

	"github.com/BillZong/go-tiny-claw/internal/engine"
	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/schema"
	"github.com/BillZong/go-tiny-claw/internal/tools"
)

func main() {
	// 通过命令行参数接收用户的 promptPtr
	promptPtr := flag.String("prompt", "", "要交给 Agent 执行的任务描述")
	flag.Parse()

	if *promptPtr == "" {
		log.Println("请提供一个 prompt 参数，或者使用 -prompt 参数指定一个 prompt")
		os.Exit(1)
	}

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

	// 2. 注入工具注册表
	registry := tools.NewRegistry()
	if err = registerTools(registry, workDir); err != nil {
		log.Fatalf("注册工具失败: %v", err)
	}

	// 3. 实例化并运行引擎，开启 EnableThinking = true (开启慢思考阶段，促使模型一次性统筹规划)
	eng := engine.NewAgentEngine(llmProvider, registry, false, true)
	// 注入新实现的终端输出器
	reporter := engine.NewTerminalReporter()

	sessionID := "task_web_server_01"
	sess := engine.GlobalSessionMgr.GetOrCreate(sessionID, workDir)

	log.Printf("\n>>> 🚀 收到指令: %s\n", *promptPtr)

	// 将用户的 Prompt 压入 Session
	sess.Append(schema.Message{Role: schema.RoleUser, Content: *promptPtr})

	if err := eng.Run(context.Background(), sess, reporter); err != nil {
		log.Fatalf("引擎运行崩溃: %v\n", err)
	}
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
