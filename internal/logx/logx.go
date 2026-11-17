// Package logx writes the registrator's log lines, one per event:
//
//	<time, RFC 3339 in UTC> [<module>] <OK|WARN|ERROR|DEBUG> <what happened> key=value ...
//
// Values with spaces, quotes or equals signs are quoted; text that came from
// outside (container names, Docker's and Consul's error messages) cannot end a
// line or reach a terminal as an escape.
package logx

import (
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/farhansabbir/DockerConsulRegistrator/internal/safe"
)

// Level is how a line reads at a glance.
type Level string

// The levels.
const (
	OK    Level = "OK"
	Warn  Level = "WARN"
	Error Level = "ERROR"
	Debug Level = "DEBUG"
)

// Logger writes lines for one module. Loggers made from one another share
// their writer and never interleave a line.
type Logger struct {
	mu     *sync.Mutex
	w      io.Writer
	module string
	debug  *atomic.Bool
	// Now is the clock; tests replace it.
	Now func() time.Time
}

// New returns a logger writing to w, for module.
func New(w io.Writer, module string) *Logger {
	return &Logger{mu: &sync.Mutex{}, w: w, module: module, debug: &atomic.Bool{}, Now: time.Now}
}

// Module returns a logger for another module, writing to the same place.
func (l *Logger) Module(module string) *Logger {
	c := *l
	c.module = module
	return &c
}

// SetDebug turns DEBUG lines on or off, for every logger made from this one.
func (l *Logger) SetDebug(on bool) { l.debug.Store(on) }

// Debugging is whether DEBUG lines are written.
func (l *Logger) Debugging() bool { return l.debug.Load() }

// OK logs an event that went as it should.
func (l *Logger) OK(msg string, kv ...any) { l.log(OK, msg, kv) }

// Warn logs an event that deserves attention but stops nothing.
func (l *Logger) Warn(msg string, kv ...any) { l.log(Warn, msg, kv) }

// Error logs a failure.
func (l *Logger) Error(msg string, kv ...any) { l.log(Error, msg, kv) }

// Debug logs a detail, only while debugging is on.
func (l *Logger) Debug(msg string, kv ...any) {
	if l.Debugging() {
		l.log(Debug, msg, kv)
	}
}

func (l *Logger) log(level Level, msg string, kv []any) {
	s := Line(l.Now(), l.module, level, msg, kv...)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = io.WriteString(l.w, s+"\n")
}

// Line formats one log line.
func Line(t time.Time, module string, level Level, msg string, kv ...any) string {
	s := t.UTC().Format("2006-01-02T15:04:05.000Z07:00") + " [" + safe.Text(module) + "] " + string(level) + " " + safe.Text(msg)
	if f := Fields(kv...); f != "" {
		s += " " + f
	}
	return s
}

// Fields renders key=value pairs. A key without a value is written as
// key=!MISSING, so the mistake shows.
func Fields(kv ...any) string {
	parts := make([]string, 0, (len(kv)+1)/2)
	for i := 0; i < len(kv); i += 2 {
		key := safe.Text(fmt.Sprint(kv[i]))
		if i+1 >= len(kv) {
			parts = append(parts, key+"=!MISSING")
			break
		}
		parts = append(parts, key+"="+value(kv[i+1]))
	}
	return strings.Join(parts, " ")
}

func value(v any) string {
	var s string
	switch x := v.(type) {
	case error:
		s = x.Error()
	case time.Duration:
		s = x.String()
	case string:
		s = x
	case []string:
		s = strings.Join(x, ",")
	default:
		s = fmt.Sprint(x)
	}
	if s == "" || strings.ContainsAny(s, " \"=") || safe.Text(s) != s {
		return strconv.Quote(s)
	}
	return s
}
