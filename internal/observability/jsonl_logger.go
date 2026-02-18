package observability

import (
	"encoding/json"
	"fmt"
	"io"
	"sync"
	"time"
)

type Logger struct {
	mu       sync.Mutex
	w        io.Writer
	redactor *Redactor
	nowFn    func() time.Time
}

type logRecord struct {
	Timestamp string         `json:"ts"`
	Level     string         `json:"level"`
	Event     string         `json:"event"`
	Message   string         `json:"msg,omitempty"`
	Fields    map[string]any `json:"fields,omitempty"`
}

func NewLogger(w io.Writer, redactor *Redactor) *Logger {
	if w == nil {
		w = io.Discard
	}

	if redactor == nil {
		redactor = NewRedactor(nil)
	}

	return &Logger{
		w:        w,
		redactor: redactor,
		nowFn:    time.Now,
	}
}

func (l *Logger) Info(event, msg string, fields map[string]any) {
	l.log("info", event, msg, fields)
}

func (l *Logger) Warn(event, msg string, fields map[string]any) {
	l.log("warn", event, msg, fields)
}

func (l *Logger) Error(event, msg string, fields map[string]any) {
	l.log("error", event, msg, fields)
}

func (l *Logger) log(level, event, msg string, fields map[string]any) {
	if l == nil || l.w == nil {
		return
	}

	if l.nowFn == nil {
		l.nowFn = time.Now
	}

	record := logRecord{
		Timestamp: l.nowFn().UTC().Format(time.RFC3339Nano),
		Level:     level,
		Event:     l.redactor.RedactString(event),
		Message:   l.redactor.RedactString(msg),
		Fields:    l.redactor.RedactFields(fields),
	}

	b, err := json.Marshal(record)
	if err != nil {
		fallback := fmt.Sprintf("{\"ts\":%q,\"level\":%q,\"event\":%q,\"msg\":%q}\n",
			record.Timestamp,
			record.Level,
			"logger_marshal_error",
			l.redactor.RedactString(err.Error()),
		)
		l.mu.Lock()
		_, _ = io.WriteString(l.w, fallback)
		l.mu.Unlock()
		return
	}

	l.mu.Lock()
	_, _ = l.w.Write(append(b, '\n'))
	l.mu.Unlock()
}
