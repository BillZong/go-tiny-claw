package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"sync"

	"github.com/BillZong/go-tiny-claw/internal/observability"
	"github.com/BillZong/go-tiny-claw/internal/schema"
)

type BaseTool interface {
	Name() string
	Definition() schema.ToolDefinition
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry 定义了工具的注册与分发执行接口
type Registry interface {
	// 挂载工具到系统中
	Register(tool BaseTool) error

	// GetAvailableTools 返回当前系统挂载的所有可用工具的 Schema
	GetAvailableTools() []schema.ToolDefinition

	Use(mw MiddlewareFunc)

	// Execute 实际执行模型请求的工具，并返回结果
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

// MiddlewareFunc 定义了中间件的签名。
// 它接收当前的 ToolCall，并返回一个是否允许执行的布尔值 (allowed)，以及拦截时的原因 (rejectReason)。
type MiddlewareFunc func(ctx context.Context, call schema.ToolCall) (allowed bool, rejectReason string)

type registryImpl struct {
	tools       map[string]BaseTool
	middlewares []MiddlewareFunc

	// cachedDefs 缓存 GetAvailableTools 的结果。注册新工具时清空 (lazy recompute on next read)。
	// 在 Agent 的 Main Loop 中每 Turn 都会调一次 GetAvailableTools，而在典型生命周期里工具集
	// 一开始就 Register 完毕后再不变更，所以缓存命中率接近 100%，可以省掉每 Turn 的 slice + map alloc。
	cachedDefs []schema.ToolDefinition
	mu         sync.RWMutex
}

func NewRegistry() Registry {
	return &registryImpl{
		tools:       make(map[string]BaseTool),
		middlewares: make([]MiddlewareFunc, 0),
	}
}

func (r *registryImpl) Use(mw MiddlewareFunc) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.middlewares = append(r.middlewares, mw)
}

func (r *registryImpl) Register(tool BaseTool) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	name := tool.Name()
	if _, exist := r.tools[name]; exist {
		log.Printf("[Warning] 工具 '%s' 已经被注册，将被覆盖。\n", name)
	}
	r.tools[name] = tool
	r.cachedDefs = nil // 工具集变动，失效缓存
	log.Printf("[Registry] 成功挂载工具: %s\n", name)
	return nil
}

func (r *registryImpl) GetAvailableTools() []schema.ToolDefinition {
	// 缓存命中路径：纯读，RLock 即可，O(1) 返回。
	r.mu.RLock()
	if r.cachedDefs != nil {
		defs := r.cachedDefs
		r.mu.RUnlock()
		return defs
	}
	r.mu.RUnlock()

	// 缓存未命中：升级为写锁重建。
	// double-check：避免两个 goroutine 同时进入写锁区后重复构建（前者写好后后者应当直接复用）。
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cachedDefs != nil {
		return r.cachedDefs
	}
	// map 遍历顺序随机，但工具集是稳定的（启动后不再变），所以这里产生的顺序抖动无影响。
	// 若以后需要稳定顺序，可以先按 name 排序 keys 再生成。
	defs := make([]schema.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	r.cachedDefs = defs
	return defs
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	// 【埋点】开启工具执行的 Span，无论成功失败都通过 defer 保证 EndSpan 被调用
	ctx, span := observability.StartSpan(ctx, "Tool.Execute")
	span.AddAttribute("tool_name", call.Name)
	span.AddAttribute("arguments", string(call.Arguments))
	defer span.EndSpan()

	// 在持锁期间只读 maps / slices + 拷贝出局部引用，
	// 调底层 tool.Execute / mw 之前必须释放锁 —— 否则：
	//   1. 用户工具可能耗时很长，阻塞 Register
	//   2. 中间件可能再次回调本 Registry，形成潜在重入风险
	r.mu.RLock()
	tool, exists := r.tools[call.Name]
	middlewares := r.middlewares
	r.mu.RUnlock()

	result := schema.ToolResult{
		ToolCallID: call.ID,
		IsError:    true, // 默认有错误
	}
	// 1. 路由查找：如果在注册表中找不到该工具，这是模型产生了缓解，直接向模型抛出错误
	if !exists {
		span.AddAttribute("error", "tool_not_found")
		result.Output = fmt.Sprintf("Error: 系统重不存在名为 '%s' 的工具。", call.Name)
		return result
	}

	// 2. 【核心防御】在执行底层逻辑前，依次运行所有的 middleware
	for _, mw := range middlewares {
		allowed, reason := mw(ctx, call)
		if !allowed {
			span.AddAttribute("intercepted", true)
			span.AddAttribute("reject_reason", reason)
			log.Printf("[Registry] ⚠️ 工具 %s 被 Middleware 拦截: %s\n", call.Name, reason)
			result.Output = fmt.Sprintf("执行被系统拦截。原因: %s", reason)
			return result
		}
	}

	// 3. 执行工具逻辑：将原始的 JSON 字节流直接丢给具体工具
	output, err := tool.Execute(ctx, call.Arguments)
	if err != nil {
		span.AddAttribute("error", err.Error())
		result.Output = fmt.Sprintf("Error executing %s: %v", call.Name, err.Error())
		return result
	}

	// 3. 封装结果：将执行结果或底层物理错误封装后返回给 Main Loop
	result.Output = output
	result.IsError = false

	// 仅截取前 100 字符放入 Trace，防止 Trace 文件过度膨胀
	span.AddAttribute("output_preview", truncate(output, 100))
	return result
}

// truncate 截取字符串前 max 个字符，超出部分用 "..." 标识。常用于 Span 属性避免 Trace 过大。
func truncate(s string, max int) string {
	if len(s) > max {
		return s[:max] + "..."
	}
	return s
}
