package applog

import (
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"
)

const (
	LevelError = "error"
	LevelWarn  = "warn"
	LevelInfo  = "info"

	ColorAuto   = "auto"
	ColorAlways = "always"
	ColorNever  = "never"
)

type Logger struct {
	mu      sync.Mutex
	out     io.Writer
	enabled map[string]bool
	color   bool
}

func New(levels []string, colorMode ...string) *Logger {
	mode := ColorAuto
	if len(colorMode) > 0 {
		mode = colorMode[0]
	}
	return newWithWriter(levels, os.Stdout, mode)
}

func NewWithWriter(levels []string, out io.Writer, colorMode ...string) *Logger {
	mode := ColorNever
	if len(colorMode) > 0 {
		mode = colorMode[0]
	}
	return newWithWriter(levels, out, mode)
}

func newWithWriter(levels []string, out io.Writer, colorMode string) *Logger {
	if out == nil {
		out = io.Discard
	}
	enabled := map[string]bool{}
	for _, level := range levels {
		level = normalizeLevel(level)
		if level == "none" {
			return &Logger{out: out, enabled: map[string]bool{}}
		}
		if level == "all" {
			enabled[LevelError] = true
			enabled[LevelWarn] = true
			enabled[LevelInfo] = true
			continue
		}
		if level != "" {
			enabled[level] = true
		}
	}
	return &Logger{out: out, enabled: enabled, color: shouldUseColor(out, colorMode)}
}

func (l *Logger) Errorf(format string, args ...any) {
	l.Logf(LevelError, format, args...)
}

func (l *Logger) Warnf(format string, args ...any) {
	l.Logf(LevelWarn, format, args...)
}

func (l *Logger) Infof(format string, args ...any) {
	l.Logf(LevelInfo, format, args...)
}

func (l *Logger) Logf(level string, format string, args ...any) {
	l.Log(level, fmt.Sprintf(format, args...))
}

func (l *Logger) Log(level string, message string) {
	if l == nil {
		return
	}
	level = normalizeLevel(level)
	if !l.enabled[level] {
		return
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	label := strings.ToUpper(level)
	if l.color {
		label = colorizeLevel(level, label)
	}
	fmt.Fprintf(l.out, "%s [%s] %s\n", time.Now().Format(time.RFC3339), label, strings.TrimSpace(message))
}

func normalizeLevel(level string) string {
	level = strings.ToLower(strings.TrimSpace(level))
	switch level {
	case "warning":
		return LevelWarn
	default:
		return level
	}
}

func shouldUseColor(out io.Writer, mode string) bool {
	switch normalizeColorMode(mode) {
	case ColorAlways:
		return true
	case ColorNever:
		return false
	default:
		return terminalSupportsColor(out)
	}
}

func terminalSupportsColor(out io.Writer) bool {
	file, ok := out.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if envEnabled("FORCE_COLOR") {
		return true
	}
	term := strings.ToLower(strings.TrimSpace(os.Getenv("TERM")))
	if term == "dumb" {
		return false
	}
	if term != "" {
		return true
	}
	if runtime.GOOS == "windows" {
		return os.Getenv("WT_SESSION") != "" ||
			os.Getenv("ANSICON") != "" ||
			strings.EqualFold(os.Getenv("ConEmuANSI"), "ON") ||
			strings.EqualFold(os.Getenv("TERM_PROGRAM"), "vscode")
	}
	return false
}

func envEnabled(name string) bool {
	value := strings.ToLower(strings.TrimSpace(os.Getenv(name)))
	return value != "" && value != "0" && value != "false" && value != "off"
}

func colorizeLevel(level string, label string) string {
	const reset = "\x1b[0m"
	switch level {
	case LevelError:
		return "\x1b[31m" + label + reset
	case LevelWarn:
		return "\x1b[33m" + label + reset
	case LevelInfo:
		return "\x1b[36m" + label + reset
	default:
		return label
	}
}

func normalizeColorMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case "", ColorAuto:
		return ColorAuto
	case ColorAlways, "on", "true", "force":
		return ColorAlways
	case ColorNever, "off", "false", "none":
		return ColorNever
	default:
		return ColorAuto
	}
}
