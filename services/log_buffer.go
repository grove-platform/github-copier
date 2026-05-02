package services

import (
	"context"
	"sync"
	"time"
)

// Maximum entries per delivery and total across all deliveries.
const (
	logBufferMaxPerDelivery = 100
	logBufferMaxDeliveries  = 50
)

// LogEntry is a single captured log line for operator diagnostics.
type LogEntry struct {
	Time    time.Time      `json:"time"`
	Level   string         `json:"level"`
	Message string         `json:"message"`
	Fields  map[string]any `json:"fields,omitempty"`
}

// DeliveryLogBuffer stores recent log entries keyed by delivery ID for the operator UI.
type DeliveryLogBuffer struct {
	mu      sync.Mutex
	entries map[string][]LogEntry
	order   []string // insertion order for eviction
}

// NewDeliveryLogBuffer creates an empty delivery log buffer.
func NewDeliveryLogBuffer() *DeliveryLogBuffer {
	return &DeliveryLogBuffer{
		entries: make(map[string][]LogEntry),
		order:   make([]string, 0, logBufferMaxDeliveries),
	}
}

// Append adds a log entry for a delivery ID.
func (b *DeliveryLogBuffer) Append(deliveryID string, entry LogEntry) {
	if b == nil || deliveryID == "" {
		return
	}
	if entry.Time.IsZero() {
		entry.Time = time.Now().UTC()
	}
	b.mu.Lock()
	defer b.mu.Unlock()

	logs, exists := b.entries[deliveryID]
	if !exists {
		b.order = append(b.order, deliveryID)
		// Evict oldest delivery if over limit
		if len(b.order) > logBufferMaxDeliveries {
			evict := b.order[0]
			b.order = b.order[1:]
			delete(b.entries, evict)
		}
	}
	logs = append(logs, entry)
	if len(logs) > logBufferMaxPerDelivery {
		logs = logs[len(logs)-logBufferMaxPerDelivery:]
	}
	b.entries[deliveryID] = logs
}

// Get returns log entries for a delivery ID (nil if not found).
func (b *DeliveryLogBuffer) Get(deliveryID string) []LogEntry {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	logs, ok := b.entries[deliveryID]
	if !ok {
		return nil
	}
	out := make([]LogEntry, len(logs))
	copy(out, logs)
	return out
}

// context key for log buffer
type logBufferCtxKey struct{}

// ContextWithLogBuffer returns a context that carries a delivery ID for log capture.
func ContextWithLogBuffer(ctx context.Context, deliveryID string, buf *DeliveryLogBuffer) context.Context {
	return context.WithValue(ctx, logBufferCtxKey{}, &logBufferCtxVal{deliveryID: deliveryID, buf: buf})
}

type logBufferCtxVal struct {
	deliveryID string
	buf        *DeliveryLogBuffer
}

// logBufferFromCtx extracts the log buffer from context (nil if not set).
func logBufferFromCtx(ctx context.Context) *logBufferCtxVal {
	if ctx == nil {
		return nil
	}
	val, _ := ctx.Value(logBufferCtxKey{}).(*logBufferCtxVal)
	return val
}

// appendToCtxBuffer appends a log entry to the context's delivery log buffer if present.
func appendToCtxBuffer(ctx context.Context, level, message string, fields map[string]any) {
	val := logBufferFromCtx(ctx)
	if val == nil || val.buf == nil {
		return
	}
	val.buf.Append(val.deliveryID, LogEntry{
		Time:    time.Now().UTC(),
		Level:   level,
		Message: message,
		Fields:  fields,
	})
}
