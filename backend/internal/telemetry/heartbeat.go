package telemetry

import (
	"sync/atomic"
	"time"
)

type Heartbeat struct{ last atomic.Pointer[time.Time] }

func (h *Heartbeat) Beat() {
	now := time.Now()
	h.last.Store(&now)
}

func (h *Heartbeat) Clear() { h.last.Store(nil) }

func (h *Heartbeat) Recent(maxAge time.Duration) bool {
	last := h.last.Load()
	return last != nil && time.Since(*last) <= maxAge
}
