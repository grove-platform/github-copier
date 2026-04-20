package services

import (
	"sync"
	"time"
)

const webhookTraceMaxEntries = 120

// WebhookTraceEntry is one observed webhook for operator troubleshooting (in-memory only).
type WebhookTraceEntry struct {
	At         time.Time `json:"at"`
	DeliveryID string    `json:"delivery_id,omitempty"`
	EventType  string    `json:"event_type,omitempty"`
	Action     string    `json:"action,omitempty"`
	Repo       string    `json:"repo,omitempty"`
	BaseBranch string    `json:"base_branch,omitempty"`
	CommitSHA  string    `json:"commit_sha,omitempty"`
	PRNumber   int       `json:"pr_number,omitempty"`
	Outcome    string    `json:"outcome"`
	Detail     string    `json:"detail,omitempty"`
}

// WebhookTraceBuffer stores the last N webhook outcomes for the operator UI.
type WebhookTraceBuffer struct {
	mu  sync.Mutex
	buf []WebhookTraceEntry
}

// NewWebhookTraceBuffer creates an empty trace buffer.
func NewWebhookTraceBuffer() *WebhookTraceBuffer {
	return &WebhookTraceBuffer{buf: make([]WebhookTraceEntry, 0, 32)}
}

// Append adds an entry (timestamps default to UTC now; detail is truncated).
func (b *WebhookTraceBuffer) Append(e WebhookTraceEntry) {
	if b == nil {
		return
	}
	if e.Outcome == "" {
		e.Outcome = "unknown"
	}
	if e.At.IsZero() {
		e.At = time.Now().UTC()
	}
	if len(e.Detail) > 500 {
		e.Detail = e.Detail[:500] + "…"
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, e)
	if len(b.buf) > webhookTraceMaxEntries {
		b.buf = b.buf[len(b.buf)-webhookTraceMaxEntries:]
	}
}

// Len returns how many trace entries are buffered.
func (b *WebhookTraceBuffer) Len() int {
	if b == nil {
		return 0
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.buf)
}

// Recent returns the last up to max entries (oldest first within the slice).
func (b *WebhookTraceBuffer) Recent(max int) []WebhookTraceEntry {
	if b == nil {
		return nil
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.buf) == 0 {
		return nil
	}
	if max <= 0 {
		max = 50
	}
	if max > webhookTraceMaxEntries {
		max = webhookTraceMaxEntries
	}
	if len(b.buf) <= max {
		out := make([]WebhookTraceEntry, len(b.buf))
		copy(out, b.buf)
		return out
	}
	return append([]WebhookTraceEntry(nil), b.buf[len(b.buf)-max:]...)
}

// AppendWebhookTrace records one webhook row for the operator dashboard.
func AppendWebhookTrace(c *ServiceContainer, e WebhookTraceEntry) {
	if c == nil || c.WebhookTraces == nil {
		return
	}
	c.WebhookTraces.Append(e)
}
