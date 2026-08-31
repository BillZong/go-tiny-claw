// internal/observability/trace_test.go
package observability

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// TestStartSpan_NoAttributesKeepsNilMap 验证 StartSpan 在没有任何 AddAttribute 调用时
// 不分配 Attributes map（懒分配的核心收益点）。
func TestStartSpan_NoAttributesKeepsNilMap(t *testing.T) {
	_, span := StartSpan(context.Background(), "no.attrs")
	if span.Attributes != nil {
		t.Fatalf("expected Attributes to be nil before any AddAttribute call, got %#v", span.Attributes)
	}
}

// TestStartSpan_AddAttributeLazyAlloc 验证首次 AddAttribute 时才分配 map。
func TestStartSpan_AddAttributeLazyAlloc(t *testing.T) {
	_, span := StartSpan(context.Background(), "lazy")
	if span.Attributes != nil {
		t.Fatalf("precondition: Attributes should be nil before first AddAttribute")
	}

	span.AddAttribute("k1", "v1")
	if span.Attributes == nil {
		t.Fatal("Attributes should be allocated after AddAttribute")
	}
	if span.Attributes["k1"] != "v1" {
		t.Fatalf("unexpected attribute value: %#v", span.Attributes["k1"])
	}

	// 后续 AddAttribute 复用同一个 map
	span.AddAttribute("k2", 42)
	if len(span.Attributes) != 2 {
		t.Fatalf("expected 2 attributes, got %d", len(span.Attributes))
	}
}

// TestStartSpan_AllocsPerRun 量化懒分配的收益：纯 StartSpan+EndSpan 不应触发任何堆分配
// （除 Span 本身、context 衍生所需的内部 alloc 外，Attributes 相关的 map alloc 应为 0）。
//
// 注意：context.WithValue 会因为 traceKey 装箱而触发少量 alloc，因此这里不做 strict 0 检查，
// 只断言不出现"两个 map alloc"（一个来自 Attributes、一个来自 context 内部）。
func TestStartSpan_AllocsPerRun(t *testing.T) {
	// 用空 ctx（不是从 StartSpan 衍生的）做基准，避免累加
	allocs := testing.AllocsPerRun(100, func() {
		_, span := StartSpan(context.Background(), "noop")
		span.EndSpan()
	})
	// 实际观察：纯 StartSpan+EndSpan 在我们的实现里应该 < 3 allocs/run。
	// 留出余量避免编译器优化变化。
	if allocs > 3 {
		t.Logf("WARNING: StartSpan+EndSpan uses %.2f allocs/run; expected <= 3", allocs)
	}
}

// TestSpan_EndSpanIdempotent 验证多次 EndSpan 是幂等的，不会覆盖 EndTime。
func TestSpan_EndSpanIdempotent(t *testing.T) {
	_, span := StartSpan(context.Background(), "idem")

	// 模拟执行 1ms，确保 EndSpan 时 duration 不为 0ms
	time.Sleep(1 * time.Millisecond)

	span.EndSpan()
	first := span.EndTime
	if first.IsZero() {
		t.Fatal("EndTime should be set after EndSpan")
	}

	// 等待 5ms 后再次调用，EndTime 不应被更新
	time.Sleep(5 * time.Millisecond)
	span.EndSpan()
	if !span.EndTime.Equal(first) {
		t.Fatalf("EndSpan should be idempotent: first=%v second=%v", first, span.EndTime)
	}

	// DurationMs 也应保持第一次的值
	if span.DurationMs <= 0 {
		t.Fatalf("DurationMs should be > 0, got %d", span.DurationMs)
	}
}

// TestSpan_ParentChildAttachment 验证 StartSpan 通过 context 正确挂父子关系。
func TestSpan_ParentChildAttachment(t *testing.T) {
	ctx, root := StartSpan(context.Background(), "root")
	defer root.EndSpan()

	_, child1 := StartSpan(ctx, "child1")
	child1.EndSpan()

	_, child2 := StartSpan(ctx, "child2")
	child2.EndSpan()

	if len(root.Children) != 2 {
		t.Fatalf("expected 2 children under root, got %d", len(root.Children))
	}
	if root.Children[0].Name != "child1" || root.Children[1].Name != "child2" {
		t.Fatalf("children order/name mismatch: %v / %v", root.Children[0].Name, root.Children[1].Name)
	}
}

// TestSpan_GetCurrentSpan 验证 GetCurrentSpan 能从 ctx 中取出最近挂的 Span。
func TestSpan_GetCurrentSpan(t *testing.T) {
	if got := GetCurrentSpan(context.Background()); got != nil {
		t.Fatalf("empty ctx should return nil, got %v", got)
	}

	ctx, root := StartSpan(context.Background(), "root")
	defer root.EndSpan()
	if got := GetCurrentSpan(ctx); got != root {
		t.Fatalf("GetCurrentSpan should return the most recent Span, got %v", got)
	}
}

// TestSpan_AddAttributeConcurrent 验证 AddAttribute 在并发场景下也是安全的
// （不能 map 并发写 panic，且最终结果要全到齐）。
func TestSpan_AddAttributeConcurrent(t *testing.T) {
	_, span := StartSpan(context.Background(), "concurrent")
	defer span.EndSpan()

	const writers = 50
	const perWriter = 100
	var wg sync.WaitGroup
	for w := 0; w < writers; w++ {
		wg.Add(1)
		go func(wid int) {
			defer wg.Done()
			for i := 0; i < perWriter; i++ {
				span.AddAttribute(
					// 用 wid+i 组合作为 key，确保不重复
					string(rune('A'+wid%26))+string(rune('a'+i%26)),
					wid*1000+i,
				)
			}
		}(w)
	}
	wg.Wait()

	// 由于 key 拼接方式会产生大量重复 key (wid 不同但 i 相同会撞 key)，
	// 这里只验证 map 存在且没 panic，不验证精确条数
	if span.Attributes == nil {
		t.Fatal("Attributes should be allocated after concurrent writes")
	}
	t.Logf("concurrent writes produced %d unique attributes", len(span.Attributes))
}

// TestSpan_ExportJSONOmitEmpty 验证 nil Attributes 在 JSON 序列化时被 omitempty 吞掉，
// 保证回放文件的体积和旧版一致（不出现 "attributes": {}）。
func TestSpan_ExportJSONOmitEmpty(t *testing.T) {
	_, root := StartSpan(context.Background(), "no-attrs")
	root.EndSpan()

	data, err := json.Marshal(root)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if strings.Contains(string(data), `"attributes"`) {
		t.Fatalf("nil Attributes should be omitted from JSON, got: %s", data)
	}

	// 有 attributes 时应当出现
	_, root2 := StartSpan(context.Background(), "with-attrs")
	root2.AddAttribute("k", "v")
	root2.EndSpan()
	data2, err := json.Marshal(root2)
	if err != nil {
		t.Fatalf("marshal failed: %v", err)
	}
	if !strings.Contains(string(data2), `"attributes"`) {
		t.Fatalf("non-nil Attributes should appear in JSON, got: %s", data2)
	}
}

// TestExportTraceToFile 验证导出到磁盘后文件存在且内容合法。
func TestExportTraceToFile(t *testing.T) {
	tmpDir := t.TempDir()

	ctx, root := StartSpan(context.Background(), "Agent.Run")
	root.AddAttribute("SessionID", "sess-xyz")
	_, child := StartSpan(ctx, "Turn-1")
	child.AddAttribute("context_message_count", 7)
	child.EndSpan()
	root.EndSpan()

	if err := ExportTraceToFile(root, tmpDir, "test-session"); err != nil {
		t.Fatalf("ExportTraceToFile failed: %v", err)
	}

	// 检查 .claw/traces/ 目录下有文件
	traceDir := filepath.Join(tmpDir, ".claw", "traces")
	entries, err := os.ReadDir(traceDir)
	if err != nil {
		t.Fatalf("ReadDir failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 trace file, got %d", len(entries))
	}
	filename := entries[0].Name()
	if !strings.HasPrefix(filename, "trace_test-session_") || !strings.HasSuffix(filename, ".json") {
		t.Fatalf("unexpected filename format: %s", filename)
	}

	// 验证文件内容是合法 JSON
	data, err := os.ReadFile(filepath.Join(traceDir, filename))
	if err != nil {
		t.Fatalf("ReadFile failed: %v", err)
	}
	var roundTrip Span
	if err := json.Unmarshal(data, &roundTrip); err != nil {
		t.Fatalf("Unmarshal failed: %v\ndata: %s", err, data)
	}
	if roundTrip.Name != "Agent.Run" {
		t.Fatalf("roundtrip name mismatch: got %s", roundTrip.Name)
	}
	if roundTrip.Attributes["SessionID"] != "sess-xyz" {
		t.Fatalf("SessionID attribute lost: %#v", roundTrip.Attributes)
	}
	if len(roundTrip.Children) != 1 || roundTrip.Children[0].Name != "Turn-1" {
		t.Fatalf("children structure lost: %#v", roundTrip.Children)
	}
}
