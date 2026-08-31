package main

import (
	"context"
	"log"
	"net/http"
	"os"

	"github.com/BillZong/go-tiny-claw/internal/engine"
	"github.com/BillZong/go-tiny-claw/internal/feishu"
	"github.com/BillZong/go-tiny-claw/internal/observability"
	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/schema"
	"github.com/BillZong/go-tiny-claw/internal/tools"
	"github.com/larksuite/oapi-sdk-go/v3/core/httpserverext"
)

func main() {
	log.Println("🚀 正在启动 go-tiny-claw AgentOps 飞书服务端...")

	if os.Getenv("MINIMAX_API_KEY") == "" || os.Getenv("FEISHU_APP_ID") == "" {
		log.Fatal("请先导出 MINIMAX_API_KEY 和 飞书相关的环境变量")
	}

	// 1. 设定监控的物理工作区
	workDir, _ := os.Getwd()
	workDir += "/workspace"
	if err := os.MkdirAll(workDir, 0755); err != nil {
		log.Fatalf("无法创建工作区: %v", err)
	}

	// 2. 初始化底层大脑与注册表
	const modelName = "MiniMax-M3"
	llmProvider, err := provider.NewMiniMaxOpenAIProvider(modelName)
	if err != nil {
		log.Fatalf("报错了: %v", err)
	}

	registry := tools.NewRegistry()
	if err = registerTools(registry, workDir); err != nil {
		log.Fatalf("注册主工具失败: %v", err)
	}

	// 3. 注入安全拦截 Middleware
	registry.Use(func(ctx context.Context, call schema.ToolCall) (allowed bool, rejectReason string) {
		argsStr := string(call.Arguments)

		// 检查是否命中危险命令黑名单
		if feishu.IsDangerousCommand(call.Name, argsStr) {
			taskID := call.ID
			log.Printf("[Middleware] 拦截到高危操作：%s, 触发飞书审批挂起...\n", call.Name)

			// 【驾驭魔术】：从 Context 中优雅地取出专属于发起该请求群聊的 Reporter！
			// 注意这里的强转，因为我们在 WaitForApproval 中需要调用 FeishuReporter 特有的 sendMsg。
			currentReporter, _ := feishu.ReporterFromContext(ctx).(*feishu.FeishuReporter)
			return feishu.GlobalApprovalMgr.WaitForApproval(ctx, taskID, call.Name, argsStr, currentReporter)
		}

		// 普通读取命令，YOLO 放行
		return true, ""
	})
	log.Println("🛡️ 安全防御 Middleware 已挂载。")

	// 4. 动态 Factory 组装器：保证高并发调用的物理独立性与账单准确追踪
	engineFactory := func(session *engine.Session) *engine.AgentEngine {
		// 让 Tracker 绑定当前特定用户的 Session 版本
		trackedProvider := observability.NewCostTracker(llmProvider, modelName, session)

		// 返回一个新组装的 Engine 实例
		return engine.NewAgentEngine(trackedProvider, registry, false, false)
	}

	// 5. 初始化飞书 Bot 调度中心
	bot, err := feishu.NewFeishuBotWithFactory(workDir, engineFactory)
	if err != nil {
		log.Fatalf("初始化飞书 Bot 调度中心失败: %v", err)
	}
	dispatcher, err := bot.GetEventDispatcher()
	if err != nil {
		log.Fatalf("获取飞书 Bot 调度中心失败: %v", err)
	}
	handler := httpserverext.NewEventHandlerFunc(dispatcher)

	// 6. 注册 Webhook 路由并启动 HTTP Server
	http.HandleFunc("/webhook/event", handler)

	port := ":28080"
	log.Printf("📡 Webhook 服务已启动，正在监听端口 %s，请配置 ngrok...\n", port)

	if err := http.ListenAndServe(port, nil); err != nil {
		log.Fatalf("HTTP Server 启动失败: %v", err)
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

	et, err := tools.NewEditFileTool(workDir)
	if err != nil {
		return err
	}
	if err := registry.Register(et); err != nil {
		return err
	}

	return nil
}
