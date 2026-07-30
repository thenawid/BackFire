// Package utils holds the leveled logger and small cross-cutting helpers used
// throughout backfire.
package utils

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"
)

// Level is a logging severity.
type Level int

const (
	LevelDebug Level = iota
	LevelInfo
	LevelWarn
	LevelError
)

func parseLevel(s string) Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return LevelDebug
	case "warn", "warning":
		return LevelWarn
	case "error":
		return LevelError
	default:
		return LevelInfo
	}
}

func (l Level) tag() string {
	switch l {
	case LevelDebug:
		return "DBG"
	case LevelWarn:
		return "WRN"
	case LevelError:
		return "ERR"
	default:
		return "INF"
	}
}

// Logger is a minimal leveled logger that writes a timestamped, tagged line to
// stderr. It is safe for concurrent use.
type Logger struct {
	mu    sync.Mutex
	min   Level
	scope string
}

// NewLogger builds a logger filtering below the given level name.
func NewLogger(level string) *Logger {
	return &Logger{min: parseLevel(level)}
}

// With returns a child logger that prefixes every line with a scope label.
func (l *Logger) With(scope string) *Logger {
	return &Logger{min: l.min, scope: scope}
}

func (l *Logger) log(level Level, format string, args ...any) {
	if level < l.min {
		return
	}
	msg := fmt.Sprintf(format, args...)
	l.mu.Lock()
	defer l.mu.Unlock()
	ts := time.Now().Format("2006-01-02 15:04:05")
	if l.scope != "" {
		fmt.Fprintf(os.Stderr, "%s %s [%s] %s\n", ts, level.tag(), l.scope, msg)
	} else {
		fmt.Fprintf(os.Stderr, "%s %s %s\n", ts, level.tag(), msg)
	}
}

func (l *Logger) Debugf(format string, args ...any) { l.log(LevelDebug, format, args...) }
func (l *Logger) Infof(format string, args ...any)  { l.log(LevelInfo, format, args...) }
func (l *Logger) Warnf(format string, args ...any)  { l.log(LevelWarn, format, args...) }
func (l *Logger) Errorf(format string, args ...any) { l.log(LevelError, format, args...) }
