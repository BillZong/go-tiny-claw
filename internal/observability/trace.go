// internal/observability/trace.go
package observability

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// traceKey 是 Context 中存放当前 Span 的专属 Key。
// 使用空 struct 作为 key 避免与业务字段冲突。
type traceKey struct{}

// Span 代表链路追踪中的一个时间跨度和操作节点。
// 多棵 Span 通过 Children 字段级联为一棵调用树，根节点即可描绘一次完整 Run 的全貌。
type Span struct {
	Name       string                 `json:"name"`
	StartTime  time.Time              `json:"start_time"`
	EndTime    time.Time              `json:"end_time"`
	DurationMs int64                  `json:"duration_ms"`
	Attributes map[string]interface{} `json:"attributes,omitempty"` // 存放元数据（如消耗的 Token、执行的命令）
	Children   []*Span                `json:"children,omitempty"`   // 子跨度

	mu sync.Mutex // 保护 Attributes 与 Children 的并发写入
}

// StartSpan 开启一个新的追踪跨度，并将其挂载到 Context 中。
// 若 Context 中已存在父 Span，则新 Span 会自动级联为父 Span 的子节点。
// 父子级联是并发安全的：StartSpan 可在多个 goroutine 中同时调用。
//
// 注意：调用方需在合适的时机调用 span.EndSpan()，推荐在当前函数内使用 defer。
// 在 for 循环中直接 defer 会导致 EndSpan 被推迟到外层函数返回时执行，务必将循环体拆为独立函数。
func StartSpan(ctx context.Context, name string) (context.Context, *Span) {
	span := &Span{
		Name:      name,
		StartTime: time.Now(),
		// Attributes 走懒分配：常见场景下 Span 不带任何属性 (例如 LLM.Action 这种纯时间跨度)，
		// 让 nil map 顶上，避免每次 StartSpan 都白白 alloc 一个空 map。
		// 写入路径在 AddAttribute 里 lazy alloc，对外行为不变。
		// Attributes 字段的 json tag 是 omitempty，nil map 序列化时直接被省略，与旧版 `{}` 输出一致。
	}

	// 从 context 中尝试获取父 Span，若存在则把当前 Span 挂到父节点的 Children 上。
	if parent, ok := ctx.Value(traceKey{}).(*Span); ok {
		parent.mu.Lock()
		parent.Children = append(parent.Children, span)
		parent.mu.Unlock()
	}

	// 将当前新创建的 Span 作为最新的父节点，塞入衍生 Context 并返回。
	newCtx := context.WithValue(ctx, traceKey{}, span)
	return newCtx, span
}

// EndSpan 结束跨度，计算耗时。
// 多次调用是幂等的：第一次调用记录 EndTime，后续调用不会覆盖。
func (s *Span) EndSpan() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.EndTime.IsZero() {
		s.EndTime = time.Now()
		s.DurationMs = s.EndTime.Sub(s.StartTime).Milliseconds()
	}
}

// GetCurrentSpan 从 Context 中取出当前 Span（若存在），便于在其他包内补打属性。
// 没有当前 Span 时返回 nil，调用方需自行处理。
func GetCurrentSpan(ctx context.Context) *Span {
	if span, ok := ctx.Value(traceKey{}).(*Span); ok {
		return span
	}
	return nil
}

// AddAttribute 为当前 Span 记录关键元数据（例如 Token 消耗、工具名）。
// 并发安全；首次写入时懒分配底层 map。
func (s *Span) AddAttribute(key string, value interface{}) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.Attributes == nil {
		s.Attributes = make(map[string]interface{})
	}
	s.Attributes[key] = value
}

// ExportTraceToFile 当整个根 Span 结束时，将其序列化并保存为本地 JSON 文件。
// 文件位于 <workDir>/.claw/traces/trace_<sessionID>_<unix>.json。
func ExportTraceToFile(rootSpan *Span, workDir string, sessionID string) error {
	traceDir := filepath.Join(workDir, ".claw", "traces")
	if err := os.MkdirAll(traceDir, 0o755); err != nil {
		return err
	}
	filename := filepath.Join(traceDir, fmt.Sprintf("trace_%s_%d.json", sessionID, time.Now().Unix()))

	// 美化输出 JSON，便于人类和工具阅读。
	data, err := json.MarshalIndent(rootSpan, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filename, data, 0o644)
}
