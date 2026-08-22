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

type FeishuBot struct {
	client    *lark.Client
	appID     string
	appSecret string
	engine    *engine.AgentEngine
}

func NewFeishuBot(engine *engine.AgentEngine) (*FeishuBot, error) {
	appID := os.Getenv("FEISHU_APP_ID")
	appSecret := os.Getenv("FEISHU_APP_SECRET")

	if appID == "" || appSecret == "" {
		return nil, fmt.Errorf("请先导出 FEISHU_APP_ID 和 FEISHU_APP_SECRET 环境变量")
	}

	client := lark.NewClient(appID, appSecret)

	return &FeishuBot{
		client:    client,
		appID:     appID,
		appSecret: appSecret,
		engine:    engine,
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
		log.Printf("[FeishuBot] 收到会话 %s 消息：%s\n", chatId, contentStr)

		go b.handleAgentRun(chatId, contentStr)

		return nil
	}).OnP2MessageReadV1(func(ctx context.Context, event *larkim.P2MessageReadV1) error {
		// 消息已读时间，静默忽略（避免日志干扰）
		return nil
	})

	return handler, nil
}

func (b *FeishuBot) handleAgentRun(chatId, prompt string) {
	ctx := context.Background()
	reporter := NewFeishuReporter(b.client, chatId)
	workDir, _ := os.Getwd()
	session := engine.GlobalSessionMgr.GetOrCreate(chatId, workDir)
	session.Append(schema.Message{Role: schema.RoleUser, Content: prompt})
	if err := b.engine.Run(ctx, session, reporter); err != nil {
		reporter.OnMessage(ctx, fmt.Sprintf("❌ 执行出错：%v", err))
	}
}

type feishuReporter struct {
	client *lark.Client
	chatId string
}

func NewFeishuReporter(client *lark.Client, chatId string) engine.Reporter {
	return &feishuReporter{
		client: client,
		chatId: chatId,
	}
}

func (r *feishuReporter) sendMsg(ctx context.Context, text string) {
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

func (r *feishuReporter) OnThinking(ctx context.Context) {
	r.sendMsg(ctx, "🤔 模型正在慢思考 (Thinking)...")
}

func (r *feishuReporter) OnToolCall(ctx context.Context, toolName string, args string) {
	r.sendMsg(ctx, fmt.Sprintf("🛠️ **正在执行工具**：`%s`\n参数：`%s`", toolName, args))
}

func (r *feishuReporter) OnToolResult(ctx context.Context, toolName string, result string, isError bool) {
	if isError {
		r.sendMsg(ctx, fmt.Sprintf("⚠️ **执行报错** (%s)：\n%s", toolName, result))
	} else {
		r.sendMsg(ctx, fmt.Sprintf("✅ **执行成功** (%s)", toolName))
	}
}

func (r *feishuReporter) OnMessage(ctx context.Context, content string) {
	r.sendMsg(ctx, content)
}
