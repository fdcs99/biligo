package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/fdcs99/biligo/internal/model"
	_ "modernc.org/sqlite"
)

type Store struct {
	db *sql.DB
}

const interruptedTaskMessage = "软件启动时检测到上次抢票未结束，已自动停止任务。"

func Open(path string) (*Store, error) {
	if path != ":memory:" {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return nil, err
		}
	}

	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	db.SetMaxOpenConns(1)

	store := &Store{db: db}
	if err := store.migrate(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}

	return store, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

func (s *Store) migrate(ctx context.Context) error {
	statements := []string{
		`PRAGMA foreign_keys = ON;`,
		`PRAGMA journal_mode = WAL;`,
		`CREATE TABLE IF NOT EXISTS accounts (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			cookie TEXT NOT NULL DEFAULT '',
			status TEXT NOT NULL DEFAULT 'missing_cookie',
			note TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS ticket_project_history (
			project_id INTEGER PRIMARY KEY,
			project_name TEXT NOT NULL DEFAULT '',
			venue_name TEXT NOT NULL DEFAULT '',
			venue_address TEXT NOT NULL DEFAULT '',
			start_at TEXT NOT NULL DEFAULT '',
			end_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS notifications (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			provider TEXT NOT NULL,
			config TEXT NOT NULL DEFAULT '{}',
			enabled INTEGER NOT NULL DEFAULT 0,
			last_test_status TEXT NOT NULL DEFAULT '',
			last_test_message TEXT NOT NULL DEFAULT '',
			last_tested_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS proxy_groups (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			type TEXT NOT NULL DEFAULT 'static',
			api_provider TEXT NOT NULL DEFAULT '',
			api_config TEXT NOT NULL DEFAULT '{}',
			last_pull_status TEXT NOT NULL DEFAULT '',
			last_pull_message TEXT NOT NULL DEFAULT '',
			last_pulled_at TEXT NOT NULL DEFAULT '',
			last_test_status TEXT NOT NULL DEFAULT '',
			last_test_message TEXT NOT NULL DEFAULT '',
			last_tested_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS proxy_nodes (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			group_id INTEGER NOT NULL DEFAULT 0,
			name TEXT NOT NULL DEFAULT '',
			protocol TEXT NOT NULL DEFAULT 'http',
			host TEXT NOT NULL DEFAULT '',
			port INTEGER NOT NULL DEFAULT 0,
			username TEXT NOT NULL DEFAULT '',
			password TEXT NOT NULL DEFAULT '',
			source TEXT NOT NULL DEFAULT 'manual',
			last_test_status TEXT NOT NULL DEFAULT '',
			last_test_message TEXT NOT NULL DEFAULT '',
			last_test_latency_ms INTEGER NOT NULL DEFAULT 0,
			last_test_ip_location TEXT NOT NULL DEFAULT '',
			last_tested_at TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			name TEXT NOT NULL,
			account_id INTEGER NOT NULL DEFAULT 0,
			proxy_group_id INTEGER NOT NULL DEFAULT 0,
			project_id INTEGER NOT NULL DEFAULT 0,
			project_name TEXT NOT NULL DEFAULT '',
			screen_id INTEGER NOT NULL DEFAULT 0,
			sku_id INTEGER NOT NULL DEFAULT 0,
			session_name TEXT NOT NULL DEFAULT '',
			ticket_level TEXT NOT NULL DEFAULT '',
			ticket_display TEXT NOT NULL DEFAULT '',
			ticket_price INTEGER NOT NULL DEFAULT 0,
			sale_start TEXT NOT NULL DEFAULT '',
			sale_status TEXT NOT NULL DEFAULT '',
			link_id INTEGER NOT NULL DEFAULT 0,
			is_hot_project INTEGER NOT NULL DEFAULT 0,
			task_mode TEXT NOT NULL DEFAULT 'rush',
			duration_mode TEXT NOT NULL DEFAULT 'limited',
			selected_tickets TEXT NOT NULL DEFAULT '[]',
			rush_duration_seconds INTEGER NOT NULL DEFAULT 600,
			order_type INTEGER NOT NULL DEFAULT 1,
			pay_money INTEGER NOT NULL DEFAULT 0,
			buyer_info TEXT NOT NULL DEFAULT '[]',
			buyer TEXT NOT NULL DEFAULT '',
			tel TEXT NOT NULL DEFAULT '',
			deliver_info TEXT NOT NULL DEFAULT '{}',
			phone TEXT NOT NULL DEFAULT '',
			order_id TEXT NOT NULL DEFAULT '',
			payment_url TEXT NOT NULL DEFAULT '',
			payment_qr_image_data_url TEXT NOT NULL DEFAULT '',
			last_checked_at TEXT NOT NULL DEFAULT '',
			time_sync_strategy TEXT NOT NULL DEFAULT 'bilibili',
			time_offset_ms INTEGER NOT NULL DEFAULT 0,
			time_synced_at TEXT NOT NULL DEFAULT '',
			quantity INTEGER NOT NULL DEFAULT 1,
			start_at TEXT NOT NULL DEFAULT '',
			end_at TEXT NOT NULL DEFAULT '',
			poll_interval_ms INTEGER NOT NULL DEFAULT 1000,
			status TEXT NOT NULL DEFAULT 'draft',
			last_message TEXT NOT NULL DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);`,
		`CREATE TABLE IF NOT EXISTS task_logs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			task_id INTEGER NOT NULL DEFAULT 0,
			level TEXT NOT NULL,
			message TEXT NOT NULL,
			created_at TEXT NOT NULL
		);`,
	}

	for _, stmt := range statements {
		if _, err := s.db.ExecContext(ctx, stmt); err != nil {
			return err
		}
	}
	if err := s.ensureColumn(ctx, "tasks", "proxy_group_id", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_nodes", "last_test_latency_ms", "INTEGER NOT NULL DEFAULT 0"); err != nil {
		return err
	}
	if err := s.ensureColumn(ctx, "proxy_nodes", "last_test_ip_location", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	return nil
}

func (s *Store) SessionSummary(ctx context.Context) (model.SessionSummary, error) {
	var summary model.SessionSummary
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COUNT(*),
			COALESCE(SUM(CASE WHEN cookie <> '' THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN status = 'logged_in' THEN 1 ELSE 0 END), 0)
		FROM accounts
	`).Scan(&summary.AccountCount, &summary.ConfiguredAccounts, &summary.VerifiedAccounts)
	if err != nil {
		return model.SessionSummary{}, err
	}

	if summary.VerifiedAccounts > 0 {
		summary.Status = "ready"
		summary.Message = "已保存通过登录态验证的账号。"
	} else if summary.ConfiguredAccounts > 0 {
		summary.Status = "needs_verify"
		summary.Message = "已保存账号 Cookie，建议先验证登录态。"
	} else {
		summary.Status = "missing_account"
		summary.Message = "尚未保存账号 Cookie。"
	}

	return summary, nil
}

func (s *Store) ListAccounts(ctx context.Context) ([]model.Account, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, cookie, status, note, created_at, updated_at
		FROM accounts
		ORDER BY updated_at DESC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	accounts := make([]model.Account, 0)
	for rows.Next() {
		var account model.Account
		var cookie string
		if err := rows.Scan(&account.ID, &account.Name, &cookie, &account.Status, &account.Note, &account.CreatedAt, &account.UpdatedAt); err != nil {
			return nil, err
		}
		account.HasCookie = cookie != ""
		account.CookiePreview = maskCookie(cookie)
		accounts = append(accounts, account)
	}

	return accounts, rows.Err()
}

func (s *Store) CreateAccount(ctx context.Context, input model.AccountInput) (model.Account, error) {
	return s.CreateAccountWithStatus(ctx, input, accountStatus(input.Cookie))
}

func (s *Store) CreateAccountWithStatus(ctx context.Context, input model.AccountInput, status string) (model.Account, error) {
	now := nowText()
	if status == "" {
		status = accountStatus(input.Cookie)
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO accounts (name, cookie, status, note, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, strings.TrimSpace(input.Name), strings.TrimSpace(input.Cookie), status, strings.TrimSpace(input.Note), now, now)
	if err != nil {
		return model.Account{}, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return model.Account{}, err
	}
	return s.GetAccount(ctx, id)
}

func (s *Store) GetAccountCookie(ctx context.Context, id int64) (model.Account, string, error) {
	var account model.Account
	var cookie string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, cookie, status, note, created_at, updated_at
		FROM accounts
		WHERE id = ?
	`, id).Scan(&account.ID, &account.Name, &cookie, &account.Status, &account.Note, &account.CreatedAt, &account.UpdatedAt)
	if err != nil {
		return model.Account{}, "", err
	}
	account.HasCookie = cookie != ""
	account.CookiePreview = maskCookie(cookie)
	return account, cookie, nil
}

func (s *Store) GetAccount(ctx context.Context, id int64) (model.Account, error) {
	var account model.Account
	var cookie string
	err := s.db.QueryRowContext(ctx, `
		SELECT id, name, cookie, status, note, created_at, updated_at
		FROM accounts
		WHERE id = ?
	`, id).Scan(&account.ID, &account.Name, &cookie, &account.Status, &account.Note, &account.CreatedAt, &account.UpdatedAt)
	if err != nil {
		return model.Account{}, err
	}
	account.HasCookie = cookie != ""
	account.CookiePreview = maskCookie(cookie)
	return account, nil
}

func (s *Store) UpdateAccount(ctx context.Context, id int64, input model.AccountInput) (model.Account, error) {
	now := nowText()
	if strings.TrimSpace(input.Cookie) == "" {
		_, err := s.db.ExecContext(ctx, `
			UPDATE accounts
			SET name = ?, note = ?, updated_at = ?
			WHERE id = ?
		`, strings.TrimSpace(input.Name), strings.TrimSpace(input.Note), now, id)
		if err != nil {
			return model.Account{}, err
		}
		return s.GetAccount(ctx, id)
	}

	_, err := s.db.ExecContext(ctx, `
		UPDATE accounts
		SET name = ?, cookie = ?, status = ?, note = ?, updated_at = ?
		WHERE id = ?
	`, strings.TrimSpace(input.Name), strings.TrimSpace(input.Cookie), accountStatus(input.Cookie), strings.TrimSpace(input.Note), now, id)
	if err != nil {
		return model.Account{}, err
	}
	return s.GetAccount(ctx, id)
}

func (s *Store) UpdateAccountStatus(ctx context.Context, id int64, status string) (model.Account, error) {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
		UPDATE accounts
		SET status = ?, updated_at = ?
		WHERE id = ?
	`, strings.TrimSpace(status), now, id)
	if err != nil {
		return model.Account{}, err
	}
	return s.GetAccount(ctx, id)
}

func (s *Store) DeleteAccount(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	return err
}

func (s *Store) ListTicketProjectHistory(ctx context.Context) ([]model.TicketProjectHistory, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT project_id, project_name, venue_name, venue_address, start_at, end_at, updated_at
		FROM ticket_project_history
		ORDER BY updated_at DESC, project_id DESC
		LIMIT 50
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	history := make([]model.TicketProjectHistory, 0)
	for rows.Next() {
		var item model.TicketProjectHistory
		if err := rows.Scan(&item.ProjectID, &item.ProjectName, &item.VenueName, &item.VenueAddress, &item.StartAt, &item.EndAt, &item.UpdatedAt); err != nil {
			return nil, err
		}
		history = append(history, item)
	}
	return history, rows.Err()
}

func (s *Store) UpsertTicketProjectHistory(ctx context.Context, project model.TicketProject) error {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ticket_project_history (project_id, project_name, venue_name, venue_address, start_at, end_at, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id) DO UPDATE SET
			project_name = excluded.project_name,
			venue_name = excluded.venue_name,
			venue_address = excluded.venue_address,
			start_at = excluded.start_at,
			end_at = excluded.end_at,
			updated_at = excluded.updated_at
	`, project.ProjectID, strings.TrimSpace(project.ProjectName), strings.TrimSpace(project.VenueName), strings.TrimSpace(project.VenueAddress), strings.TrimSpace(project.StartAt), strings.TrimSpace(project.EndAt), now, now)
	return err
}

func (s *Store) ListNotifications(ctx context.Context) ([]model.Notification, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, provider, config, enabled, last_test_status, last_test_message, last_tested_at, created_at, updated_at
		FROM notifications
		ORDER BY updated_at DESC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	notifications := make([]model.Notification, 0)
	for rows.Next() {
		var notification model.Notification
		if err := scanNotification(rows, &notification); err != nil {
			return nil, err
		}
		notifications = append(notifications, notification)
	}
	return notifications, rows.Err()
}

func (s *Store) ListEnabledNotifications(ctx context.Context) ([]model.Notification, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, name, provider, config, enabled, last_test_status, last_test_message, last_tested_at, created_at, updated_at
		FROM notifications
		WHERE enabled = 1
		ORDER BY updated_at DESC, id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	notifications := make([]model.Notification, 0)
	for rows.Next() {
		var notification model.Notification
		if err := scanNotification(rows, &notification); err != nil {
			return nil, err
		}
		notifications = append(notifications, notification)
	}
	return notifications, rows.Err()
}

func (s *Store) CreateNotification(ctx context.Context, input model.NotificationInput) (model.Notification, error) {
	now := nowText()
	input = normalizeNotificationInput(input)
	config, err := marshalJSON(input.Config, "{}")
	if err != nil {
		return model.Notification{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO notifications (name, provider, config, enabled, created_at, updated_at)
		VALUES (?, ?, ?, 0, ?, ?)
	`, strings.TrimSpace(input.Name), input.Provider, config, now, now)
	if err != nil {
		return model.Notification{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.Notification{}, err
	}
	return s.GetNotification(ctx, id)
}

func (s *Store) GetNotification(ctx context.Context, id int64) (model.Notification, error) {
	var notification model.Notification
	row := s.db.QueryRowContext(ctx, `
		SELECT id, name, provider, config, enabled, last_test_status, last_test_message, last_tested_at, created_at, updated_at
		FROM notifications
		WHERE id = ?
	`, id)
	err := scanNotification(row, &notification)
	return notification, err
}

func (s *Store) UpdateNotification(ctx context.Context, id int64, input model.NotificationInput) (model.Notification, error) {
	now := nowText()
	input = normalizeNotificationInput(input)
	config, err := marshalJSON(input.Config, "{}")
	if err != nil {
		return model.Notification{}, err
	}
	if _, err := s.db.ExecContext(ctx, `
		UPDATE notifications
		SET name = ?, provider = ?, config = ?, updated_at = ?
		WHERE id = ?
	`, strings.TrimSpace(input.Name), input.Provider, config, now, id); err != nil {
		return model.Notification{}, err
	}
	return s.GetNotification(ctx, id)
}

func (s *Store) DeleteNotification(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM notifications WHERE id = ?`, id)
	return err
}

func (s *Store) SetNotificationEnabled(ctx context.Context, id int64, enabled bool) (model.Notification, error) {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
		UPDATE notifications
		SET enabled = ?, updated_at = ?
		WHERE id = ?
	`, boolToInt(enabled), now, id)
	if err != nil {
		return model.Notification{}, err
	}
	return s.GetNotification(ctx, id)
}

func (s *Store) SetNotificationTestResult(ctx context.Context, id int64, status string, message string) (model.Notification, error) {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
		UPDATE notifications
		SET last_test_status = ?, last_test_message = ?, last_tested_at = ?, updated_at = ?
		WHERE id = ?
	`, strings.TrimSpace(status), strings.TrimSpace(message), now, now, id)
	if err != nil {
		return model.Notification{}, err
	}
	return s.GetNotification(ctx, id)
}

func (s *Store) ListProxyGroups(ctx context.Context) ([]model.ProxyGroup, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			pg.id, pg.name, pg.type, pg.api_provider, pg.api_config,
			pg.last_pull_status, pg.last_pull_message, pg.last_pulled_at,
			pg.last_test_status, pg.last_test_message, pg.last_tested_at,
			COUNT(DISTINCT pn.id),
			COUNT(DISTINCT CASE WHEN pn.last_test_status = 'success' THEN pn.id END),
			COALESCE(MAX(CASE WHEN t.status IN ('waiting_start', 'running') THEN 1 ELSE 0 END), 0),
			pg.created_at, pg.updated_at
		FROM proxy_groups pg
		LEFT JOIN proxy_nodes pn ON pn.group_id = pg.id
		LEFT JOIN tasks t ON t.proxy_group_id = pg.id
		GROUP BY pg.id
		ORDER BY pg.updated_at DESC, pg.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	groups := make([]model.ProxyGroup, 0)
	for rows.Next() {
		var group model.ProxyGroup
		if err := scanProxyGroup(rows, &group); err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, rows.Err()
}

func (s *Store) GetProxyGroup(ctx context.Context, id int64) (model.ProxyGroup, error) {
	var group model.ProxyGroup
	row := s.db.QueryRowContext(ctx, `
		SELECT
			pg.id, pg.name, pg.type, pg.api_provider, pg.api_config,
			pg.last_pull_status, pg.last_pull_message, pg.last_pulled_at,
			pg.last_test_status, pg.last_test_message, pg.last_tested_at,
			COUNT(DISTINCT pn.id),
			COUNT(DISTINCT CASE WHEN pn.last_test_status = 'success' THEN pn.id END),
			COALESCE(MAX(CASE WHEN t.status IN ('waiting_start', 'running') THEN 1 ELSE 0 END), 0),
			pg.created_at, pg.updated_at
		FROM proxy_groups pg
		LEFT JOIN proxy_nodes pn ON pn.group_id = pg.id
		LEFT JOIN tasks t ON t.proxy_group_id = pg.id
		WHERE pg.id = ?
		GROUP BY pg.id
	`, id)
	err := scanProxyGroup(row, &group)
	return group, err
}

func (s *Store) CreateProxyGroup(ctx context.Context, input model.ProxyGroupInput) (model.ProxyGroup, error) {
	now := nowText()
	input = normalizeProxyGroupInput(input)
	config, err := marshalJSON(input.APIConfig, "{}")
	if err != nil {
		return model.ProxyGroup{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO proxy_groups (name, type, api_provider, api_config, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)
	`, strings.TrimSpace(input.Name), input.Type, input.APIProvider, config, now, now)
	if err != nil {
		return model.ProxyGroup{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.ProxyGroup{}, err
	}
	return s.GetProxyGroup(ctx, id)
}

func (s *Store) UpdateProxyGroup(ctx context.Context, id int64, input model.ProxyGroupInput) (model.ProxyGroup, error) {
	now := nowText()
	input = normalizeProxyGroupInput(input)
	config, err := marshalJSON(input.APIConfig, "{}")
	if err != nil {
		return model.ProxyGroup{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE proxy_groups
		SET name = ?, type = ?, api_provider = ?, api_config = ?, updated_at = ?
		WHERE id = ?
	`, strings.TrimSpace(input.Name), input.Type, input.APIProvider, config, now, id)
	if err != nil {
		return model.ProxyGroup{}, err
	}
	return s.GetProxyGroup(ctx, id)
}

func (s *Store) DeleteProxyGroup(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM proxy_nodes WHERE group_id = ?`, id)
	if err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, `DELETE FROM proxy_groups WHERE id = ?`, id)
	return err
}

func (s *Store) ProxyGroupInUse(ctx context.Context, id int64) (bool, error) {
	var count int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*)
		FROM tasks
		WHERE proxy_group_id = ? AND status IN ('waiting_start', 'running')
	`, id).Scan(&count)
	return count > 0, err
}

func (s *Store) ListProxyNodes(ctx context.Context, groupID int64) ([]model.ProxyNode, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, group_id, name, protocol, host, port, username, password, source,
			last_test_status, last_test_message, last_test_latency_ms, last_test_ip_location,
			last_tested_at, created_at, updated_at
		FROM proxy_nodes
		WHERE group_id = ?
		ORDER BY id ASC
	`, groupID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	nodes := make([]model.ProxyNode, 0)
	for rows.Next() {
		var node model.ProxyNode
		if err := scanProxyNode(rows, &node); err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, rows.Err()
}

func (s *Store) GetProxyNode(ctx context.Context, id int64) (model.ProxyNode, error) {
	var node model.ProxyNode
	row := s.db.QueryRowContext(ctx, `
		SELECT id, group_id, name, protocol, host, port, username, password, source,
			last_test_status, last_test_message, last_test_latency_ms, last_test_ip_location,
			last_tested_at, created_at, updated_at
		FROM proxy_nodes
		WHERE id = ?
	`, id)
	err := scanProxyNode(row, &node)
	return node, err
}

func (s *Store) CreateProxyNode(ctx context.Context, groupID int64, input model.ProxyNodeInput) (model.ProxyNode, error) {
	return s.createProxyNodeWithSource(ctx, groupID, input, model.ProxyNodeSourceManual)
}

func (s *Store) createProxyNodeWithSource(ctx context.Context, groupID int64, input model.ProxyNodeInput, source string) (model.ProxyNode, error) {
	now := nowText()
	input = normalizeProxyNodeInput(input)
	source = model.NormalizeProxyNodeSource(source)
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO proxy_nodes (group_id, name, protocol, host, port, username, password, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, groupID, strings.TrimSpace(input.Name), input.Protocol, strings.TrimSpace(input.Host), input.Port, strings.TrimSpace(input.Username), strings.TrimSpace(input.Password), source, now, now)
	if err != nil {
		return model.ProxyNode{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.ProxyNode{}, err
	}
	return s.GetProxyNode(ctx, id)
}

func (s *Store) UpdateProxyNode(ctx context.Context, id int64, input model.ProxyNodeInput) (model.ProxyNode, error) {
	now := nowText()
	input = normalizeProxyNodeInput(input)
	_, err := s.db.ExecContext(ctx, `
		UPDATE proxy_nodes
		SET name = ?, protocol = ?, host = ?, port = ?, username = ?, password = ?, updated_at = ?
		WHERE id = ?
	`, strings.TrimSpace(input.Name), input.Protocol, strings.TrimSpace(input.Host), input.Port, strings.TrimSpace(input.Username), strings.TrimSpace(input.Password), now, id)
	if err != nil {
		return model.ProxyNode{}, err
	}
	return s.GetProxyNode(ctx, id)
}

func (s *Store) DeleteProxyNode(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM proxy_nodes WHERE id = ?`, id)
	return err
}

func (s *Store) ReplaceAPIProxyNodes(ctx context.Context, groupID int64, nodes []model.ProxyNodeInput) ([]model.ProxyNode, error) {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM proxy_nodes WHERE group_id = ? AND source = ?`, groupID, model.ProxyNodeSourceAPI); err != nil {
		return nil, err
	}
	created := make([]model.ProxyNode, 0, len(nodes))
	for _, input := range nodes {
		node, err := s.createProxyNodeWithSource(ctx, groupID, input, model.ProxyNodeSourceAPI)
		if err != nil {
			return nil, err
		}
		created = append(created, node)
	}
	return created, nil
}

func (s *Store) SetProxyNodeTestResult(ctx context.Context, id int64, status string, message string, latencyMillis int64, ipLocation string) (model.ProxyNode, error) {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
		UPDATE proxy_nodes
		SET last_test_status = ?, last_test_message = ?, last_test_latency_ms = ?, last_test_ip_location = ?, last_tested_at = ?, updated_at = ?
		WHERE id = ?
	`, strings.TrimSpace(status), strings.TrimSpace(message), latencyMillis, strings.TrimSpace(ipLocation), now, now, id)
	if err != nil {
		return model.ProxyNode{}, err
	}
	return s.GetProxyNode(ctx, id)
}

func (s *Store) SetProxyGroupPullResult(ctx context.Context, id int64, status string, message string) (model.ProxyGroup, error) {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
		UPDATE proxy_groups
		SET last_pull_status = ?, last_pull_message = ?, last_pulled_at = ?, updated_at = ?
		WHERE id = ?
	`, strings.TrimSpace(status), strings.TrimSpace(message), now, now, id)
	if err != nil {
		return model.ProxyGroup{}, err
	}
	return s.GetProxyGroup(ctx, id)
}

func (s *Store) SetProxyGroupTestResult(ctx context.Context, id int64, status string, message string) (model.ProxyGroup, error) {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
		UPDATE proxy_groups
		SET last_test_status = ?, last_test_message = ?, last_tested_at = ?, updated_at = ?
		WHERE id = ?
	`, strings.TrimSpace(status), strings.TrimSpace(message), now, now, id)
	if err != nil {
		return model.ProxyGroup{}, err
	}
	return s.GetProxyGroup(ctx, id)
}

func (s *Store) ListTasks(ctx context.Context) ([]model.Task, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT
			t.id, t.name, t.account_id, COALESCE(a.name, ''), t.proxy_group_id, COALESCE(pg.name, ''),
			t.project_id, t.project_name, t.screen_id, t.sku_id,
			t.session_name, t.ticket_level, t.ticket_display, t.ticket_price,
				t.sale_start, t.sale_status, t.link_id, t.is_hot_project,
			t.task_mode, t.duration_mode, t.selected_tickets, t.rush_duration_seconds,
			t.order_type, t.pay_money, t.buyer_info, t.buyer, t.tel, t.deliver_info, t.phone,
			t.order_id, t.payment_url, t.payment_qr_image_data_url, t.last_checked_at,
			t.time_sync_strategy, t.time_offset_ms, t.time_synced_at,
			t.quantity, t.start_at, t.end_at,
			t.poll_interval_ms, t.status, t.last_message, t.created_at, t.updated_at
		FROM tasks t
		LEFT JOIN accounts a ON a.id = t.account_id
		LEFT JOIN proxy_groups pg ON pg.id = t.proxy_group_id
		ORDER BY t.updated_at DESC, t.id DESC
	`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	tasks := make([]model.Task, 0)
	for rows.Next() {
		var task model.Task
		if err := scanTask(rows, &task); err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}

	return tasks, rows.Err()
}

func (s *Store) CreateTask(ctx context.Context, input model.TaskInput) (model.Task, error) {
	now := nowText()
	input = normalizeTaskInput(input)
	buyerInfo, err := marshalJSON(input.BuyerInfo, "[]")
	if err != nil {
		return model.Task{}, err
	}
	selectedTickets, err := marshalJSON(input.SelectedTickets, "[]")
	if err != nil {
		return model.Task{}, err
	}
	deliverInfo, err := marshalJSON(input.DeliverInfo, "{}")
	if err != nil {
		return model.Task{}, err
	}
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO tasks (
			name, account_id, proxy_group_id, project_id, project_name, screen_id, sku_id,
				session_name, ticket_level, ticket_display, ticket_price,
				sale_start, sale_status, link_id, is_hot_project,
				task_mode, duration_mode, selected_tickets, rush_duration_seconds,
				order_type, pay_money, buyer_info, buyer, tel, deliver_info, phone,
			time_sync_strategy,
			quantity, start_at, end_at, poll_interval_ms,
			status, last_message, created_at, updated_at
		)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'draft', '任务已创建，等待下发。', ?, ?)
		`, strings.TrimSpace(input.Name), input.AccountID, input.ProxyGroupID, input.ProjectID, strings.TrimSpace(input.ProjectName), input.ScreenID, input.SKUID, strings.TrimSpace(input.SessionName), strings.TrimSpace(input.TicketLevel), strings.TrimSpace(input.TicketDisplay), input.TicketPrice, strings.TrimSpace(input.SaleStart), strings.TrimSpace(input.SaleStatus), input.LinkID, boolToInt(input.IsHotProject), input.TaskMode, input.DurationMode, selectedTickets, input.RushDurationSeconds, input.OrderType, input.PayMoney, buyerInfo, strings.TrimSpace(input.Buyer), strings.TrimSpace(input.Tel), deliverInfo, strings.TrimSpace(input.Phone), input.TimeSyncStrategy, input.Quantity, strings.TrimSpace(input.StartAt), strings.TrimSpace(input.EndAt), input.PollIntervalMillis, now, now)
	if err != nil {
		return model.Task{}, err
	}

	id, err := result.LastInsertId()
	if err != nil {
		return model.Task{}, err
	}
	if _, err := s.AddTaskLog(ctx, id, "info", "任务已创建。"); err != nil {
		return model.Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) GetTask(ctx context.Context, id int64) (model.Task, error) {
	var task model.Task
	row := s.db.QueryRowContext(ctx, `
		SELECT
			t.id, t.name, t.account_id, COALESCE(a.name, ''), t.proxy_group_id, COALESCE(pg.name, ''),
			t.project_id, t.project_name, t.screen_id, t.sku_id,
			t.session_name, t.ticket_level, t.ticket_display, t.ticket_price,
				t.sale_start, t.sale_status, t.link_id, t.is_hot_project,
			t.task_mode, t.duration_mode, t.selected_tickets, t.rush_duration_seconds,
			t.order_type, t.pay_money, t.buyer_info, t.buyer, t.tel, t.deliver_info, t.phone,
			t.order_id, t.payment_url, t.payment_qr_image_data_url, t.last_checked_at,
			t.time_sync_strategy, t.time_offset_ms, t.time_synced_at,
			t.quantity, t.start_at, t.end_at,
			t.poll_interval_ms, t.status, t.last_message, t.created_at, t.updated_at
		FROM tasks t
		LEFT JOIN accounts a ON a.id = t.account_id
		LEFT JOIN proxy_groups pg ON pg.id = t.proxy_group_id
		WHERE t.id = ?
	`, id)
	err := scanTask(row, &task)
	return task, err
}

func (s *Store) UpdateTask(ctx context.Context, id int64, input model.TaskInput) (model.Task, error) {
	now := nowText()
	input = normalizeTaskInput(input)
	buyerInfo, err := marshalJSON(input.BuyerInfo, "[]")
	if err != nil {
		return model.Task{}, err
	}
	selectedTickets, err := marshalJSON(input.SelectedTickets, "[]")
	if err != nil {
		return model.Task{}, err
	}
	deliverInfo, err := marshalJSON(input.DeliverInfo, "{}")
	if err != nil {
		return model.Task{}, err
	}
	_, err = s.db.ExecContext(ctx, `
		UPDATE tasks
		SET name = ?, account_id = ?, proxy_group_id = ?, project_id = ?, project_name = ?, screen_id = ?, sku_id = ?,
			session_name = ?, ticket_level = ?, ticket_display = ?, ticket_price = ?,
			sale_start = ?, sale_status = ?, link_id = ?, is_hot_project = ?,
			task_mode = ?, duration_mode = ?, selected_tickets = ?, rush_duration_seconds = ?,
			order_type = ?, pay_money = ?, buyer_info = ?, buyer = ?, tel = ?, deliver_info = ?, phone = ?,
			time_sync_strategy = ?,
			quantity = ?, start_at = ?, end_at = ?, poll_interval_ms = ?, updated_at = ?
		WHERE id = ?
	`, strings.TrimSpace(input.Name), input.AccountID, input.ProxyGroupID, input.ProjectID, strings.TrimSpace(input.ProjectName), input.ScreenID, input.SKUID, strings.TrimSpace(input.SessionName), strings.TrimSpace(input.TicketLevel), strings.TrimSpace(input.TicketDisplay), input.TicketPrice, strings.TrimSpace(input.SaleStart), strings.TrimSpace(input.SaleStatus), input.LinkID, boolToInt(input.IsHotProject), input.TaskMode, input.DurationMode, selectedTickets, input.RushDurationSeconds, input.OrderType, input.PayMoney, buyerInfo, strings.TrimSpace(input.Buyer), strings.TrimSpace(input.Tel), deliverInfo, strings.TrimSpace(input.Phone), input.TimeSyncStrategy, input.Quantity, strings.TrimSpace(input.StartAt), strings.TrimSpace(input.EndAt), input.PollIntervalMillis, now, id)
	if err != nil {
		return model.Task{}, err
	}
	if _, err := s.AddTaskLog(ctx, id, "info", "任务配置已更新。"); err != nil {
		return model.Task{}, err
	}
	return s.GetTask(ctx, id)
}

func (s *Store) DeleteTask(ctx context.Context, id int64) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM tasks WHERE id = ?`, id)
	return err
}

func (s *Store) PauseInterruptedTasks(ctx context.Context) ([]model.Task, error) {
	tasks, _, err := s.PauseActiveTasks(ctx, interruptedTaskMessage)
	return tasks, err
}

func (s *Store) PauseActiveTasks(ctx context.Context, message string) ([]model.Task, []model.TaskLog, error) {
	message = strings.TrimSpace(message)
	if message == "" {
		message = "任务已停止。"
	}
	rows, err := s.db.QueryContext(ctx, `
		SELECT id
		FROM tasks
		WHERE status IN ('waiting_start', 'running')
		ORDER BY id ASC
	`)
	if err != nil {
		return nil, nil, err
	}
	defer rows.Close()

	ids := make([]int64, 0)
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, nil, err
	}

	tasks := make([]model.Task, 0, len(ids))
	logs := make([]model.TaskLog, 0, len(ids))
	for _, id := range ids {
		task, log, err := s.SetTaskRuntime(ctx, id, model.TaskRuntimeUpdate{
			Status:      "paused",
			LastMessage: message,
		}, "warn")
		if err != nil {
			return nil, nil, err
		}
		tasks = append(tasks, task)
		logs = append(logs, log)
	}
	return tasks, logs, nil
}

func (s *Store) SetTaskStatus(ctx context.Context, id int64, status string, message string, level string) (model.Task, error) {
	task, _, err := s.SetTaskRuntime(ctx, id, model.TaskRuntimeUpdate{
		Status:      status,
		LastMessage: message,
	}, level)
	return task, err
}

func (s *Store) SetTaskRuntime(ctx context.Context, id int64, update model.TaskRuntimeUpdate, level string) (model.Task, model.TaskLog, error) {
	now := nowText()
	lastCheckedAt := strings.TrimSpace(update.LastCheckedAt)
	if lastCheckedAt == "" {
		lastCheckedAt = time.Now().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks
		SET status = COALESCE(NULLIF(?, ''), status),
			last_message = COALESCE(NULLIF(?, ''), last_message),
			order_id = COALESCE(NULLIF(?, ''), order_id),
			payment_url = COALESCE(NULLIF(?, ''), payment_url),
			payment_qr_image_data_url = COALESCE(NULLIF(?, ''), payment_qr_image_data_url),
			last_checked_at = ?,
			updated_at = ?
		WHERE id = ?
	`, update.Status, update.LastMessage, update.OrderID, update.PaymentURL, update.PaymentQRImageDataURL, lastCheckedAt, now, id)
	if err != nil {
		return model.Task{}, model.TaskLog{}, err
	}
	var log model.TaskLog
	if strings.TrimSpace(update.LastMessage) != "" {
		if level == "" {
			level = "info"
		}
		log, err = s.AddTaskLog(ctx, id, level, update.LastMessage)
		if err != nil {
			return model.Task{}, model.TaskLog{}, err
		}
	}
	task, err := s.GetTask(ctx, id)
	if err != nil {
		return model.Task{}, model.TaskLog{}, err
	}
	return task, log, nil
}

func (s *Store) SetTaskTimeSync(ctx context.Context, id int64, strategy string, offsetMillis int64, syncedAt string, message string) (model.Task, model.TaskLog, error) {
	now := nowText()
	strategy = model.NormalizeTimeSyncStrategy(strategy)
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks
		SET time_sync_strategy = ?,
			time_offset_ms = ?,
			time_synced_at = ?,
			last_message = COALESCE(NULLIF(?, ''), last_message),
			updated_at = ?
		WHERE id = ?
	`, strategy, offsetMillis, strings.TrimSpace(syncedAt), strings.TrimSpace(message), now, id)
	if err != nil {
		return model.Task{}, model.TaskLog{}, err
	}

	var log model.TaskLog
	if strings.TrimSpace(message) != "" {
		log, err = s.AddTaskLog(ctx, id, "info", message)
		if err != nil {
			return model.Task{}, model.TaskLog{}, err
		}
	}

	task, err := s.GetTask(ctx, id)
	if err != nil {
		return model.Task{}, model.TaskLog{}, err
	}
	return task, log, nil
}

func (s *Store) SetTaskPayMoney(ctx context.Context, id int64, payMoney int64, message string) (model.Task, model.TaskLog, error) {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks
		SET pay_money = ?,
			last_message = COALESCE(NULLIF(?, ''), last_message),
			updated_at = ?
		WHERE id = ?
	`, payMoney, strings.TrimSpace(message), now, id)
	if err != nil {
		return model.Task{}, model.TaskLog{}, err
	}

	var log model.TaskLog
	if strings.TrimSpace(message) != "" {
		log, err = s.AddTaskLog(ctx, id, "warn", message)
		if err != nil {
			return model.Task{}, model.TaskLog{}, err
		}
	}

	task, err := s.GetTask(ctx, id)
	if err != nil {
		return model.Task{}, model.TaskLog{}, err
	}
	return task, log, nil
}

func (s *Store) SetTaskHotProject(ctx context.Context, id int64, isHotProject bool, message string) (model.Task, model.TaskLog, error) {
	now := nowText()
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks
		SET is_hot_project = ?,
			last_message = COALESCE(NULLIF(?, ''), last_message),
			updated_at = ?
		WHERE id = ?
	`, boolToInt(isHotProject), strings.TrimSpace(message), now, id)
	if err != nil {
		return model.Task{}, model.TaskLog{}, err
	}

	var log model.TaskLog
	if strings.TrimSpace(message) != "" {
		log, err = s.AddTaskLog(ctx, id, "info", message)
		if err != nil {
			return model.Task{}, model.TaskLog{}, err
		}
	}

	task, err := s.GetTask(ctx, id)
	if err != nil {
		return model.Task{}, model.TaskLog{}, err
	}
	return task, log, nil
}

func (s *Store) SetTaskMatchedTicket(ctx context.Context, id int64, ticket model.TicketOption, quantity int, message string, checkedAt string) (model.Task, model.TaskLog, error) {
	now := nowText()
	if quantity <= 0 {
		quantity = 1
	}
	payMoney := ticket.Price * int64(quantity)
	if checkedAt == "" {
		checkedAt = time.Now().Format(time.RFC3339Nano)
	}
	_, err := s.db.ExecContext(ctx, `
		UPDATE tasks
		SET project_id = ?,
			screen_id = ?,
			sku_id = ?,
			session_name = ?,
			ticket_level = ?,
			ticket_display = ?,
			ticket_price = ?,
			sale_start = ?,
			sale_status = ?,
			link_id = ?,
			is_hot_project = ?,
			pay_money = ?,
			last_message = COALESCE(NULLIF(?, ''), last_message),
			last_checked_at = ?,
			updated_at = ?
		WHERE id = ?
	`, ticket.ProjectID, ticket.ScreenID, ticket.SKUID, strings.TrimSpace(ticket.ScreenName), strings.TrimSpace(ticket.TicketLevel), strings.TrimSpace(ticket.Display), ticket.Price, strings.TrimSpace(ticket.SaleStart), strings.TrimSpace(ticket.SaleStatus), ticket.LinkID, boolToInt(ticket.IsHotProject), payMoney, strings.TrimSpace(message), checkedAt, now, id)
	if err != nil {
		return model.Task{}, model.TaskLog{}, err
	}

	var log model.TaskLog
	if strings.TrimSpace(message) != "" {
		log, err = s.AddTaskLog(ctx, id, "info", message)
		if err != nil {
			return model.Task{}, model.TaskLog{}, err
		}
	}

	task, err := s.GetTask(ctx, id)
	if err != nil {
		return model.Task{}, model.TaskLog{}, err
	}
	return task, log, nil
}

func (s *Store) AddTaskLog(ctx context.Context, taskID int64, level string, message string) (model.TaskLog, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO task_logs (task_id, level, message, created_at)
		VALUES (?, ?, ?, ?)
	`, taskID, level, message, nowText())
	if err != nil {
		return model.TaskLog{}, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return model.TaskLog{}, err
	}
	return s.GetTaskLog(ctx, id)
}

func (s *Store) GetTaskLog(ctx context.Context, id int64) (model.TaskLog, error) {
	var log model.TaskLog
	err := s.db.QueryRowContext(ctx, `
		SELECT id, task_id, level, message, created_at
		FROM task_logs
		WHERE id = ?
	`, id).Scan(&log.ID, &log.TaskID, &log.Level, &log.Message, &log.CreatedAt)
	return log, err
}

func (s *Store) ListTaskLogs(ctx context.Context, taskID int64) ([]model.TaskLog, error) {
	query := `
		SELECT id, task_id, level, message, created_at
		FROM task_logs
	`
	args := []any{}
	if taskID > 0 {
		query += ` WHERE task_id = ?`
		args = append(args, taskID)
	}
	query += ` ORDER BY id DESC LIMIT 200`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	logs := make([]model.TaskLog, 0)
	for rows.Next() {
		var log model.TaskLog
		if err := rows.Scan(&log.ID, &log.TaskID, &log.Level, &log.Message, &log.CreatedAt); err != nil {
			return nil, err
		}
		logs = append(logs, log)
	}
	return logs, rows.Err()
}

func (s *Store) ensureColumn(ctx context.Context, table string, column string, definition string) error {
	rows, err := s.db.QueryContext(ctx, fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return err
	}
	defer rows.Close()

	for rows.Next() {
		var cid int
		var name string
		var dataType string
		var notNull int
		var defaultValue any
		var primaryKey int
		if err := rows.Scan(&cid, &name, &dataType, &notNull, &defaultValue, &primaryKey); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = s.db.ExecContext(ctx, fmt.Sprintf(`ALTER TABLE %s ADD COLUMN %s %s`, table, column, definition))
	return err
}

func nowText() string {
	return time.Now().Format(time.RFC3339)
}

func accountStatus(cookie string) string {
	if strings.TrimSpace(cookie) == "" {
		return "missing_cookie"
	}
	return "configured"
}

func maskCookie(cookie string) string {
	cookie = strings.TrimSpace(cookie)
	if cookie == "" {
		return ""
	}
	if len(cookie) <= 12 {
		return "***"
	}
	return fmt.Sprintf("%s...%s", cookie[:6], cookie[len(cookie)-6:])
}

type taskScanner interface {
	Scan(dest ...any) error
}

func scanNotification(scanner taskScanner, notification *model.Notification) error {
	var config string
	var enabled int
	if err := scanner.Scan(
		&notification.ID,
		&notification.Name,
		&notification.Provider,
		&config,
		&enabled,
		&notification.LastTestStatus,
		&notification.LastTestMessage,
		&notification.LastTestedAt,
		&notification.CreatedAt,
		&notification.UpdatedAt,
	); err != nil {
		return err
	}
	notification.Provider = model.NormalizeNotificationProvider(notification.Provider)
	notification.Enabled = enabled != 0
	if err := unmarshalJSON(config, &notification.Config); err != nil {
		return err
	}
	if notification.Config == nil {
		notification.Config = map[string]string{}
	}
	return nil
}

func scanProxyGroup(scanner taskScanner, group *model.ProxyGroup) error {
	var config string
	var inUse int
	if err := scanner.Scan(
		&group.ID,
		&group.Name,
		&group.Type,
		&group.APIProvider,
		&config,
		&group.LastPullStatus,
		&group.LastPullMessage,
		&group.LastPulledAt,
		&group.LastTestStatus,
		&group.LastTestMessage,
		&group.LastTestedAt,
		&group.NodeCount,
		&group.AvailableNodeCount,
		&inUse,
		&group.CreatedAt,
		&group.UpdatedAt,
	); err != nil {
		return err
	}
	group.Type = model.NormalizeProxyGroupType(group.Type)
	group.APIProvider = model.NormalizeProxyProvider(group.APIProvider)
	group.InUse = inUse != 0
	if err := unmarshalJSON(config, &group.APIConfig); err != nil {
		return err
	}
	if group.APIConfig == nil {
		group.APIConfig = map[string]string{}
	}
	return nil
}

func scanProxyNode(scanner taskScanner, node *model.ProxyNode) error {
	if err := scanner.Scan(
		&node.ID,
		&node.GroupID,
		&node.Name,
		&node.Protocol,
		&node.Host,
		&node.Port,
		&node.Username,
		&node.Password,
		&node.Source,
		&node.LastTestStatus,
		&node.LastTestMessage,
		&node.LastTestLatencyMillis,
		&node.LastTestIPLocation,
		&node.LastTestedAt,
		&node.CreatedAt,
		&node.UpdatedAt,
	); err != nil {
		return err
	}
	node.Protocol = model.NormalizeProxyProtocol(node.Protocol)
	node.Source = model.NormalizeProxyNodeSource(node.Source)
	return nil
}

func scanTask(scanner taskScanner, task *model.Task) error {
	var isHotProject int
	var buyerInfo string
	var selectedTickets string
	var deliverInfo string
	if err := scanner.Scan(
		&task.ID,
		&task.Name,
		&task.AccountID,
		&task.AccountName,
		&task.ProxyGroupID,
		&task.ProxyGroupName,
		&task.ProjectID,
		&task.ProjectName,
		&task.ScreenID,
		&task.SKUID,
		&task.SessionName,
		&task.TicketLevel,
		&task.TicketDisplay,
		&task.TicketPrice,
		&task.SaleStart,
		&task.SaleStatus,
		&task.LinkID,
		&isHotProject,
		&task.TaskMode,
		&task.DurationMode,
		&selectedTickets,
		&task.RushDurationSeconds,
		&task.OrderType,
		&task.PayMoney,
		&buyerInfo,
		&task.Buyer,
		&task.Tel,
		&deliverInfo,
		&task.Phone,
		&task.OrderID,
		&task.PaymentURL,
		&task.PaymentQRImageDataURL,
		&task.LastCheckedAt,
		&task.TimeSyncStrategy,
		&task.TimeOffsetMillis,
		&task.TimeSyncedAt,
		&task.Quantity,
		&task.StartAt,
		&task.EndAt,
		&task.PollIntervalMillis,
		&task.Status,
		&task.LastMessage,
		&task.CreatedAt,
		&task.UpdatedAt,
	); err != nil {
		return err
	}
	task.IsHotProject = isHotProject != 0
	task.TaskMode = model.NormalizeTaskMode(task.TaskMode)
	task.DurationMode = model.NormalizeDurationMode(task.DurationMode)
	if task.RushDurationSeconds <= 0 {
		task.RushDurationSeconds = model.DefaultRushDurationSeconds
	}
	if err := unmarshalJSON(selectedTickets, &task.SelectedTickets); err != nil {
		return err
	}
	if err := unmarshalJSON(buyerInfo, &task.BuyerInfo); err != nil {
		return err
	}
	var address model.TicketAddress
	if err := unmarshalJSON(deliverInfo, &address); err != nil {
		return err
	}
	if address.ID != 0 || address.Name != "" || address.Phone != "" || address.FullAddress != "" {
		task.DeliverInfo = &address
	}
	return nil
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

func normalizeTaskInput(input model.TaskInput) model.TaskInput {
	input.TimeSyncStrategy = model.NormalizeTimeSyncStrategy(input.TimeSyncStrategy)
	input.TaskMode = model.NormalizeTaskMode(input.TaskMode)
	input.DurationMode = model.NormalizeDurationMode(input.DurationMode)
	if input.TaskMode == model.TaskModeRestock {
		input.ProxyGroupID = 0
	}
	if input.RushDurationSeconds <= 0 {
		input.RushDurationSeconds = model.DefaultRushDurationSeconds
	}
	if input.OrderType <= 0 {
		input.OrderType = 1
	}
	if len(input.BuyerInfo) > 0 {
		input.Quantity = len(input.BuyerInfo)
	}
	if input.Quantity <= 0 {
		input.Quantity = 1
	}
	if input.PayMoney <= 0 && input.TicketPrice > 0 {
		input.PayMoney = input.TicketPrice * int64(input.Quantity)
	}
	if input.PollIntervalMillis <= 0 {
		input.PollIntervalMillis = 1000
	}
	return input
}

func normalizeNotificationInput(input model.NotificationInput) model.NotificationInput {
	input.Provider = model.NormalizeNotificationProvider(input.Provider)
	if input.Config == nil {
		input.Config = map[string]string{}
	}
	normalizedConfig := make(map[string]string, len(input.Config))
	for key, value := range input.Config {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		normalizedConfig[key] = strings.TrimSpace(value)
	}
	input.Config = normalizedConfig
	return input
}

func normalizeProxyGroupInput(input model.ProxyGroupInput) model.ProxyGroupInput {
	input.Type = model.NormalizeProxyGroupType(input.Type)
	if input.Type == model.ProxyGroupTypeAPI {
		input.APIProvider = model.NormalizeProxyProvider(input.APIProvider)
	} else {
		input.APIProvider = ""
		input.APIConfig = map[string]string{}
	}
	if input.APIConfig == nil {
		input.APIConfig = map[string]string{}
	}
	normalizedConfig := make(map[string]string, len(input.APIConfig))
	for key, value := range input.APIConfig {
		key = strings.TrimSpace(key)
		if key == "" {
			continue
		}
		normalizedConfig[key] = strings.TrimSpace(value)
	}
	input.APIConfig = normalizedConfig
	return input
}

func normalizeProxyNodeInput(input model.ProxyNodeInput) model.ProxyNodeInput {
	input.Protocol = model.NormalizeProxyProtocol(input.Protocol)
	input.Host = strings.TrimSpace(input.Host)
	input.Username = strings.TrimSpace(input.Username)
	input.Password = strings.TrimSpace(input.Password)
	return input
}

func marshalJSON(value any, fallback string) (string, error) {
	if value == nil {
		return fallback, nil
	}
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	if len(data) == 0 || string(data) == "null" {
		return fallback, nil
	}
	return string(data), nil
}

func unmarshalJSON(raw string, target any) error {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	return json.Unmarshal([]byte(raw), target)
}
