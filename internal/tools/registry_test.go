// internal/tools/registry_test.go
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"testing"

	"github.com/BillZong/go-tiny-claw/internal/schema"
)

// fakeTool 用于测试的最小 BaseTool 实现。
type fakeTool struct {
	name string
	def  schema.ToolDefinition
}

func (f *fakeTool) Name() string { return f.name }
func (f *fakeTool) Definition() schema.ToolDefinition {
	if f.def.Name == "" {
		return schema.ToolDefinition{Name: f.name}
	}
	return f.def
}
func (f *fakeTool) Execute(_ context.Context, _ json.RawMessage) (string, error) {
	return "", nil
}

// TestGetAvailableTools_CachesResult 验证多次调用 GetAvailableTools 返回的是同一个
// backing array（缓存命中，无新 alloc）。
func TestGetAvailableTools_CachesResult(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeTool{name: "bash"})

	first := r.GetAvailableTools()
	second := r.GetAvailableTools()
	third := r.GetAvailableTools()

	if len(first) != 1 || first[0].Name != "bash" {
		t.Fatalf("unexpected first result: %#v", first)
	}

	// 通过 reflect 比较 slice header 的 Data 指针，应当完全一致（同一个 backing array）
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(first, third) {
		t.Fatalf("cached result should be deep-equal across calls")
	}
	// 进一步：用 reflect.ValueOf 拿到底层 array 首地址
	p1 := reflect.ValueOf(first).Pointer()
	p2 := reflect.ValueOf(second).Pointer()
	if p1 != p2 {
		t.Fatalf("cached slice should share backing array: first=%x second=%x", p1, p2)
	}
}

// TestGetAvailableTools_RegisterInvalidatesCache 验证 Register 会清掉缓存，
// 下一次 GetAvailableTools 拿到新结果。
func TestGetAvailableTools_RegisterInvalidatesCache(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeTool{name: "bash"})

	_ = r.GetAvailableTools() // 触发缓存

	_ = r.Register(&fakeTool{name: "edit_file"})
	after := r.GetAvailableTools()

	if len(after) != 2 {
		t.Fatalf("expected 2 tools after Register, got %d (%v)", len(after), after)
	}

	names := map[string]bool{}
	for _, def := range after {
		names[def.Name] = true
	}
	if !names["bash"] || !names["edit_file"] {
		t.Fatalf("expected both bash and edit_file in result, got: %v", names)
	}
}

// TestGetAvailableTools_AllocsPerRun 量化缓存收益：第二次起的 GetAvailableTools
// 在已有缓存的情况下应该零 alloc。
func TestGetAvailableTools_AllocsPerRun(t *testing.T) {
	r := NewRegistry()
	for i := 0; i < 4; i++ {
		_ = r.Register(&fakeTool{name: fmt.Sprintf("tool-%d", i)})
	}
	// 触发首次构建
	_ = r.GetAvailableTools()

	allocs := testing.AllocsPerRun(1000, func() {
		_ = r.GetAvailableTools()
	})
	if allocs != 0 {
		t.Fatalf("expected 0 allocs on cached path, got %.2f", allocs)
	}
}

// TestGetAvailableTools_EmptyRegistry 验证空注册表返回空 slice（不是 nil panic）。
func TestGetAvailableTools_EmptyRegistry(t *testing.T) {
	r := NewRegistry()
	defs := r.GetAvailableTools()
	if len(defs) != 0 {
		t.Fatalf("expected empty result, got %d items", len(defs))
	}
}

// TestGetAvailableTools_ConcurrentReads 验证并发读缓存是安全的（不会 panic）。
// 注意：项目里 AgentEngine 是单 goroutine 调 GetAvailableTools，但 RunSub 这种子任务
// 可能并发启动，所以多读并发场景要保证安全。
func TestGetAvailableTools_ConcurrentReads(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeTool{name: "bash"})
	_ = r.GetAvailableTools()

	const readers = 32
	var wg sync.WaitGroup
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defs := r.GetAvailableTools()
			if len(defs) != 1 || defs[0].Name != "bash" {
				t.Errorf("unexpected result: %#v", defs)
			}
		}()
	}
	wg.Wait()
}

// TestGetAvailableTools_WithRealTool 集成测试：挂载真实工具（NewReadFileTool），
// 确认缓存路径也能正确序列化 tool 的 JSON schema。
func TestGetAvailableTools_WithRealTool(t *testing.T) {
	r := NewRegistry()
	rt, err := NewReadFileTool("/tmp")
	if err != nil {
		t.Fatalf("NewReadFileTool: %v", err)
	}
	if err := r.Register(rt); err != nil {
		t.Fatalf("Register: %v", err)
	}

	first := r.GetAvailableTools()
	second := r.GetAvailableTools()

	if len(first) != 1 || first[0].Name != "read_file" {
		t.Fatalf("unexpected: %#v", first)
	}
	if first[0].InputSchema == nil {
		t.Fatal("InputSchema should not be nil for read_file")
	}
	// 缓存命中时必须返回同一份 schema
	if !reflect.DeepEqual(first[0], second[0]) {
		t.Fatal("cached schema should be deep-equal")
	}
}
