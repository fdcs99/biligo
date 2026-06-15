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
	mu       sync.Mutex
	console  io.Writer
	file     io.Writer
	enabled  map[string]bool
	useColor bool
}

func New(levels []string, colorMode ...string) *Logger {
	mode := ColorAuto
	if len(colorMode) > 0 {
		mode = colorMode[0]
	}
	return newWithWriters(levels, os.Stdout, nil, mode)
}

func NewWithWriter(levels []string, out io.Writer, colorMode ...string) *Logger {
	mode := ColorNever
	if len(colorMode) > 0 {
		mode = colorMode[0]
	}
	return newWithWriters(levels, out, nil, mode)
}

func NewWithFile(levels []string, colorMode string, file io.Writer) *Logger {
	return newWithWriters(levels, os.Stdout, file, colorMode)
}

func newWithWriters(levels []string, console io.Writer, file io.Writer, colorMode string) *Logger {
	if console == nil {
		console = io.Discard
	}
	enabled := map[string]bool{}
	for _, level := range levels {
		level = normalizeLevel(level)
		if level == "none" {
			return &Logger{console: console, file: file, enabled: map[string]bool{}}
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
	return &Logger{console: console, file: file, enabled: enabled, useColor: shouldUseColor(console, colorMode)}
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
	timestamp := time.Now().Format(time.RFC3339)
	body := strings.TrimSpace(message)
	consoleLabel := label
	if l.useColor {
		consoleLabel = colorizeLevel(level, label)
	}
	fmt.Fprintf(l.console, "%s [%s] %s\n", timestamp, consoleLabel, body)
	if l.file != nil {
		fmt.Fprintf(l.file, "%s [%s] %s\n", timestamp, label, body)
	}
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
