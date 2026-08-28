package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"log"

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
}

func NewRegistry() Registry {
	return &registryImpl{
		tools:       make(map[string]BaseTool),
		middlewares: make([]MiddlewareFunc, 0),
	}
}

func (r *registryImpl) Use(mw MiddlewareFunc) {
	r.middlewares = append(r.middlewares, mw)
}

func (r *registryImpl) Register(tool BaseTool) error {
	name := tool.Name()
	if _, exist := r.tools[name]; exist {
		log.Printf("[Warning] 工具 '%s' 已经被注册，将被覆盖。\n", name)
	}
	r.tools[name] = tool
	log.Printf("[Registry] 成功挂载工具: %s\n", name)
	return nil
}

func (r *registryImpl) GetAvailableTools() []schema.ToolDefinition {
	var defs = make([]schema.ToolDefinition, 0, len(r.tools))
	for _, tool := range r.tools {
		defs = append(defs, tool.Definition())
	}
	return defs
}

func (r *registryImpl) Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult {
	result := schema.ToolResult{
		ToolCallID: call.ID,
		IsError:    true, // 默认有错误
	}
	// 1. 路由查找：如果在注册表中找不到该工具，这是模型产生了缓解，直接向模型抛出错误
	tool, exists := r.tools[call.Name]
	if !exists {
		result.Output = fmt.Sprintf("Error: 系统重不存在名为 '%s' 的工具。", call.Name)
		return result
	}

	// 2. 【核心防御】在执行底层逻辑前，依次运行所有的 middleware
	for _, mw := range r.middlewares {
		allowed, reason := mw(ctx, call)
		if !allowed {
			log.Printf("[Registry] ⚠️ 工具 %s 被 Middleware 拦截: %s\n", call.Name, reason)
			result.Output = fmt.Sprintf("执行被系统拦截。原因: %s", reason)
			return result
		}
	}

	// 3. 执行工具逻辑：将原始的 JSON 字节流直接丢给具体工具
	output, err := tool.Execute(ctx, call.Arguments)
	if err != nil {
		result.Output = fmt.Sprintf("Error executing %s: %v", call.Name, err.Error())
		return result
	}

	// 3. 封装结果：将执行结果或底层物理错误封装后返回给 Main Loop
	result.Output = output
	result.IsError = false

	return result
}
