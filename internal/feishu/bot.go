package feishu

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/BillZong/go-tiny-claw/internal/engine"
	"github.com/BillZong/go-tiny-claw/internal/schema"
	lark "github.com/larksuite/oapi-sdk-go/v3"
	"github.com/larksuite/oapi-sdk-go/v3/event/dispatcher"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
)

// ==========================================
// 1. Context 传递机制：解决并发 Reporter 的提取
// ==========================================

// context 中存放 Reporter 的专属键
type reporterKey struct{}

// 将专属的 Reporter 存入 context 中
func ContextWithReporter(ctx context.Context, r engine.Reporter) context.Context {
	return context.WithValue(ctx, reporterKey{}, r)
}

// 供底层的 Middleware 提取专属的 Reporter 发送审批卡片
func ReporterFromContext(ctx context.Context) engine.Reporter {
	if r, ok := ctx.Value(reporterKey{}).(engine.Reporter); ok {
		return r
	}
	return nil
}

// ==========================================
// 2.飞书 Bot 核心调度器
// ==========================================

// 允许每次收到消息时，根据 Session 动态创建引擎
type AgentEngineFactory func(session *engine.Session) *engine.AgentEngine

type FeishuBot struct {
	ctx       context.Context // 最顶层的上下文
	client    *lark.Client
	appID     string
	appSecret string
	workDir   string             // 保存从入口传来的工作区路径
	factory   AgentEngineFactory // 替换掉原来的单一 engine 引用
}

func NewFeishuBotWithFactory(workDir string, factory AgentEngineFactory) (*FeishuBot, error) {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")

	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("请先导出 FEISHU_APP_ID 和 FEISHU_APP_SECRET 环境变量")
	}

	client := lark.NewClient(appID, appSecret)

	return &FeishuBot{
		ctx:       context.Background(),
		client:    client,
		appID:     appID,
		appSecret: appSecret,
		workDir:   workDir,
		factory:   factory,
	}, nil
}

func (b *FeishuBot) GetEventDispatcher() (*dispatcher.EventDispatcher, error) {
	encryptKey := os.Getenv("FEISHU_ENCRYPT_KEY")
	verifyToken := os.Getenv("FEISHU_VERIFY_TOKEN")

	if verifyToken == "" {
		return nil, fmt.Errorf("请先导出 FEISHU_VERIFY_TOKEN 环境变量")
	}

	handler := dispatcher.NewEventDispatcher(verifyToken, encryptKey).OnP2MessageReceiveV1(func(ctx context.Context, event *larkim.P2MessageReceiveV1) error {
		// 忽略空消息
		if event.Event.Message == nil {
			log.Printf("[FeishuBot] 收到消息但 Message 为空，跳过\n")
			return nil
		}

		// 忽略机器人自己发送的消息，避免死循环
		if event.Event.Sender != nil && event.Event.Sender.SenderType != nil && *event.Event.Sender.SenderType == "bot" {
			return nil
		}

		// 飞书消息是 JSON，暂时粗略地提取文本内容
		contentStr := *event.Event.Message.Content
		contentStr = strings.TrimPrefix(contentStr, `{"text":"`)
		contentStr = strings.TrimSuffix(contentStr, `"}`)

		chatId := ""
		if event.Event.Message.ChatId != nil {
			chatId = *event.Event.Message.ChatId
		}
		log.Printf("[Feishu] 收到会话 %s 消息: %s\n", chatId, contentStr)

		// 拦截人工审批的特殊口令
		if strings.HasPrefix(contentStr, "approve ") {
			taskID := strings.TrimPrefix(contentStr, "approve ")
			taskID = strings.TrimSpace(taskID)
			// 唤醒挂起的引擎携程
			GlobalApprovalMgr.ResolveApproval(ctx, taskID, true, "管理员已批准操作")
			log.Printf("[Feishu] 会话 %s: ✅ 已为您批准任务 %s", chatId, taskID)
			return nil
		}
		if strings.HasPrefix(contentStr, "reject ") {
			taskID := strings.TrimPrefix(contentStr, "reject ")
			taskID = strings.TrimSpace(taskID)
			// 唤醒挂起的引擎携程，并反馈拒绝理由
			GlobalApprovalMgr.ResolveApproval(ctx, taskID, false, "管理员认为该操作存在极高风险，已无情拒绝")
			log.Printf("[Feishu] 会话 %s: 🚫 已拒绝任务 %s", chatId, taskID)
			return nil
		}

		// 如果不是审批命令，则是正常对话，启动一个新的 Agent 任务去处理
		go b.handleAgentRun(chatId, contentStr)

		return nil
	}).OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
		// 消息已读事件（v2 协议），静默忽略
		return nil
	}).OnP1MessageReadV1(func(ctx context.Context, event *larkim.P1MessageReadV1) error {
		// 消息已读事件（v1 协议，key 为 "message_read"），静默忽略
		// 不注册这个 handler 的话，dispatcher 会打 "event type: message_read, not found handler" 噪音日志
		return nil
	})

	return handler, nil
}

func (b *FeishuBot) handleAgentRun(chatId, prompt string) {
	// 为当前并发请求实例化一个专属的 Reporter
	reporter := &FeishuReporter{
		client: b.client,
		chatId: chatId,
	}

	// 1. 获取物理隔离的 session
	sess := engine.GlobalSessionMgr.GetOrCreate(chatId, b.workDir)
	sess.Append(schema.Message{
		Role:    schema.RoleUser,
		Content: prompt,
	})

	// 2. 通过工厂模式，为当前会话生成一个挂好了专属 CostTracker 的新引擎
	engine := b.factory(sess)

	// 3. 将专属的 reporter 放入 Context 并传给引擎
	runCtx := ContextWithReporter(b.ctx, reporter)

	if err := engine.Run(runCtx, sess, reporter); err != nil {
		reporter.sendMsg(runCtx, fmt.Sprintf("❌ Agent 运行崩溃: %v", err))
	}
}

// ==========================================
// 3. 飞书 Reporter 实现 ()
// ==========================================

type FeishuReporter struct {
	client *lark.Client
	chatId string
}

func NewFeishuReporter(client *lark.Client, chatId string) engine.Reporter {
	return &FeishuReporter{
		client: client,
		chatId: chatId,
	}
}

func (r *FeishuReporter) sendMsg(ctx context.Context, text string) {
	textContent := map[string]string{"text": text}
	contentBytes, _ := json.Marshal(textContent)
	contentStr := string(contentBytes)

	msgReq := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.CreateMessageV1ReceiveIDTypeChatId).
		Body(larkim.NewCreateMessageReqBodyBuilder().
			ReceiveId(r.chatId).
			MsgType(larkim.MsgTypeText).
			Content(contentStr).
			Build()).
		Build()

	_, _ = r.client.Im.Message.Create(ctx, msgReq)
}

func (r *FeishuReporter) OnThinking(ctx context.Context) {
	r.sendMsg(ctx, "🤔 模型正在慢思考 (Thinking)...")
}

func (r *FeishuReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	r.sendMsg(ctx, fmt.Sprintf("🛠️ **正在执行工具**：`%s`\n参数：`%s`", toolName, args))
}

func (r *FeishuReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.sendMsg(ctx, fmt.Sprintf("⚠️ **执行报错** (%s)：\n%s", toolName, result))
	} else {
		r.sendMsg(ctx, fmt.Sprintf("✅ **执行成功** (%s)", toolName))
	}
}

func (r *FeishuReporter) OnMessage(ctx context.Context, content string) {
	r.sendMsg(ctx, content)
}
