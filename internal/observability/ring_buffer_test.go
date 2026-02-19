package observability

import (
	"fmt"
	"io"
	"sync"
	"testing"
)

func TestRingBuffer_WriteSingleLine(t *testing.T) {
	rb := NewRingBuffer(10)
	n, err := rb.Write([]byte("hello\n"))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != 6 {
		t.Fatalf("Write returned n=%d, want 6", n)
	}
	lines := rb.Lines()
	if len(lines) != 1 {
		t.Fatalf("Lines() returned %d lines, want 1", len(lines))
	}
	if lines[0] != "hello" {
		t.Fatalf("Lines()[0] = %q, want %q", lines[0], "hello")
	}
	if rb.Seq() != 1 {
		t.Fatalf("Seq() = %d, want 1", rb.Seq())
	}
}

func TestRingBuffer_WriteMultipleLines(t *testing.T) {
	rb := NewRingBuffer(10)
	n, err := rb.Write([]byte("a\nb\nc\n"))
	if err != nil {
		t.Fatalf("Write returned error: %v", err)
	}
	if n != 6 {
		t.Fatalf("Write returned n=%d, want 6", n)
	}
	lines := rb.Lines()
	if len(lines) != 3 {
		t.Fatalf("Lines() returned %d lines, want 3", len(lines))
	}
	want := []string{"a", "b", "c"}
	for i, w := range want {
		if lines[i] != w {
			t.Fatalf("Lines()[%d] = %q, want %q", i, lines[i], w)
		}
	}
	if rb.Seq() != 3 {
		t.Fatalf("Seq() = %d, want 3", rb.Seq())
	}
}

func TestRingBuffer_CircularOverwrite(t *testing.T) {
	rb := NewRingBuffer(3)
	for i := 1; i <= 5; i++ {
		rb.Write([]byte(fmt.Sprintf("line%d\n", i)))
	}
	lines := rb.Lines()
	if len(lines) != 3 {
		t.Fatalf("Lines() returned %d lines, want 3", len(lines))
	}
	want := []string{"line3", "line4", "line5"}
	for i, w := range want {
		if lines[i] != w {
			t.Fatalf("Lines()[%d] = %q, want %q", i, lines[i], w)
		}
	}
	if rb.Seq() != 5 {
		t.Fatalf("Seq() = %d, want 5", rb.Seq())
	}
}

func TestRingBuffer_PartialWrite(t *testing.T) {
	rb := NewRingBuffer(10)
	rb.Write([]byte("partial"))

	lines := rb.Lines()
	if len(lines) != 0 {
		t.Fatalf("Lines() returned %d lines after partial write, want 0", len(lines))
	}
	if rb.Seq() != 0 {
		t.Fatalf("Seq() = %d after partial write, want 0", rb.Seq())
	}

	rb.Write([]byte(" end\n"))
	lines = rb.Lines()
	if len(lines) != 1 {
		t.Fatalf("Lines() returned %d lines, want 1", len(lines))
	}
	if lines[0] != "partial end" {
		t.Fatalf("Lines()[0] = %q, want %q", lines[0], "partial end")
	}
	if rb.Seq() != 1 {
		t.Fatalf("Seq() = %d, want 1", rb.Seq())
	}
}

func TestRingBuffer_SeqIncrementsPerLine(t *testing.T) {
	rb := NewRingBuffer(10)
	for i := 1; i <= 5; i++ {
		rb.Write([]byte(fmt.Sprintf("line%d\n", i)))
		if rb.Seq() != uint64(i) {
			t.Fatalf("After writing line %d: Seq() = %d, want %d", i, rb.Seq(), i)
		}
	}
}

func TestRingBuffer_SeqUnchangedWithoutWrite(t *testing.T) {
	rb := NewRingBuffer(10)
	if rb.Seq() != 0 {
		t.Fatalf("New buffer Seq() = %d, want 0", rb.Seq())
	}
	rb.Write([]byte("no newline here"))
	if rb.Seq() != 0 {
		t.Fatalf("After partial write Seq() = %d, want 0", rb.Seq())
	}
}

func TestRingBuffer_EmptyLinesReturnsEmpty(t *testing.T) {
	rb := NewRingBuffer(10)
	lines := rb.Lines()
	if lines == nil {
		t.Fatalf("Lines() returned nil, want non-nil empty slice")
	}
	if len(lines) != 0 {
		t.Fatalf("Lines() returned %d lines, want 0", len(lines))
	}
}

func TestRingBuffer_ImplementsIOWriter(t *testing.T) {
	rb := NewRingBuffer(10)
	var _ io.Writer = rb // compile-time check

	if _, ok := interface{}(rb).(io.Writer); !ok {
		t.Fatalf("*RingBuffer does not satisfy io.Writer interface")
	}
}

func TestRingBuffer_ConcurrentWriteSafe(t *testing.T) {
	rb := NewRingBuffer(DefaultLogBufferSize)
	var wg sync.WaitGroup

	numGoroutines := 10
	linesPerGoroutine := 100

	wg.Add(numGoroutines)
	for g := 0; g < numGoroutines; g++ {
		go func(id int) {
			defer wg.Done()
			for i := 0; i < linesPerGoroutine; i++ {
				rb.Write([]byte(fmt.Sprintf("goroutine-%d-line-%d\n", id, i)))
			}
		}(g)
	}
	wg.Wait()

	lines := rb.Lines()
	if len(lines) == 0 {
		t.Fatalf("Lines() returned 0 lines after concurrent writes")
	}
	if len(lines) > DefaultLogBufferSize {
		t.Fatalf("Lines() returned %d lines, exceeding capacity %d", len(lines), DefaultLogBufferSize)
	}
	if rb.Seq() != uint64(numGoroutines*linesPerGoroutine) {
		t.Fatalf("Seq() = %d, want %d", rb.Seq(), numGoroutines*linesPerGoroutine)
	}
}

func TestRingBuffer_NewRingBuffer_ClampsCapacity(t *testing.T) {
	tests := []struct {
		name    string
		input   int
		wantCap int
		wantLen int
	}{
		{"zero clamped to 1", 0, 1, 1},
		{"negative clamped to 1", -5, 1, 1},
		{"over max clamped to 10000", 20000, 10000, 10000},
		{"valid capacity stays", 100, 100, 100},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rb := NewRingBuffer(tc.input)
			if rb == nil {
				t.Fatalf("NewRingBuffer(%d) returned nil", tc.input)
			}
			// Verify effective capacity by filling the buffer and checking Lines() length
			for i := 0; i < tc.wantCap+5; i++ {
				rb.Write([]byte(fmt.Sprintf("line%d\n", i)))
			}
			lines := rb.Lines()
			if len(lines) != tc.wantCap {
				t.Fatalf("NewRingBuffer(%d): after overflow, Lines() has %d entries, want %d (effective cap)", tc.input, len(lines), tc.wantCap)
			}
		})
	}
}
