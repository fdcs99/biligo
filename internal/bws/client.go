package bws

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fdcs99/biligo/internal/biliauth"
	"github.com/fdcs99/biligo/internal/model"
)

const (
	DefaultBaseURL      = "https://api.bilibili.com/x/activity/bws/online/park"
	DefaultReserveDates = "20260710,20260711,20260712"
	DefaultYear         = "202601"
	userAgent           = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/540.36 (KHTML, like Gecko)"
	secondPayMarker     = "预约只是签售资格，现场签售需购买up主周边。"
)

type Client struct {
	httpClient *http.Client
	baseURL    string
	rand       *rand.Rand
}

type ReserveInput struct {
	TicketNo        string
	ReserveID       int64
	Year            string
	TimestampMillis int64
}

type ReserveResult struct {
	Code    int            `json:"code"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

type apiResponse struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data"`
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 10 * time.Second}
	}
	return &Client{
		httpClient: httpClient,
		baseURL:    DefaultBaseURL,
		rand:       rand.New(rand.NewSource(time.Now().UnixNano())),
	}
}

func (c *Client) WithBaseURL(baseURL string) *Client {
	next := *c
	next.baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if next.baseURL == "" {
		next.baseURL = DefaultBaseURL
	}
	next.rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	return &next
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	next := *c
	if httpClient != nil {
		next.httpClient = httpClient
	}
	next.rand = rand.New(rand.NewSource(time.Now().UnixNano()))
	return &next
}

func (c *Client) FetchInfo(ctx context.Context, cookie string, input model.BWSInfoInput) (model.BWSReservationInfo, error) {
	csrf, err := csrfFromCookie(cookie)
	if err != nil {
		return model.BWSReservationInfo{}, err
	}
	input = normalizeInfoInput(input)
	endpoint, err := url.Parse(c.baseURL + "/reserve/info")
	if err != nil {
		return model.BWSReservationInfo{}, err
	}
	query := endpoint.Query()
	query.Set("csrf", csrf)
	query.Set("reserve_date", input.ReserveDates)
	query.Set("reserve_type", strconv.Itoa(model.BWSReserveTypeCode(input.ReserveType)))
	query.Set("year", input.Year)
	endpoint.RawQuery = query.Encode()

	var response apiResponse
	if err := c.do(ctx, http.MethodGet, endpoint.String(), cookie, nil, &response); err != nil {
		return model.BWSReservationInfo{}, err
	}
	if response.Code != 0 {
		return model.BWSReservationInfo{}, fmt.Errorf("BWS 预约信息接口返回 code=%d message=%s", response.Code, response.Message)
	}
	info, err := normalizeReservationInfo(response.Data, input)
	if err != nil {
		return model.BWSReservationInfo{}, err
	}
	return info, nil
}

func (c *Client) FetchMyReservations(ctx context.Context, cookie string, year string) (model.BWSMyReservationList, error) {
	csrf, err := csrfFromCookie(cookie)
	if err != nil {
		return model.BWSMyReservationList{}, err
	}
	year = strings.TrimSpace(year)
	if year == "" {
		year = DefaultYear
	}
	endpoint, err := url.Parse(c.baseURL + "/myreserve")
	if err != nil {
		return model.BWSMyReservationList{}, err
	}
	query := endpoint.Query()
	query.Set("csrf", csrf)
	query.Set("year", year)
	endpoint.RawQuery = query.Encode()

	var response apiResponse
	if err := c.do(ctx, http.MethodGet, endpoint.String(), cookie, nil, &response); err != nil {
		return model.BWSMyReservationList{}, err
	}
	if response.Code != 0 {
		return model.BWSMyReservationList{}, fmt.Errorf("BWS 我的预约接口返回 code=%d message=%s", response.Code, response.Message)
	}
	return normalizeMyReservations(response.Data, year)
}

func (c *Client) Reserve(ctx context.Context, cookie string, input ReserveInput) (ReserveResult, error) {
	csrf, err := csrfFromCookie(cookie)
	if err != nil {
		return ReserveResult{}, err
	}
	input.Year = strings.TrimSpace(input.Year)
	if input.Year == "" {
		input.Year = DefaultYear
	}
	if input.TimestampMillis <= 0 {
		input.TimestampMillis = time.Now().UnixMilli()
	}
	form := url.Values{}
	form.Set("ticket_no", strings.TrimSpace(input.TicketNo))
	form.Set("csrf", csrf)
	form.Set("inter_reserve_id", strconv.FormatInt(input.ReserveID, 10))
	form.Set("year", input.Year)
	form.Set("ts", strconv.FormatInt(input.TimestampMillis, 10))
	form.Set("_", strconv.Itoa(c.randomNonce()))

	var response apiResponse
	endpoint := c.baseURL + "/reserve/do"
	if err := c.do(ctx, http.MethodPost, endpoint, cookie, strings.NewReader(form.Encode()), &response, "application/x-www-form-urlencoded"); err != nil {
		var httpErr httpStatusError
		if errors.As(err, &httpErr) {
			return ReserveResult{Code: httpErr.StatusCode, Message: httpErr.Error()}, nil
		}
		return ReserveResult{Code: -1, Message: err.Error()}, err
	}
	result := ReserveResult{
		Code:    response.Code,
		Message: response.Message,
	}
	if len(response.Data) > 0 && string(response.Data) != "null" {
		_ = json.Unmarshal(response.Data, &result.Data)
	}
	return result, nil
}

func IsRetryCode(code int) bool {
	switch code {
	case 75637, -702, 429, 76650:
		return true
	default:
		return false
	}
}

func IsTerminalCode(code int) bool {
	switch code {
	case 412, 75574, 76674, 76651:
		return true
	default:
		return false
	}
}

func CodeMessage(code int, fallback string) string {
	fallback = strings.TrimSpace(fallback)
	switch code {
	case 0:
		return "预约成功"
	case 75637:
		return "尚未开放，请等待预约开始"
	case -702:
		return "请求频率太快"
	case 429:
		return "请求限流"
	case 76650:
		return "操作频繁"
	case 412:
		return "IP 或账号被限流，建议更换 IP 再试"
	case 75574:
		return "预约已被抢空"
	case 76674:
		return "预约已达上限"
	case 76651:
		return "该活动仅限女性预约"
	default:
		if fallback != "" {
			return fallback
		}
		return fmt.Sprintf("未知状态码 %d", code)
	}
}

func (c *Client) do(ctx context.Context, method string, endpoint string, cookie string, body io.Reader, target any, contentType ...string) error {
	req, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/json, text/plain, */*")
	if len(contentType) > 0 && strings.TrimSpace(contentType[0]) != "" {
		req.Header.Set("Content-Type", strings.TrimSpace(contentType[0]))
	}
	if normalized := biliauth.NormalizeCookieHeader(cookie); normalized != "" {
		req.Header.Set("Cookie", normalized)
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return httpStatusError{StatusCode: resp.StatusCode}
	}
	if target == nil {
		return nil
	}
	if err := json.NewDecoder(resp.Body).Decode(target); err != nil {
		return err
	}
	return nil
}

func (c *Client) randomNonce() int {
	if c.rand == nil {
		return rand.Intn(90000) + 10000
	}
	return c.rand.Intn(90000) + 10000
}

type httpStatusError struct {
	StatusCode int
}

func (e httpStatusError) Error() string {
	return fmt.Sprintf("HTTP %d", e.StatusCode)
}

func normalizeInfoInput(input model.BWSInfoInput) model.BWSInfoInput {
	input.Year = strings.TrimSpace(input.Year)
	if input.Year == "" {
		input.Year = DefaultYear
	}
	input.ReserveDates = strings.TrimSpace(input.ReserveDates)
	if input.ReserveDates == "" {
		input.ReserveDates = DefaultReserveDates
	}
	input.ReserveType = model.NormalizeBWSReserveType(input.ReserveType)
	return input
}

func csrfFromCookie(cookie string) (string, error) {
	normalized := biliauth.NormalizeCookieHeader(cookie)
	if normalized == "" {
		return "", errors.New("账号 Cookie 不能为空")
	}
	for _, part := range strings.Split(normalized, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		if strings.TrimSpace(key) == "bili_jct" && strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value), nil
		}
	}
	return "", errors.New("Cookie 中缺少 bili_jct，无法执行 BWS 预约")
}

func normalizeReservationInfo(data json.RawMessage, input model.BWSInfoInput) (model.BWSReservationInfo, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return model.BWSReservationInfo{}, err
	}
	ticketInfo := mapValue(raw, "user_ticket_info")
	reserveList := mapValue(raw, "reserve_list")
	myReservedIDs := reservedIDs(mapValue(raw, "user_reserve_info"))

	dates := sortedKeys(reserveList)
	if len(dates) == 0 {
		dates = sortedKeys(ticketInfo)
	}

	tickets := make([]model.BWSTicket, 0, len(dates))
	ticketByDate := map[string]model.BWSTicket{}
	for _, date := range dates {
		ticketRaw := mapValue(ticketInfo, date)
		ticket := model.BWSTicket{
			Date:       date,
			TicketNo:   stringValue(ticketRaw, "ticket"),
			ScreenName: stringValue(ticketRaw, "screen_name"),
			SKUName:    stringValue(ticketRaw, "sku_name"),
			IsVIP:      boolValue(ticketRaw, "is_vip"),
			Raw:        ticketRaw,
		}
		tickets = append(tickets, ticket)
		ticketByDate[date] = ticket
	}

	items := make([]model.BWSItem, 0)
	for _, date := range dates {
		for _, activity := range sliceMaps(reserveList[date]) {
			itemDate := date
			actBegin := int64Value(activity, "act_begin_time")
			if actBegin > 0 {
				itemDate = time.Unix(actBegin, 0).Format("20060102")
			}
			ticket := ticketByDate[itemDate]
			if ticket.TicketNo == "" {
				ticket = ticketByDate[date]
			}
			nextReserve := mapValue(activity, "next_reserve")
			reserveID := int64Value(activity, "reserve_id")
			reserveBegin := int64Value(activity, "reserve_begin_time")
			nextReserveBegin := int64Value(nextReserve, "reserve_begin_time")
			isVIPTicket := int64Value(activity, "is_vip_ticket") == 1 || boolValue(activity, "is_vip_ticket")
			effectiveReserve := reserveBegin
			if isVIPTicket && !ticket.IsVIP && nextReserveBegin > 0 {
				effectiveReserve = nextReserveBegin
			}
			describe := stringValue(activity, "describe_info")
			items = append(items, model.BWSItem{
				ReserveID:                 reserveID,
				Title:                     cleanTitle(stringValue(activity, "act_title")),
				Date:                      itemDate,
				TicketNo:                  ticket.TicketNo,
				ReserveBeginTime:          reserveBegin,
				NextReserveBeginTime:      nextReserveBegin,
				EffectiveReserveBeginTime: effectiveReserve,
				ActBeginTime:              actBegin,
				ActEndTime:                int64Value(activity, "act_end_time"),
				ReserveLocation:           stringValue(activity, "reserve_location"),
				DescribeInfo:              describe,
				State:                     int(int64Value(activity, "state")),
				IsVIPTicket:               isVIPTicket,
				UserIsVIP:                 ticket.IsVIP,
				IsSecondPay:               strings.Contains(describe, secondPayMarker),
				Reserved:                  myReservedIDs[reserveID],
				Raw:                       activity,
			})
		}
	}

	return model.BWSReservationInfo{
		Year:         input.Year,
		ReserveDates: input.ReserveDates,
		ReserveType:  input.ReserveType,
		Tickets:      tickets,
		Items:        items,
		Raw:          raw,
	}, nil
}

func normalizeMyReservations(data json.RawMessage, year string) (model.BWSMyReservationList, error) {
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return model.BWSMyReservationList{}, err
	}
	reserveList := mapValue(raw, "reserve_list")
	items := make([]model.BWSMyReservation, 0)
	for _, date := range sortedKeys(reserveList) {
		for _, activity := range sliceMaps(reserveList[date]) {
			title := cleanTitle(stringValue(activity, "act_title"))
			describe := stringValue(activity, "describe_info")
			status := "待确认"
			if int64Value(activity, "is_checked") == 1 {
				status = "已签到"
			} else if int64Value(activity, "online_state") == 0 {
				status = "预约成功"
			}
			items = append(items, model.BWSMyReservation{
				ReserveID:       int64Value(activity, "reserve_id"),
				ReserveNo:       stringValue(activity, "reserve_no"),
				Title:           title,
				Date:            date,
				ActBeginTime:    int64Value(activity, "act_begin_time"),
				ActEndTime:      int64Value(activity, "act_end_time"),
				ReserveLocation: stringValue(activity, "reserve_location"),
				DescribeInfo:    describe,
				Status:          status,
				Raw:             activity,
			})
		}
	}
	return model.BWSMyReservationList{Year: year, Items: items, Raw: raw}, nil
}

func reservedIDs(userReserveInfo map[string]any) map[int64]bool {
	ids := map[int64]bool{}
	for _, value := range userReserveInfo {
		for _, item := range sliceMaps(value) {
			id := int64Value(item, "reserve_id")
			if id > 0 {
				ids[id] = true
			}
		}
	}
	return ids
}

func cleanTitle(value string) string {
	return strings.TrimSpace(strings.ReplaceAll(value, "\n", ""))
}

func mapValue(raw map[string]any, key string) map[string]any {
	if raw == nil {
		return map[string]any{}
	}
	value, ok := raw[key]
	if !ok {
		return map[string]any{}
	}
	if mapped, ok := value.(map[string]any); ok {
		return mapped
	}
	return map[string]any{}
}

func sliceMaps(value any) []map[string]any {
	values, ok := value.([]any)
	if !ok {
		return nil
	}
	result := make([]map[string]any, 0, len(values))
	for _, item := range values {
		if mapped, ok := item.(map[string]any); ok {
			result = append(result, mapped)
		}
	}
	return result
}

func sortedKeys(raw map[string]any) []string {
	keys := make([]string, 0, len(raw))
	for key := range raw {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

func stringValue(raw map[string]any, key string) string {
	value, ok := raw[key]
	if !ok || value == nil {
		return ""
	}
	switch typed := value.(type) {
	case string:
		return typed
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	default:
		return fmt.Sprint(typed)
	}
}

func int64Value(raw map[string]any, key string) int64 {
	value, ok := raw[key]
	if !ok || value == nil {
		return 0
	}
	switch typed := value.(type) {
	case int64:
		return typed
	case int:
		return int64(typed)
	case float64:
		return int64(typed)
	case json.Number:
		parsed, _ := typed.Int64()
		return parsed
	case string:
		parsed, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return parsed
	default:
		return 0
	}
}

func boolValue(raw map[string]any, key string) bool {
	value, ok := raw[key]
	if !ok || value == nil {
		return false
	}
	switch typed := value.(type) {
	case bool:
		return typed
	case float64:
		return typed != 0
	case string:
		return typed == "1" || strings.EqualFold(typed, "true")
	default:
		return false
	}
}
