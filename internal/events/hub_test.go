package events

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/fdcs99/biligo/internal/applog"
	"github.com/fdcs99/biligo/internal/model"
)

func TestHubPublishesToSubscriber(t *testing.T) {
	hub := NewHub()
	id, ch := hub.Subscribe()
	defer hub.Unsubscribe(id)

	hub.Publish("task.updated", map[string]any{"id": 1})

	select {
	case event := <-ch:
		if event.Name != "task.updated" {
			t.Fatalf("event.Name = %q, want task.updated", event.Name)
		}
		var payload map[string]any
		if err := json.Unmarshal(event.Data, &payload); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if payload["id"].(float64) != 1 {
			t.Fatalf("payload id = %v, want 1", payload["id"])
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for event")
	}
}

func TestHubLogsCreatedTaskLogs(t *testing.T) {
	var out bytes.Buffer
	hub := NewHub(applog.NewWithWriter([]string{"info"}, &out))

	hub.Publish("log.created", model.TaskLog{
		TaskID:  9,
		Level:   "info",
		Message: "任务已创建。",
	})

	got := out.String()
	if !strings.Contains(got, "[INFO] 任务日志实时同步：任务 9：任务已创建。") {
		t.Fatalf("unexpected app log: %q", got)
	}
}
