package observability

import (
	"context"
	"log"
	"time"

	"github.com/BillZong/go-tiny-claw/internal/engine"
	"github.com/BillZong/go-tiny-claw/internal/provider"
	"github.com/BillZong/go-tiny-claw/internal/schema"
)

// PricingModel 定义了不同大模型的计费标准 (单位: 人民币/1M Tokens)// 为了演示，这里硬编码了当前市面上几个主流模型的官方大致定价。
var PriceModel = map[string]struct {
	InputPrice  float64 // 每个 Token
	OutputPrice float64 // 每个 Token
}{
	"MiniMax-M3": {
		InputPrice:  0.0000042,
		OutputPrice: 0.0000168,
	},
	"MiniMax-M2.7": {
		InputPrice:  0.0000021,
		OutputPrice: 0.0000084,
	},
	"MiniMax-M2.7-highspeed": {
		InputPrice:  0.0000042,
		OutputPrice: 0.0000168,
	},
	"deepseek-v4-flash": {
		InputPrice:  0.000003,
		OutputPrice: 0.000009,
	},
	"deepseek-v4-pro": {
		InputPrice:  0.000009,
		OutputPrice: 0.000027,
	},
	"deepseek-v4-flash-vision-exp": {
		InputPrice:  0.000003,
		OutputPrice: 0.000009,
	},
}

type CostTracker struct {
	nextProvider provider.LLMProvider
	modelName    string
	session      *engine.Session // 当前所属的会话
}

// Generate 实现了 LLMProvider 接口！这意味着它可以被无缝注入到 Main Loop 中。
func NewCostTracker(next provider.LLMProvider, modelName string, session *engine.Session) provider.LLMProvider {
	return &CostTracker{
		nextProvider: next,
		modelName:    modelName,
		session:      session,
	}
}

func (t *CostTracker) Generate(ctx context.Context, messages []schema.Message, availableTools []schema.ToolDefinition) (*schema.Message, error) {
	// 记录请求发起的时间
	startTime := time.Now()

	// 调用真实的底层大模型去执行耗时的网络请求
	respMsg, err := t.nextProvider.Generate(ctx, messages, availableTools)

	// 计算耗时
	latency := time.Since(startTime)

	// 如果报错了，只打印时间，不计费
	if err != nil {
		log.Printf("[Tracker] ❌ API 调用失败，耗时: %v\n", latency)
		return respMsg, err
	}

	// 解析 Token 并计算成本
	if respMsg.Usage != nil {
		promptTokens := int(respMsg.Usage.PromptTokens)
		completionTokens := int(respMsg.Usage.CompletionTokens)

		var cost float64
		if price, exists := PriceModel[t.modelName]; exists {
			cost = float64(promptTokens)*price.InputPrice + float64(completionTokens)*price.OutputPrice
		}

		// 打印仪表盘日志
		log.Printf("[Tracker] 📊 API 调用完成 | 耗时: %v | 输入: %d tk | 输出: %d tk | 花费: ¥%.6f\n", latency, promptTokens, completionTokens, cost)

		// 将账单累加到当前的 session 中，供人类后续s随时查询
		if t.session != nil {
			t.session.RecordUsage(promptTokens, completionTokens, cost)
			log.Printf("[Tracker] 💰 当前会话 (%s) 累计花费: ¥%.6f\n", t.session.ID, t.session.TotalCostCNY)
		}
	} else {
		log.Printf("[Tracker] ⚠️ API 调用完成，但未返回 Usage 数据 | 耗时: %v\n", latency)
	}

	return respMsg, nil
}
