package events

import (
	"encoding/json"
	"fmt"
	"sync"

	"github.com/fdcs99/biligo/internal/applog"
	"github.com/fdcs99/biligo/internal/model"
)

type Event struct {
	Name string
	Data []byte
}

type Hub struct {
	mu          sync.Mutex
	nextID      int
	subscribers map[int]chan Event
	logger      *applog.Logger
}

func NewHub(logger ...*applog.Logger) *Hub {
	var selectedLogger *applog.Logger
	if len(logger) > 0 {
		selectedLogger = logger[0]
	}
	return &Hub{
		subscribers: map[int]chan Event{},
		logger:      selectedLogger,
	}
}

func (h *Hub) Subscribe() (int, <-chan Event) {
	h.mu.Lock()
	defer h.mu.Unlock()

	h.nextID++
	id := h.nextID
	ch := make(chan Event, 32)
	h.subscribers[id] = ch
	return id, ch
}

func (h *Hub) Unsubscribe(id int) {
	h.mu.Lock()
	ch, ok := h.subscribers[id]
	if ok {
		delete(h.subscribers, id)
		close(ch)
	}
	h.mu.Unlock()
}

func (h *Hub) Publish(name string, payload any) {
	h.logEvent(name, payload)

	data, err := json.Marshal(payload)
	if err != nil {
		return
	}
	event := Event{Name: name, Data: data}

	h.mu.Lock()
	defer h.mu.Unlock()
	for _, ch := range h.subscribers {
		select {
		case ch <- event:
		default:
		}
	}
}

func (h *Hub) logEvent(name string, payload any) {
	if h.logger == nil || name != "log.created" {
		return
	}
	switch value := payload.(type) {
	case model.TaskLog:
		h.logger.Log(value.Level, fmt.Sprintf("任务日志实时同步：任务 %d：%s", value.TaskID, value.Message))
	case *model.TaskLog:
		if value != nil {
			h.logger.Log(value.Level, fmt.Sprintf("任务日志实时同步：任务 %d：%s", value.TaskID, value.Message))
		}
	}
}
