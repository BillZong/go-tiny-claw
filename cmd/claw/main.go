// cmd/claw/main.go
package main

import (
	"context"
	"log"
	"os"

	"github.com/BillZong/go-tiny-claw/internal/engine"
	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/tools"
)

func main() {
	// 确保已设置 MINIMAX_API_KEY
	if os.Getenv("MINIMAX_API_KEY") == "" {
		log.Fatal("请先导出 MINIMAX_API_KEY 环境变量")
	}

	workDir, _ := os.Getwd()

	// 1. 初始化真实的 Provider大脑
	// 这里你可以任意切换 openai 或 claude，效果完全一致！
	llmProvider, err := provider.NewMiniMaxOpenAIProvider("MiniMax-M2.7")
	if err != nil {
		log.Fatalf("报错了: %v", err)
	}

	// 2. 注入工具注册表
	registry := tools.NewRegistry()
	if err = registerTools(registry, workDir); err != nil {
		log.Fatalf("注册工具失败: %v", err)
	}

	// 3. 实例化并运行引擎，开启 EnableThinking = true (开启慢思考阶段！)
	eng := engine.NewAgentEngine(llmProvider, registry, workDir, false)

	// 设定测试任务
	// 发起一个需要连贯物理动作的任务
	prompt := `
	在工作目录下编辑一个 server.go文件，内容如下：
package main

import "fmt"

func main() {
	// 启动服务器
	fmt.Println("Server is starting on port 8080...")

	// TODO: 增加鉴权逻辑
	if true {
		fmt.Println("No auth, everyone can access.")
	}
}

1. 保存好文件内容后，通知我验证，然后等待10秒钟。
2. 请帮我把里面 "TODO: 增加鉴权逻辑" 下面的那个 if 语句，整个替换为：
	if user == nil {
		fmt.Println("Forbidden!")
		return
	}
3. 编辑完成后，通知我验证，然后再等待10秒钟。
4. 删除 server.go 文件，并通知验证结果。
`
	err = eng.Run(context.Background(), prompt)
	if err != nil {
		log.Fatalf("引擎运行崩溃: %v", err)
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
