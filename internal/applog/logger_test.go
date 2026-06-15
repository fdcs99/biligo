package applog

import (
	"bytes"
	"strings"
	"testing"
)

func TestLoggerFiltersLevels(t *testing.T) {
	var out bytes.Buffer
	logger := NewWithWriter([]string{LevelWarn, LevelError}, &out)

	logger.Infof("hidden")
	logger.Warnf("visible")

	got := out.String()
	if strings.Contains(got, "hidden") {
		t.Fatalf("info log should be hidden: %q", got)
	}
	if !strings.Contains(got, "[WARN] visible") {
		t.Fatalf("warn log missing: %q", got)
	}
}

func TestLoggerNoneDisablesOutput(t *testing.T) {
	var out bytes.Buffer
	logger := NewWithWriter([]string{"none"}, &out)

	logger.Errorf("hidden")
	logger.Warnf("hidden")
	logger.Infof("hidden")

	if out.String() != "" {
		t.Fatalf("none should suppress all logs: %q", out.String())
	}
}

func TestLoggerAlwaysColorsLevel(t *testing.T) {
	var out bytes.Buffer
	logger := NewWithWriter([]string{LevelInfo}, &out, ColorAlways)

	logger.Infof("colored")

	got := out.String()
	if !strings.Contains(got, "[\x1b[36mINFO\x1b[0m] colored") {
		t.Fatalf("colored info log missing: %q", got)
	}
}

func TestLoggerAutoDoesNotColorNonTerminalWriter(t *testing.T) {
	t.Setenv("NO_COLOR", "")
	t.Setenv("FORCE_COLOR", "")

	var out bytes.Buffer
	logger := NewWithWriter([]string{LevelError}, &out, ColorAuto)

	logger.Errorf("plain")

	got := out.String()
	if strings.Contains(got, "\x1b[") {
		t.Fatalf("auto mode should not color non-terminal writer: %q", got)
	}
	if !strings.Contains(got, "[ERROR] plain") {
		t.Fatalf("plain error log missing: %q", got)
	}
}
