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

	// Execute 实际执行模型请求的工具，并返回结果
	Execute(ctx context.Context, call schema.ToolCall) schema.ToolResult
}

type registryImpl struct {
	tools map[string]BaseTool
}

func NewRegistry() Registry {
	return &registryImpl{
		tools: make(map[string]BaseTool),
	}
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
	}
	// 1. 路由查找：如果在注册表中找不到该工具，这是模型产生了缓解，直接向模型抛出错误
	tool, exists := r.tools[call.Name]
	if !exists {
		result.IsError = true
		result.Output = fmt.Sprintf("Error: 系统重不存在名为 '%s' 的工具。", call.Name)
		return result
	}

	// 2. 执行工具逻辑：将原始的 JSON 字节流直接丢给具体工具
	output, err := tool.Execute(ctx, call.Arguments)

	// 3. 封装结果：将执行结果或底层物理错误封装后返回给 Main Loop
	result.Output = output
	if err != nil {
		result.IsError = true
	}

	return result
}
