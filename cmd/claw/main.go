// cmd/claw/main.go
package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/BillZong/go-tiny-claw/internal/engine"
	"github.com/BillZong/go-tiny-claw/internal/feishu"
	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/schema"
	"github.com/BillZong/go-tiny-claw/internal/tools"
	"github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
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

	registry := tools.NewRegistry()
	if err = registerTools(registry, workDir); err != nil {
		log.Fatalf("注册工具失败: %v", err)
	}

	// 关闭 Plan 模式，让它在死胡同里专注地展示挣扎过程
	eng := engine.NewAgentEngine(llmProvider, registry, false, false)

	// 假设一个bot绑定一个session
	sessionID := "test_command_intercept_001"
	sess := engine.GlobalSessionMgr.GetOrCreate(sessionID, workDir)
	sess.Append(schema.Message{Role: schema.RoleUser, Content: ""})

	bot, err := feishu.NewFeishuBot(eng, sess)
	if err != nil {
		log.Fatalf("bot 创建失败: %v", err)
	}
	dispatcher, err := bot.GetEventDispatcher()
	if err != nil {
		log.Fatalf("bot 获取事件分发器失败: %v", err)
	}
	handler := httpserverext.NewEventHandlerFunc(dispatcher)

	// 注册安全拦截 Middleware
	registry.Use(func(ctx context.Context, call schema.ToolCall) (allowed bool, rejectReason string) {
		argsStr := string(call.Arguments)

		// 检查是否命中高危特征库
		if feishu.IsDangerousCommand(call.Name, argsStr) {
			taskID := call.ID

			// 如果命中，挂起当前协程，发送消息给飞书，等审批
			allowed, reason := feishu.GlobalApprovalMgr.WaitForApproval(ctx, taskID, call.Name, argsStr, bot.Reporter())

			if !allowed {
				return false, reason
			}
		}

		// 没命中黑名单，直接 YOLO 放行
		return true, ""
	})

	// 注册路由并启动 http 服务
	http.HandleFunc("/webhook/event", handler)

	port := ":18080"
	log.Printf("🚀 go-tiny-claw 飞书服务端已启动，正在监听 %s 端口\n", port)

	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("go-tiny-claw 服务启动失败: %s", err)
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
