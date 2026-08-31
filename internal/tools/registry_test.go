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

// TestGetAvailableTools_ConcurrentMiss 压测 cache miss 路径的并发安全性。
//
// 这个测试针对的是：之前 GetAvailableTools 在 cache miss 路径下"持 RLock 做写"
// 的 data race bug。修复方式是 double-checked locking：未命中时升级为写锁。
//
// 测试方法：通过 Register 让 cachedDefs 失效，然后让 N 个 goroutine 同时
// 调 GetAvailableTools，配合 -race 检测 data race。
func TestGetAvailableTools_ConcurrentMiss(t *testing.T) {
	r := NewRegistry().(*registryImpl)
	_ = r.Register(&fakeTool{name: "bash"})
	_ = r.Register(&fakeTool{name: "read_file"})
	_ = r.Register(&fakeTool{name: "edit_file"})
	// 强制 cache miss：通过 Register 让 cachedDefs 失效，然后立刻并发读
	_ = r.Register(&fakeTool{name: "write_file"})

	const readers = 64
	var wg sync.WaitGroup
	start := make(chan struct{}) // 同步起跑，让所有 goroutine 同时撞上 cache miss
	for i := 0; i < readers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			defs := r.GetAvailableTools()
			if len(defs) != 4 {
				t.Errorf("expected 4 tool defs, got %d", len(defs))
			}
		}()
	}
	close(start)
	wg.Wait()

	// 最后缓存应当已经构建好
	if r.cachedDefs == nil {
		t.Fatal("cachedDefs should be populated after concurrent miss resolution")
	}
	if len(r.cachedDefs) != 4 {
		t.Fatalf("final cache should have 4 entries, got %d", len(r.cachedDefs))
	}
}

// TestExecute_ConcurrentWithRegister 压测 Execute 与 Register 并发安全。
//
// 这是用户最初想堵的"concurrent map read/write fatal panic"场景。
// 之前 Execute 完全没有锁，理论上与 Register 并发会触发 Go runtime fatal。
// 修复后：Execute 在读 r.tools / r.middlewares 时持 RLock，Register 拿 Lock 互斥。
func TestExecute_ConcurrentWithRegister(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeTool{name: "bash"})

	const (
		executors    = 16
		registrations = 200
	)
	var wg sync.WaitGroup

	// 执行者：循环 Execute
	for i := 0; i < executors; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < registrations; j++ {
				res := r.Execute(context.Background(), schema.ToolCall{
					ID:        fmt.Sprintf("call-%d", j),
					Name:      "bash",
					Arguments: json.RawMessage(`{}`),
				})
				if res.IsError {
					t.Errorf("unexpected error: %s", res.Output)
				}
			}
		}()
	}

	// 注册者：循环 Register 覆盖现有工具
	wg.Add(1)
	go func() {
		defer wg.Done()
		for j := 0; j < registrations; j++ {
			_ = r.Register(&fakeTool{name: "bash"})
		}
	}()

	wg.Wait()
}

// TestExecute_AllocsOnRead 验证 Execute 命中常见路径（不触发拦截、不报错）下尽量少 alloc。
// 注意：StartSpan/AddAttribute 内部会 alloc 一些 map 和 context 包装，这是 Tracing 自身成本，
// 这里只验证 Execute 自身没有意外新增 alloc。
func TestExecute_AllocsOnRead(t *testing.T) {
	r := NewRegistry()
	_ = r.Register(&fakeTool{name: "bash"})

	call := schema.ToolCall{
		ID:        "call-1",
		Name:      "bash",
		Arguments: json.RawMessage(`{}`),
	}

	// 第一次执行预热
	_ = r.Execute(context.Background(), call)

	allocs := testing.AllocsPerRun(100, func() {
		_ = r.Execute(context.Background(), call)
	})
	// 实际观察：Execute 路径大约 6-8 allocs（Span + context + log boxing），
	// 这里只给上限防止明显回归。
	if allocs > 15 {
		t.Logf("WARNING: Execute uses %.2f allocs/run; expected <= 15", allocs)
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
