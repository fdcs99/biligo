package notify

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/fdcs99/biligo/internal/events"
	"github.com/fdcs99/biligo/internal/model"
	"github.com/fdcs99/biligo/internal/store"
)

type Service struct {
	store  *store.Store
	sender Sender
	hub    *events.Hub
}

func NewService(store *store.Store, sender Sender, hub *events.Hub) *Service {
	if sender == nil {
		sender = NewHTTPSender(nil)
	}
	return &Service{store: store, sender: sender, hub: hub}
}

func (s *Service) SendTaskSuccess(ctx context.Context, task model.Task) {
	if s == nil || s.store == nil || s.sender == nil {
		return
	}
	notifications, err := s.store.ListEnabledNotifications(ctx)
	if err != nil || len(notifications) == 0 {
		return
	}
	title := "Biligo 抢票成功"
	content := taskSuccessContent(task)
	for _, notification := range notifications {
		sendCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		err := s.sender.Send(sendCtx, notification, title, content)
		cancel()
		level := "info"
		message := fmt.Sprintf("通知推送成功：%s。", notification.Name)
		if err != nil {
			level = "warn"
			message = fmt.Sprintf("通知推送失败：%s：%s", notification.Name, err.Error())
		}
		if log, logErr := s.store.AddTaskLog(context.Background(), task.ID, level, message); logErr == nil {
			s.publishLog(log)
		}
	}
}

func (s *Service) publishLog(log model.TaskLog) {
	if s.hub != nil && log.ID > 0 {
		s.hub.Publish("log.created", log)
	}
}

func taskSuccessContent(task model.Task) string {
	lines := []string{
		"订单已创建，请尽快完成支付。",
		"任务：" + fallback(task.Name, "-"),
		"项目：" + fallback(task.ProjectName, "-"),
		"票种：" + fallback(task.TicketDisplay, fallback(strings.TrimSpace(task.SessionName+" "+task.TicketLevel), "-")),
		fmt.Sprintf("数量：%d 张", task.Quantity),
	}
	if strings.TrimSpace(task.OrderID) != "" {
		lines = append(lines, "订单号："+strings.TrimSpace(task.OrderID))
	}
	return strings.Join(lines, "\n")
}

func fallback(value string, fallbackValue string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallbackValue
	}
	return value
}
