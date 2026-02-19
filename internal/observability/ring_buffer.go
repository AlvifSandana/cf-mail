package observability

import (
	"strings"
	"sync"
)

// DefaultLogBufferSize is the default number of log lines kept in memory for
// the TUI Logs panel.
const DefaultLogBufferSize = 200

// RingBuffer is a thread-safe, fixed-capacity circular buffer of log lines
// that implements io.Writer. Each Write splits the incoming bytes on newlines
// and stores complete lines. Oldest lines are silently discarded when the
// buffer is full.
type RingBuffer struct {
	mu      sync.Mutex
	buf     []string
	cap     int
	head    int
	count   int
	seq     uint64
	partial string
}

// NewRingBuffer creates a RingBuffer with the given line capacity.
// Capacity is clamped to [1, 10000].
func NewRingBuffer(capacity int) *RingBuffer {
	if capacity < 1 {
		capacity = 1
	}
	if capacity > 10000 {
		capacity = 10000
	}
	return &RingBuffer{
		buf: make([]string, capacity),
		cap: capacity,
	}
}

// Write implements io.Writer. Incoming bytes are split on newlines and stored
// as individual lines. Partial lines (no trailing newline) are accumulated
// until the next Write completes them. Write never returns an error.
func (rb *RingBuffer) Write(p []byte) (int, error) {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	data := rb.partial + string(p)
	rb.partial = ""

	for {
		idx := strings.IndexByte(data, '\n')
		if idx < 0 {
			// No more newlines — store as partial
			rb.partial = data
			break
		}

		line := data[:idx]
		data = data[idx+1:]

		rb.buf[rb.head] = line
		rb.head = (rb.head + 1) % rb.cap
		if rb.count < rb.cap {
			rb.count++
		}
		rb.seq++
	}

	return len(p), nil
}

// Lines returns a copy of all stored lines in chronological order (oldest
// first). The returned slice is safe to use without synchronization.
func (rb *RingBuffer) Lines() []string {
	rb.mu.Lock()
	defer rb.mu.Unlock()

	if rb.count == 0 {
		return []string{}
	}

	out := make([]string, rb.count)
	if rb.count < rb.cap {
		copy(out, rb.buf[:rb.count])
	} else {
		// Buffer is full — head points to the oldest entry
		n := copy(out, rb.buf[rb.head:rb.cap])
		copy(out[n:], rb.buf[:rb.head])
	}

	return out
}

// Seq returns a monotonically increasing counter that is incremented each
// time a complete line is stored. Callers can use this to detect whether
// new lines have been written since the last poll.
func (rb *RingBuffer) Seq() uint64 {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.seq
}
