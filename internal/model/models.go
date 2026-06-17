package model

import "strings"

const (
	TimeSyncStrategyBilibili = "bilibili"
	TimeSyncStrategyLocal    = "local"

	NotificationProviderPushPlus = "pushplus"
	NotificationProviderBark     = "bark"

	TaskModeRush    = "rush"
	TaskModeRestock = "restock"
	TaskModeHybrid  = "rush_restock"

	DurationModeLimited   = "limited"
	DurationModeUnlimited = "unlimited"

	DefaultRushDurationSeconds = 600
)

type Health struct {
	Status   string `json:"status"`
	Database string `json:"database"`
	Time     string `json:"time"`
}

type SessionSummary struct {
	Status             string `json:"status"`
	Message            string `json:"message"`
	AccountCount       int64  `json:"accountCount"`
	ConfiguredAccounts int64  `json:"configuredAccounts"`
	VerifiedAccounts   int64  `json:"verifiedAccounts"`
}

type Account struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	CookiePreview string `json:"cookiePreview"`
	HasCookie     bool   `json:"hasCookie"`
	Status        string `json:"status"`
	Note          string `json:"note"`
	CreatedAt     string `json:"createdAt"`
	UpdatedAt     string `json:"updatedAt"`
}

type AccountInput struct {
	Name   string `json:"name"`
	Cookie string `json:"cookie"`
	Note   string `json:"note"`
}

type AccountCookieResponse struct {
	AccountID     int64  `json:"accountId"`
	Cookie        string `json:"cookie"`
	CookiePreview string `json:"cookiePreview"`
}

type QRLoginStartResponse struct {
	OK               bool   `json:"ok"`
	LoginURL         string `json:"loginUrl"`
	QRCodeKey        string `json:"qrcodeKey"`
	QRImageDataURL   string `json:"qrImageDataUrl"`
	ExpiresInSeconds int    `json:"expiresInSeconds"`
	NextAction       string `json:"nextAction"`
}

type QRLoginPollInput struct {
	QRCodeKey   string `json:"qrcodeKey"`
	AccountName string `json:"accountName"`
	Note        string `json:"note"`
}

type QRLoginPollResponse struct {
	OK       bool     `json:"ok"`
	Status   string   `json:"status"`
	Message  string   `json:"message"`
	Code     int      `json:"code,omitempty"`
	Username string   `json:"username,omitempty"`
	Account  *Account `json:"account,omitempty"`
}

type CookieLoginInput struct {
	Name   string `json:"name"`
	Cookie string `json:"cookie"`
	Note   string `json:"note"`
}

type CookieLoginResponse struct {
	OK       bool     `json:"ok"`
	LoggedIn bool     `json:"loggedIn"`
	Username string   `json:"username,omitempty"`
	Message  string   `json:"message"`
	Account  *Account `json:"account,omitempty"`
}

type AccountVerifyResponse struct {
	OK        bool     `json:"ok"`
	LoggedIn  bool     `json:"loggedIn"`
	AccountID int64    `json:"accountId"`
	Username  string   `json:"username,omitempty"`
	Message   string   `json:"message"`
	Account   *Account `json:"account,omitempty"`
}

type PanelLoginInput struct {
	Password string `json:"password"`
}

type PanelAuthResponse struct {
	Token     string `json:"token,omitempty"`
	ExpiresAt string `json:"expiresAt"`
}

type Notification struct {
	ID              int64             `json:"id"`
	Name            string            `json:"name"`
	Provider        string            `json:"provider"`
	Config          map[string]string `json:"config"`
	Enabled         bool              `json:"enabled"`
	LastTestStatus  string            `json:"lastTestStatus"`
	LastTestMessage string            `json:"lastTestMessage"`
	LastTestedAt    string            `json:"lastTestedAt"`
	CreatedAt       string            `json:"createdAt"`
	UpdatedAt       string            `json:"updatedAt"`
}

type NotificationInput struct {
	Name     string            `json:"name"`
	Provider string            `json:"provider"`
	Config   map[string]string `json:"config"`
}

type TicketProjectHistory struct {
	ProjectID    int64  `json:"projectId"`
	ProjectName  string `json:"projectName"`
	VenueName    string `json:"venueName"`
	VenueAddress string `json:"venueAddress"`
	StartAt      string `json:"startAt"`
	EndAt        string `json:"endAt"`
	UpdatedAt    string `json:"updatedAt"`
}

type TicketProjectFetchInput struct {
	ProjectInput string `json:"projectInput"`
	AccountID    int64  `json:"accountId"`
}

type TicketAccountContextInput struct {
	ProjectInput string `json:"projectInput"`
	AccountID    int64  `json:"accountId"`
}

type TicketAccountContext struct {
	ProjectID int64           `json:"projectId"`
	Username  string          `json:"username"`
	Phone     string          `json:"phone"`
	Buyers    []TicketBuyer   `json:"buyers"`
	Addresses []TicketAddress `json:"addresses"`
}

type TicketProject struct {
	ProjectID     int64           `json:"projectId"`
	ProjectName   string          `json:"projectName"`
	ProjectURL    string          `json:"projectUrl"`
	Username      string          `json:"username"`
	Phone         string          `json:"phone"`
	VenueName     string          `json:"venueName"`
	VenueAddress  string          `json:"venueAddress"`
	StartAt       string          `json:"startAt"`
	EndAt         string          `json:"endAt"`
	IsHotProject  bool            `json:"isHotProject"`
	HasETicket    bool            `json:"hasETicket"`
	SalesDates    []string        `json:"salesDates"`
	TicketOptions []TicketOption  `json:"ticketOptions"`
	Buyers        []TicketBuyer   `json:"buyers"`
	Addresses     []TicketAddress `json:"addresses"`
}

type TicketOption struct {
	Value        string `json:"value"`
	Display      string `json:"display"`
	ProjectID    int64  `json:"projectId"`
	ScreenID     int64  `json:"screenId"`
	SKUID        int64  `json:"skuId"`
	ScreenName   string `json:"screenName"`
	TicketLevel  string `json:"ticketLevel"`
	Price        int64  `json:"price"`
	PriceText    string `json:"priceText"`
	SaleStatus   string `json:"saleStatus"`
	SaleStart    string `json:"saleStart"`
	IsHotProject bool   `json:"isHotProject"`
	LinkID       int64  `json:"linkId,omitempty"`
	Clickable    bool   `json:"clickable"`
}

type TicketBuyer struct {
	ID         int64          `json:"id,omitempty"`
	Name       string         `json:"name"`
	PersonalID string         `json:"personalId"`
	Tel        string         `json:"tel,omitempty"`
	Raw        map[string]any `json:"raw,omitempty"`
}

type TicketAddress struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Phone       string         `json:"phone"`
	Prov        string         `json:"prov"`
	City        string         `json:"city"`
	Area        string         `json:"area"`
	Addr        string         `json:"addr"`
	FullAddress string         `json:"fullAddress"`
	Raw         map[string]any `json:"raw,omitempty"`
}

type Task struct {
	ID                    int64          `json:"id"`
	Name                  string         `json:"name"`
	AccountID             int64          `json:"accountId"`
	AccountName           string         `json:"accountName"`
	ProjectID             int64          `json:"projectId"`
	ProjectName           string         `json:"projectName"`
	ScreenID              int64          `json:"screenId"`
	SKUID                 int64          `json:"skuId"`
	SessionName           string         `json:"sessionName"`
	TicketLevel           string         `json:"ticketLevel"`
	TicketDisplay         string         `json:"ticketDisplay"`
	TicketPrice           int64          `json:"ticketPrice"`
	SaleStart             string         `json:"saleStart"`
	SaleStatus            string         `json:"saleStatus"`
	LinkID                int64          `json:"linkId"`
	IsHotProject          bool           `json:"isHotProject"`
	TaskMode              string         `json:"taskMode"`
	DurationMode          string         `json:"durationMode"`
	SelectedTickets       []TicketOption `json:"selectedTickets"`
	RushDurationSeconds   int            `json:"rushDurationSeconds"`
	OrderType             int            `json:"orderType"`
	PayMoney              int64          `json:"payMoney"`
	BuyerInfo             []TicketBuyer  `json:"buyerInfo"`
	Buyer                 string         `json:"buyer"`
	Tel                   string         `json:"tel"`
	DeliverInfo           *TicketAddress `json:"deliverInfo,omitempty"`
	Phone                 string         `json:"phone"`
	OrderID               string         `json:"orderId"`
	PaymentURL            string         `json:"paymentUrl"`
	PaymentQRImageDataURL string         `json:"paymentQrImageDataUrl"`
	LastCheckedAt         string         `json:"lastCheckedAt"`
	TimeSyncStrategy      string         `json:"timeSyncStrategy"`
	TimeOffsetMillis      int64          `json:"timeOffsetMillis"`
	TimeSyncedAt          string         `json:"timeSyncedAt"`
	Quantity              int            `json:"quantity"`
	StartAt               string         `json:"startAt"`
	EndAt                 string         `json:"endAt"`
	PollIntervalMillis    int            `json:"pollIntervalMillis"`
	Status                string         `json:"status"`
	LastMessage           string         `json:"lastMessage"`
	CreatedAt             string         `json:"createdAt"`
	UpdatedAt             string         `json:"updatedAt"`
}

type TaskInput struct {
	Name                string         `json:"name"`
	AccountID           int64          `json:"accountId"`
	ProjectID           int64          `json:"projectId"`
	ProjectName         string         `json:"projectName"`
	ScreenID            int64          `json:"screenId"`
	SKUID               int64          `json:"skuId"`
	SessionName         string         `json:"sessionName"`
	TicketLevel         string         `json:"ticketLevel"`
	TicketDisplay       string         `json:"ticketDisplay"`
	TicketPrice         int64          `json:"ticketPrice"`
	SaleStart           string         `json:"saleStart"`
	SaleStatus          string         `json:"saleStatus"`
	LinkID              int64          `json:"linkId"`
	IsHotProject        bool           `json:"isHotProject"`
	TaskMode            string         `json:"taskMode"`
	DurationMode        string         `json:"durationMode"`
	SelectedTickets     []TicketOption `json:"selectedTickets"`
	RushDurationSeconds int            `json:"rushDurationSeconds"`
	OrderType           int            `json:"orderType"`
	PayMoney            int64          `json:"payMoney"`
	BuyerInfo           []TicketBuyer  `json:"buyerInfo"`
	Buyer               string         `json:"buyer"`
	Tel                 string         `json:"tel"`
	DeliverInfo         *TicketAddress `json:"deliverInfo,omitempty"`
	Phone               string         `json:"phone"`
	TimeSyncStrategy    string         `json:"timeSyncStrategy"`
	Quantity            int            `json:"quantity"`
	StartAt             string         `json:"startAt"`
	EndAt               string         `json:"endAt"`
	PollIntervalMillis  int            `json:"pollIntervalMillis"`
}

type TaskLog struct {
	ID        int64  `json:"id"`
	TaskID    int64  `json:"taskId"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	CreatedAt string `json:"createdAt"`
}

type TaskRuntimeUpdate struct {
	Status                string
	LastMessage           string
	OrderID               string
	PaymentURL            string
	PaymentQRImageDataURL string
	LastCheckedAt         string
}

type EventSnapshot struct {
	Tasks []Task    `json:"tasks"`
	Logs  []TaskLog `json:"logs"`
}

func NormalizeTimeSyncStrategy(strategy string) string {
	switch strings.ToLower(strings.TrimSpace(strategy)) {
	case TimeSyncStrategyLocal:
		return TimeSyncStrategyLocal
	default:
		return TimeSyncStrategyBilibili
	}
}

func NormalizeNotificationProvider(provider string) string {
	switch strings.ToLower(strings.TrimSpace(provider)) {
	case NotificationProviderPushPlus:
		return NotificationProviderPushPlus
	case NotificationProviderBark:
		return NotificationProviderBark
	default:
		return ""
	}
}

func NormalizeTaskMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case TaskModeRestock:
		return TaskModeRestock
	case TaskModeHybrid:
		return TaskModeHybrid
	default:
		return TaskModeRush
	}
}

func NormalizeDurationMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case DurationModeUnlimited:
		return DurationModeUnlimited
	default:
		return DurationModeLimited
	}
}
