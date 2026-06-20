package biliticket

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fdcs99/biligo/internal/biliauth"
	"github.com/fdcs99/biligo/internal/model"
	"github.com/fdcs99/biligo/internal/tickettime"
	"github.com/skip2/go-qrcode"
)

const (
	defaultShowBaseURL = "https://show.bilibili.com"
	defaultMallBaseURL = "https://mall.bilibili.com"
	defaultAPIBaseURL  = "https://api.bilibili.com"
	showReferer        = "https://show.bilibili.com/"
	ticketUserAgent    = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/126.0.0.0 Safari/537.36"
)

var projectIDPattern = regexp.MustCompile(`^\d+$`)

var saleFlagNumberMap = map[int64]string{
	1:   "不可售",
	2:   "预售",
	3:   "停售",
	4:   "售罄",
	5:   "不可用",
	6:   "库存紧张",
	8:   "暂时售罄",
	9:   "不在白名单",
	101: "未开始",
	102: "已结束",
	103: "未完成",
	105: "下架",
	106: "已取消",
}

type Client struct {
	httpClient   *http.Client
	showBaseURL  string
	mallBaseURL  string
	apiBaseURL   string
	createTimeMs int64
	cTokenWindow cTokenBrowserWindowState
}

type OrderPrepareResult struct {
	Token         string
	PToken        string
	Raw           map[string]any
	cTokenSession cTokenSession
}

type OrderCreateResult struct {
	Code     int64
	Message  string
	OrderID  string
	PayMoney int64
	Raw      map[string]any
}

type PayParamResult struct {
	CodeURL        string
	QRImageDataURL string
	Raw            map[string]any
}

type projectPayload struct {
	ID         int64
	Name       string
	HotProject bool
	HasETicket bool
	ScreenList []map[string]any
	SalesDates []string
	VenueName  string
	VenueAddr  string
	StartTime  int64
	EndTime    int64
}

func NewClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = newKeepAliveHTTPClient()
	}
	return &Client{
		httpClient:   httpClient,
		showBaseURL:  defaultShowBaseURL,
		mallBaseURL:  defaultMallBaseURL,
		apiBaseURL:   defaultAPIBaseURL,
		createTimeMs: time.Now().UnixMilli(),
		cTokenWindow: generateCTokenBrowserWindowState(),
	}
}

func newKeepAliveHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.DisableKeepAlives = false
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	transport.IdleConnTimeout = 90 * time.Second
	return &http.Client{
		Timeout:   15 * time.Second,
		Transport: transport,
	}
}

func NewClientWithBaseURL(httpClient *http.Client, baseURL string) *Client {
	client := NewClient(httpClient)
	if strings.TrimSpace(baseURL) != "" {
		baseURL = strings.TrimRight(baseURL, "/")
		client.showBaseURL = baseURL
		client.mallBaseURL = baseURL
		client.apiBaseURL = baseURL
	}
	return client
}

func (c *Client) WithHTTPClient(httpClient *http.Client) *Client {
	if httpClient == nil {
		httpClient = newKeepAliveHTTPClient()
	}
	if c == nil {
		return NewClient(httpClient)
	}
	return &Client{
		httpClient:   httpClient,
		showBaseURL:  c.showBaseURL,
		mallBaseURL:  c.mallBaseURL,
		apiBaseURL:   c.apiBaseURL,
		createTimeMs: c.createTimeMs,
		cTokenWindow: c.cTokenWindow,
	}
}

func ExtractProjectID(projectInput string) (int64, error) {
	text := cleanProjectIDToken(projectInput)
	if text == "" {
		return 0, errors.New("项目 ID 不能为空")
	}
	if projectIDPattern.MatchString(text) {
		return strconv.ParseInt(text, 10, 64)
	}

	parsed, err := url.Parse(text)
	if err != nil {
		return 0, errors.New("无法解析项目链接")
	}
	values := parsed.Query()
	if raw := cleanProjectIDToken(values.Get("id")); projectIDPattern.MatchString(raw) {
		return strconv.ParseInt(raw, 10, 64)
	}
	return 0, errors.New("请输入纯数字项目 ID，或包含 id 参数的会员购详情页链接")
}

func cleanProjectIDToken(value string) string {
	return strings.Trim(strings.TrimSpace(value), "+")
}

func (c *Client) FetchProject(ctx context.Context, projectInput string, cookie string) (model.TicketProject, error) {
	projectID, err := ExtractProjectID(projectInput)
	if err != nil {
		return model.TicketProject{}, err
	}
	cookie = biliauth.NormalizeCookieHeader(cookie)

	payload, err := c.fetchProjectPayload(ctx, projectID, cookie)
	if err != nil {
		return model.TicketProject{}, err
	}

	screens := copyScreens(payload.ScreenList)
	for _, date := range payload.SalesDates {
		dateScreens, err := c.fetchScreensByDate(ctx, payload.ID, date, cookie)
		if err != nil {
			continue
		}
		for _, screen := range dateScreens {
			if int64Value(screen["project_id"]) == 0 {
				screen["project_id"] = payload.ID
			}
		}
		screens = mergeScreensByID(screens, dateScreens)
	}

	screens = append(screens, c.fetchLinkGoods(ctx, payload.ID, cookie)...)
	options := buildTicketOptions(payload, screens)

	return model.TicketProject{
		ProjectID:     payload.ID,
		ProjectName:   payload.Name,
		ProjectURL:    fmt.Sprintf("https://show.bilibili.com/platform/detail.html?id=%d", payload.ID),
		VenueName:     payload.VenueName,
		VenueAddress:  payload.VenueAddr,
		StartAt:       formatUnix(payload.StartTime),
		EndAt:         formatUnix(payload.EndTime),
		IsHotProject:  payload.HotProject,
		HasETicket:    payload.HasETicket,
		SalesDates:    payload.SalesDates,
		TicketOptions: options,
		Buyers:        []model.TicketBuyer{},
		Addresses:     []model.TicketAddress{},
	}, nil
}

func (c *Client) FetchPurchaseContext(ctx context.Context, projectInput string, cookie string) (model.TicketProject, error) {
	project, err := c.FetchProject(ctx, projectInput, cookie)
	if err != nil {
		return model.TicketProject{}, err
	}
	if strings.TrimSpace(cookie) == "" {
		return project, nil
	}

	project.Username = c.fetchUsername(ctx, cookie)
	buyers, err := c.fetchBuyers(ctx, project.ProjectID, cookie)
	if err != nil {
		return model.TicketProject{}, err
	}
	addresses, err := c.fetchAddresses(ctx, cookie)
	if err != nil {
		return model.TicketProject{}, err
	}
	project.Buyers = buyers
	project.Addresses = addresses
	return project, nil
}

func (c *Client) FetchAccountContext(ctx context.Context, projectID int64, cookie string) (model.TicketAccountContext, error) {
	if projectID <= 0 {
		return model.TicketAccountContext{}, errors.New("项目 ID 不能为空")
	}
	cookie = biliauth.NormalizeCookieHeader(cookie)
	if strings.TrimSpace(cookie) == "" {
		return model.TicketAccountContext{}, errors.New("账号 Cookie 不能为空")
	}

	buyers, err := c.fetchBuyers(ctx, projectID, cookie)
	if err != nil {
		return model.TicketAccountContext{}, err
	}
	addresses, err := c.fetchAddresses(ctx, cookie)
	if err != nil {
		return model.TicketAccountContext{}, err
	}
	return model.TicketAccountContext{
		ProjectID: projectID,
		Username:  c.fetchUsername(ctx, cookie),
		Buyers:    buyers,
		Addresses: addresses,
	}, nil
}

func (c *Client) FetchProjectHotProject(ctx context.Context, projectID int64, cookie string) (bool, error) {
	if projectID <= 0 {
		return false, errors.New("项目 ID 不能为空")
	}
	project, err := c.FetchProject(ctx, strconv.FormatInt(projectID, 10), cookie)
	if err != nil {
		return false, err
	}
	return project.IsHotProject, nil
}

func (c *Client) CheckTicketStatus(ctx context.Context, task model.Task, cookie string) (model.TicketOption, bool, error) {
	project, err := c.FetchProject(ctx, strconv.FormatInt(task.ProjectID, 10), cookie)
	if err != nil {
		return model.TicketOption{}, false, err
	}
	for _, option := range project.TicketOptions {
		if !ticketOptionMatchesTask(option, task) {
			continue
		}
		return option, isTicketOptionAvailable(option), nil
	}
	return model.TicketOption{}, false, errors.New("未在最新票务信息中找到任务票档")
}

func (c *Client) CheckSelectedTicketsStatus(ctx context.Context, task model.Task, cookie string) (model.TicketOption, int, bool, error) {
	project, err := c.FetchProject(ctx, strconv.FormatInt(task.ProjectID, 10), cookie)
	if err != nil {
		return model.TicketOption{}, 0, false, err
	}
	selected := selectedTicketSet(task.SelectedTickets)
	if len(selected) == 0 {
		return model.TicketOption{}, 0, false, errors.New("回流蹲票模式未选择票种")
	}
	checkedCount := 0
	for _, option := range project.TicketOptions {
		if _, ok := selected[ticketOptionKey(option)]; !ok {
			continue
		}
		checkedCount++
		if isTicketOptionAvailable(option) {
			return option, checkedCount, true, nil
		}
	}
	if checkedCount == 0 {
		return model.TicketOption{}, 0, false, errors.New("未在最新票务信息中找到已选票种")
	}
	return model.TicketOption{}, checkedCount, false, nil
}

func (c *Client) WarmupShow(ctx context.Context, count int) error {
	if count <= 0 {
		count = 1
	}
	endpoint := c.showBaseURL
	errs := make([]string, 0)
	for i := 0; i < count; i++ {
		if err := c.doHead(ctx, endpoint); err != nil {
			errs = append(errs, err.Error())
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("预热 keep-alive 连接失败：%s", strings.Join(errs, ";"))
	}
	return nil
}

func (c *Client) PrepareOrder(ctx context.Context, task model.Task, cookie string) (OrderPrepareResult, error) {
	payload := map[string]any{
		"count":              task.Quantity,
		"screen_id":          task.ScreenID,
		"order_type":         firstPositiveInt(int64(task.OrderType), 1),
		"project_id":         task.ProjectID,
		"sku_id":             task.SKUID,
		"buyer_info":         orderBuyers(task.BuyerInfo),
		"ignoreRequestLimit": true,
		"ticket_agent":       "",
		"token":              "",
		"newRisk":            true,
		"requestSource":      "neul-next",
	}
	var cTokenSession cTokenSession
	if task.IsHotProject {
		ticketCollectionMs := time.Now().UnixMilli()
		cTokenSession = newCTokenSession(task.ProjectID, ticketUserAgent, ticketCollectionMs, c.cTokenWindow)
		payload["token"] = cTokenSession.generatePrepareAt(ticketCollectionMs)
	}
	var response map[string]any
	endpoint := fmt.Sprintf("%s/api/ticket/order/prepare?project_id=%d", c.showBaseURL, task.ProjectID)
	if err := c.doJSON(ctx, http.MethodPost, endpoint, payload, cookie, nil, &response); err != nil {
		return OrderPrepareResult{}, err
	}
	code, _ := optionalCode(response)
	if code != 0 {
		return OrderPrepareResult{Raw: response}, apiError(response, "订单准备失败")
	}
	data, ok := mapValue(response["data"])
	if !ok {
		return OrderPrepareResult{Raw: response}, errors.New("订单准备响应缺少 data")
	}
	token := stringValue(data["token"])
	if token == "" {
		return OrderPrepareResult{Raw: response}, errors.New("订单准备响应缺少 token")
	}
	return OrderPrepareResult{
		Token:         token,
		PToken:        normalizePreparePToken(stringValue(data["ptoken"])),
		Raw:           response,
		cTokenSession: cTokenSession,
	}, nil
}

func (c *Client) CreateOrder(ctx context.Context, task model.Task, cookie string, prepared OrderPrepareResult) (OrderCreateResult, error) {
	nowMs := time.Now().UnixMilli()
	payload, err := buildOrderPayload(task, prepared.Token, nowMs)
	if err != nil {
		return OrderCreateResult{}, err
	}
	orderCreateURL := c.showBaseURL + "/api/ticket/order/createV2"
	endpoint := fmt.Sprintf("%s?project_id=%d", orderCreateURL, task.ProjectID)
	if task.IsHotProject {
		cTokenSession := prepared.cTokenSession
		if cTokenSession.isZero() {
			cTokenSession = newCTokenSession(task.ProjectID, ticketUserAgent, nowMs, c.cTokenWindow)
		}
		payload["clickPosition"] = c.clickPosition(nowMs)
		payload["ctoken"] = cTokenSession.generateCreateAt(nowMs)
	}
	if task.IsHotProject || prepared.PToken != "" {
		payload["ptoken"] = prepared.PToken
		payload["orderCreateUrl"] = orderCreateURL
		endpoint += "&ptoken=" + url.QueryEscape(prepared.PToken)
	}

	var response map[string]any
	if err := c.doJSON(ctx, http.MethodPost, endpoint, payload, cookie, nil, &response); err != nil {
		return OrderCreateResult{}, err
	}
	code, _ := optionalCode(response)
	message := firstNonEmpty(stringValue(response["msg"]), stringValue(response["message"]))
	data, _ := mapValue(response["data"])
	result := OrderCreateResult{
		Code:     code,
		Message:  message,
		OrderID:  firstNonEmpty(stringValue(data["orderId"]), stringValue(data["order_id"])),
		PayMoney: int64Value(data["pay_money"]),
		Raw:      response,
	}
	if !isCreateSuccess(response, code) {
		return result, createV2Error(response)
	}
	return result, nil
}

func (c *Client) clickPosition(nowMs int64) map[string]any {
	originMs := c.createTimeMs
	if originMs <= 0 {
		originMs = nowMs
	}
	return map[string]any{
		"x":      randIntInclusive(400, 900),
		"y":      randIntInclusive(400, 900),
		"origin": originMs,
		"now":    nowMs,
	}
}

func (c *Client) GetPayParam(ctx context.Context, orderID string, cookie string) (PayParamResult, error) {
	orderID = strings.TrimSpace(orderID)
	if orderID == "" {
		return PayParamResult{}, errors.New("订单 ID 不能为空")
	}
	endpoint := fmt.Sprintf("%s/api/ticket/order/getPayParam?order_id=%s", c.showBaseURL, url.QueryEscape(orderID))
	var response map[string]any
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, cookie, nil, &response); err != nil {
		return PayParamResult{}, err
	}
	code, _ := optionalCode(response)
	if code != 0 {
		return PayParamResult{Raw: response}, apiError(response, "获取支付参数失败")
	}
	data, ok := mapValue(response["data"])
	if !ok {
		return PayParamResult{Raw: response}, errors.New("支付参数响应缺少 data")
	}
	codeURL := stringValue(data["code_url"])
	if codeURL == "" {
		return PayParamResult{Raw: response}, errors.New("支付参数响应缺少 code_url")
	}
	png, err := qrcode.Encode(codeURL, qrcode.Medium, 256)
	if err != nil {
		return PayParamResult{}, err
	}
	return PayParamResult{
		CodeURL:        codeURL,
		QRImageDataURL: "data:image/png;base64," + base64.StdEncoding.EncodeToString(png),
		Raw:            response,
	}, nil
}

func (c *Client) fetchProjectPayload(ctx context.Context, projectID int64, cookie string) (projectPayload, error) {
	newPayload, newErr := c.fetchProjectPayloadNew(ctx, projectID, cookie)
	if newErr == nil {
		return newPayload, nil
	}
	oldPayload, oldErr := c.fetchProjectPayloadOld(ctx, projectID, cookie)
	if oldErr == nil {
		return oldPayload, nil
	}
	return projectPayload{}, fmt.Errorf("获取项目详情失败：新版接口=%v；旧版接口=%v", newErr, oldErr)
}

func (c *Client) fetchProjectPayloadNew(ctx context.Context, projectID int64, cookie string) (projectPayload, error) {
	var response map[string]any
	err := c.doJSON(ctx, http.MethodPost, c.mallBaseURL+"/mall-search-items/items_detail/info", map[string]any{
		"itemsId":             projectID,
		"itemsDetailPageType": 3,
	}, cookie, map[string]string{
		"Origin":  "https://mall.bilibili.com",
		"Referer": fmt.Sprintf("%s/neul-next/ticket-renovation/detail.html?id=%d&from=pc_ticketlist&noTitleBar=1", c.mallBaseURL, projectID),
	}, &response)
	if err != nil {
		return projectPayload{}, err
	}

	if boolValue(response["success"]) == false && response["success"] != nil {
		return projectPayload{}, errors.New(firstNonEmpty(stringValue(response["message"]), stringValue(response["msg"]), "新版项目接口返回失败"))
	}
	if code, ok := optionalCode(response); ok && code != 0 {
		return projectPayload{}, errors.New(firstNonEmpty(stringValue(response["message"]), stringValue(response["msg"]), "新版项目接口返回失败"))
	}
	data, ok := mapValue(response["data"])
	if !ok {
		return projectPayload{}, errors.New("新版项目接口缺少 data")
	}
	return normalizeNewProjectPayload(data, projectID)
}

func (c *Client) fetchProjectPayloadOld(ctx context.Context, projectID int64, cookie string) (projectPayload, error) {
	endpoint := fmt.Sprintf("%s/api/ticket/project/getV2?version=134&id=%d&project_id=%d", c.showBaseURL, projectID, projectID)
	var response map[string]any
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, cookie, nil, &response); err != nil {
		return projectPayload{}, err
	}
	if code, ok := optionalCode(response); !ok || code != 0 {
		return projectPayload{}, errors.New(firstNonEmpty(stringValue(response["msg"]), stringValue(response["message"]), "旧版项目接口返回失败"))
	}
	data, ok := mapValue(response["data"])
	if !ok {
		return projectPayload{}, errors.New("旧版项目接口缺少 data")
	}
	return normalizeOldProjectPayload(data, projectID)
}

func (c *Client) fetchScreensByDate(ctx context.Context, projectID int64, date string, cookie string) ([]map[string]any, error) {
	if strings.TrimSpace(date) == "" {
		return nil, nil
	}
	endpoint := fmt.Sprintf("%s/api/ticket/project/infoByDate?id=%d&date=%s", c.showBaseURL, projectID, url.QueryEscape(date))
	var response map[string]any
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, cookie, nil, &response); err != nil {
		return nil, err
	}
	if code, ok := optionalCode(response); ok && code != 0 {
		return nil, errors.New(firstNonEmpty(stringValue(response["msg"]), stringValue(response["message"]), "按日期获取场次失败"))
	}
	data, ok := mapValue(response["data"])
	if !ok {
		return nil, nil
	}
	screens, _ := mapSliceValue(data["screen_list"])
	return screens, nil
}

func (c *Client) fetchLinkGoods(ctx context.Context, projectID int64, cookie string) []map[string]any {
	listURL := fmt.Sprintf("%s/api/ticket/linkgoods/list?project_id=%d&page_type=0", c.showBaseURL, projectID)
	var listResponse map[string]any
	if err := c.doJSON(ctx, http.MethodGet, listURL, nil, cookie, nil, &listResponse); err != nil {
		return nil
	}
	data, ok := mapValue(listResponse["data"])
	if !ok {
		return nil
	}
	rawList, ok := anySliceValue(data["list"])
	if !ok {
		return nil
	}

	result := make([]map[string]any, 0)
	for _, raw := range rawList {
		item, ok := mapValue(raw)
		if !ok {
			continue
		}
		linkID := int64Value(item["id"])
		if linkID <= 0 {
			continue
		}

		detailURL := fmt.Sprintf("%s/api/ticket/linkgoods/detail?link_id=%d", c.showBaseURL, linkID)
		var detailResponse map[string]any
		if err := c.doJSON(ctx, http.MethodGet, detailURL, nil, cookie, nil, &detailResponse); err != nil {
			continue
		}
		detailData, ok := mapValue(detailResponse["data"])
		if !ok {
			continue
		}
		itemID := int64Value(detailData["item_id"])
		specs, ok := mapSliceValue(detailData["specs_list"])
		if !ok {
			continue
		}
		for _, spec := range specs {
			spec["project_id"] = itemID
			spec["link_id"] = linkID
			result = append(result, spec)
		}
	}
	return result
}

func (c *Client) fetchUsername(ctx context.Context, cookie string) string {
	var response map[string]any
	if err := c.doJSON(ctx, http.MethodGet, c.apiBaseURL+"/x/web-interface/nav", nil, cookie, map[string]string{
		"Referer": showReferer,
	}, &response); err != nil {
		return ""
	}
	data, ok := mapValue(response["data"])
	if !ok {
		return ""
	}
	return stringValue(data["uname"])
}

func (c *Client) fetchBuyers(ctx context.Context, projectID int64, cookie string) ([]model.TicketBuyer, error) {
	endpoint := fmt.Sprintf("%s/api/ticket/buyer/list?is_default&projectId=%d", c.showBaseURL, projectID)
	var response map[string]any
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, cookie, nil, &response); err != nil {
		return nil, err
	}
	code, _ := optionalCode(response)
	if code != 0 {
		return nil, apiError(response, "获取购票人失败")
	}
	data, ok := mapValue(response["data"])
	if !ok {
		return nil, nil
	}
	items, ok := mapSliceValue(data["list"])
	if !ok {
		return nil, nil
	}
	buyers := make([]model.TicketBuyer, 0, len(items))
	for _, item := range items {
		buyers = append(buyers, normalizeBuyer(item))
	}
	return buyers, nil
}

func (c *Client) fetchAddresses(ctx context.Context, cookie string) ([]model.TicketAddress, error) {
	endpoint := fmt.Sprintf("%s/api/ticket/addr/list", c.showBaseURL)
	var response map[string]any
	if err := c.doJSON(ctx, http.MethodGet, endpoint, nil, cookie, nil, &response); err != nil {
		return nil, err
	}
	code, _ := optionalCode(response)
	if code != 0 {
		return nil, apiError(response, "获取收货地址失败")
	}
	data, ok := mapValue(response["data"])
	if !ok {
		return nil, nil
	}
	items, ok := mapSliceValue(data["addr_list"])
	if !ok {
		return nil, nil
	}
	addresses := make([]model.TicketAddress, 0, len(items))
	for _, item := range items {
		addresses = append(addresses, normalizeAddress(item))
	}
	return addresses, nil
}

func (c *Client) doJSON(ctx context.Context, method string, endpoint string, body any, cookie string, headers map[string]string, target any) error {
	var reader io.Reader
	if body != nil {
		buffer := &bytes.Buffer{}
		if err := json.NewEncoder(buffer).Encode(body); err != nil {
			return err
		}
		reader = buffer
	}

	req, err := http.NewRequestWithContext(ctx, method, endpoint, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", showReferer)
	req.Header.Set("User-Agent", ticketUserAgent)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.Header.Set("Cookie", cookie)
	}
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Bilibili 接口返回状态码 %d", resp.StatusCode)
	}

	decoder := json.NewDecoder(resp.Body)
	decoder.UseNumber()
	if err := decoder.Decode(target); err != nil {
		return err
	}

	if payload, ok := target.(*map[string]any); ok {
		if stringValue((*payload)["msg"]) == "请先登录" {
			return errors.New("当前账号未登录，请先验证或重新登录")
		}
	}
	return nil
}

func (c *Client) doHead(ctx context.Context, endpoint string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "*/*")
	req.Header.Set("Accept-Language", "zh-CN,zh;q=0.9")
	req.Header.Set("Referer", showReferer)
	req.Header.Set("User-Agent", ticketUserAgent)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer drainAndClose(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		return fmt.Errorf("HEAD %s 返回状态码 %d", endpoint, resp.StatusCode)
	}
	return nil
}

func drainAndClose(body io.ReadCloser) {
	if body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, body)
	_ = body.Close()
}

func normalizeNewProjectPayload(data map[string]any, fallbackProjectID int64) (projectPayload, error) {
	rawScreens, ok := mapSliceValue(data["screenList"])
	if !ok || len(rawScreens) == 0 {
		return projectPayload{}, errors.New("新版项目接口缺少 screenList")
	}

	projectID := firstPositiveInt(int64Value(data["projectId"]), int64Value(data["itemsId"]), fallbackProjectID)
	screenStartTimes := make([]int64, 0, len(rawScreens))
	hasDeliveryFee := false
	for _, screen := range rawScreens {
		screen["project_id"] = firstPositiveInt(int64Value(screen["project_id"]), projectID)
		if int64Value(screen["start_time"]) > 0 {
			screenStartTimes = append(screenStartTimes, int64Value(screen["start_time"]))
		}
		if int64Value(screen["express_fee"]) > 0 {
			hasDeliveryFee = true
		}
		if tickets, ok := mapSliceValue(screen["ticket_list"]); ok {
			for _, ticket := range tickets {
				ticket["project_id"] = firstPositiveInt(int64Value(ticket["project_id"]), projectID)
				ticket["screen_name"] = firstNonEmpty(stringValue(ticket["screen_name"]), stringValue(screen["name"]))
				if saleFlag, ok := mapValue(ticket["sale_flag"]); ok && int64Value(ticket["sale_flag_number"]) == 0 {
					ticket["sale_flag_number"] = int64Value(saleFlag["number"])
				}
			}
		}
	}

	venue, _ := mapValue(data["skuVenueInfo"])
	startTime := minPositive(screenStartTimes)
	endTime := firstPositiveInt(int64Value(data["endTime"]), maxPositive(screenStartTimes))
	return projectPayload{
		ID:         projectID,
		Name:       stringValue(data["projectName"]),
		HotProject: hotProjectValue(data),
		HasETicket: !hasDeliveryFee,
		ScreenList: rawScreens,
		SalesDates: extractSalesDates(data["salesDates"]),
		VenueName:  stringValue(venue["name"]),
		VenueAddr:  stringValue(venue["address_detail"]),
		StartTime:  startTime,
		EndTime:    endTime,
	}, nil
}

func normalizeOldProjectPayload(data map[string]any, fallbackProjectID int64) (projectPayload, error) {
	rawScreens, ok := mapSliceValue(data["screen_list"])
	if !ok {
		return projectPayload{}, errors.New("旧版项目接口缺少 screen_list")
	}
	projectID := firstPositiveInt(int64Value(data["id"]), fallbackProjectID)
	venue, _ := mapValue(data["venue_info"])
	for _, screen := range rawScreens {
		screen["project_id"] = firstPositiveInt(int64Value(screen["project_id"]), projectID)
	}
	return projectPayload{
		ID:         projectID,
		Name:       stringValue(data["name"]),
		HotProject: hotProjectValue(data),
		HasETicket: boolValue(data["has_eticket"]),
		ScreenList: rawScreens,
		SalesDates: extractSalesDates(data["sales_dates"]),
		VenueName:  stringValue(venue["name"]),
		VenueAddr:  stringValue(venue["address_detail"]),
		StartTime:  int64Value(data["start_time"]),
		EndTime:    int64Value(data["end_time"]),
	}, nil
}

func buildTicketOptions(payload projectPayload, screens []map[string]any) []model.TicketOption {
	options := make([]model.TicketOption, 0)
	seen := map[string]struct{}{}
	for _, screen := range screens {
		screenName := stringValue(screen["name"])
		if screenName == "" {
			continue
		}
		screenID := int64Value(screen["id"])
		currentProjectID := firstPositiveInt(int64Value(screen["project_id"]), payload.ID)
		expressFee := int64(0)
		if !payload.HasETicket {
			expressFee = maxInt64(int64Value(screen["express_fee"]), 0)
		}
		tickets, ok := mapSliceValue(screen["ticket_list"])
		if !ok {
			continue
		}
		for _, ticket := range tickets {
			skuID := int64Value(ticket["id"])
			ticketLevel := stringValue(ticket["desc"])
			price := int64Value(ticket["price"]) + expressFee
			saleStatus := formatSaleStatus(ticket)
			clickable := boolValue(ticket["clickable"])
			saleStart := stringValue(ticket["sale_start"])
			linkID := int64Value(screen["link_id"])
			value := fmt.Sprintf("%d:%d:%d:%d", currentProjectID, screenID, skuID, linkID)
			if _, exists := seen[value]; exists {
				continue
			}
			seen[value] = struct{}{}
			priceText := formatPrice(price)
			display := fmt.Sprintf("%s - %s - %s - %s - 【起售时间：%s】", screenName, ticketLevel, priceText, saleStatus, saleStart)
			options = append(options, model.TicketOption{
				Value:        value,
				Display:      display,
				ProjectID:    currentProjectID,
				ScreenID:     screenID,
				SKUID:        skuID,
				ScreenName:   screenName,
				TicketLevel:  ticketLevel,
				Price:        price,
				PriceText:    priceText,
				SaleStatus:   saleStatus,
				SaleStart:    saleStart,
				IsHotProject: payload.HotProject,
				LinkID:       linkID,
				Clickable:    clickable,
			})
		}
	}
	sort.SliceStable(options, func(i, j int) bool {
		if options[i].ScreenName == options[j].ScreenName {
			return options[i].Price < options[j].Price
		}
		return options[i].ScreenName < options[j].ScreenName
	})
	return options
}

func isTicketOptionAvailable(option model.TicketOption) bool {
	return option.Clickable
}

func ticketOptionMatchesTask(option model.TicketOption, task model.Task) bool {
	if option.ProjectID != task.ProjectID || option.ScreenID != task.ScreenID || option.SKUID != task.SKUID {
		return false
	}
	return task.LinkID <= 0 || option.LinkID == task.LinkID
}

func selectedTicketSet(tickets []model.TicketOption) map[string]struct{} {
	result := make(map[string]struct{}, len(tickets))
	for _, ticket := range tickets {
		key := ticketOptionKey(ticket)
		if key == "" {
			continue
		}
		result[key] = struct{}{}
	}
	return result
}

func ticketOptionKey(ticket model.TicketOption) string {
	if ticket.ProjectID <= 0 || ticket.ScreenID <= 0 || ticket.SKUID <= 0 {
		return ""
	}
	return fmt.Sprintf("%d:%d:%d:%d", ticket.ProjectID, ticket.ScreenID, ticket.SKUID, ticket.LinkID)
}

func normalizeBuyer(raw map[string]any) model.TicketBuyer {
	return model.TicketBuyer{
		ID:         int64Value(firstPresent(raw, "id", "uid", "buyer_id")),
		Name:       stringValue(raw["name"]),
		PersonalID: firstNonEmpty(stringValue(raw["personal_id"]), stringValue(raw["personalId"])),
		Tel:        firstNonEmpty(stringValue(raw["tel"]), stringValue(raw["phone"])),
		Raw:        cloneMap(raw),
	}
}

func normalizeAddress(raw map[string]any) model.TicketAddress {
	address := model.TicketAddress{
		ID:    int64Value(raw["id"]),
		Name:  stringValue(raw["name"]),
		Phone: stringValue(raw["phone"]),
		Prov:  stringValue(raw["prov"]),
		City:  stringValue(raw["city"]),
		Area:  stringValue(raw["area"]),
		Addr:  stringValue(raw["addr"]),
		Raw:   cloneMap(raw),
	}
	address.FullAddress = address.Prov + address.City + address.Area + address.Addr
	return address
}

func buildOrderPayload(task model.Task, token string, nowMs int64) (map[string]any, error) {
	buyerInfo, err := json.Marshal(orderBuyers(task.BuyerInfo))
	if err != nil {
		return nil, err
	}
	deliverInfo, err := json.Marshal(orderDeliver(task.DeliverInfo))
	if err != nil {
		return nil, err
	}
	payload := map[string]any{
		"again":         1,
		"count":         task.Quantity,
		"screen_id":     task.ScreenID,
		"project_id":    task.ProjectID,
		"sku_id":        task.SKUID,
		"order_type":    firstPositiveInt(int64(task.OrderType), 1),
		"pay_money":     task.PayMoney,
		"buyer_info":    string(buyerInfo),
		"buyer":         task.Buyer,
		"tel":           task.Tel,
		"deliver_info":  string(deliverInfo),
		"phone":         task.Phone,
		"token":         token,
		"timestamp":     nowMs,
		"newRisk":       true,
		"requestSource": "neul-next",
	}
	if task.LinkID > 0 {
		payload["link_id"] = task.LinkID
	}
	return payload, nil
}

func orderBuyers(buyers []model.TicketBuyer) []map[string]any {
	result := make([]map[string]any, 0, len(buyers))
	for _, buyer := range buyers {
		item := map[string]any{}
		if buyer.Raw != nil {
			item = cloneMap(buyer.Raw)
		}
		item["name"] = buyer.Name
		item["personal_id"] = buyer.PersonalID
		if buyer.ID > 0 {
			item["id"] = buyer.ID
		}
		if buyer.Tel != "" {
			item["tel"] = buyer.Tel
		}
		result = append(result, item)
	}
	return result
}

func orderDeliver(address *model.TicketAddress) map[string]any {
	if address == nil {
		return map[string]any{}
	}
	return map[string]any{
		"name":    address.Name,
		"tel":     address.Phone,
		"addr_id": address.ID,
		"addr":    firstNonEmpty(address.FullAddress, address.Prov+address.City+address.Area+address.Addr),
	}
}

func isCreateSuccess(response map[string]any, code int64) bool {
	if code == 100048 || code == 100079 {
		return true
	}
	message := firstNonEmpty(stringValue(response["msg"]), stringValue(response["message"]))
	return code == 0 && !IsDefaultBBRMessage(message)
}

func IsDefaultBBRMessage(message string) bool {
	return strings.Contains(strings.ToLower(message), "defaultbbr")
}

func apiError(response map[string]any, fallback string) error {
	code, _ := optionalCode(response)
	message := firstNonEmpty(stringValue(response["msg"]), stringValue(response["message"]), fallback)
	if code != 0 {
		return fmt.Errorf("[%d] %s", code, message)
	}
	return errors.New(message)
}

func createV2Error(response map[string]any) error {
	code, _ := optionalCode(response)
	return errors.New(createV2StatusMessage(response, code))
}

func createV2StatusMessage(response map[string]any, code int64) string {
	apiMessage := firstNonEmpty(stringValue(response["msg"]), stringValue(response["message"]))
	message := createV2StatusHint(response, code, apiMessage)
	if message == "" {
		message = firstNonEmpty(apiMessage, "创建订单失败")
	}
	return fmt.Sprintf("状态码：%d，提示信息：%s", code, message)
}

func createV2StatusHint(response map[string]any, code int64, apiMessage string) string {
	if code == 0 && IsDefaultBBRMessage(apiMessage) {
		return "createV2 返回 defaultBBR 警告，继续重试。"
	}

	switch code {
	case 504:
		return "网关超时，B 站服务响应超时，可稍后重试。"
	case 503:
		return "服务不可用，B 站服务暂时不可用，可稍后重试。"
	case 412:
		return "触发风控，建议暂停一段时间，并在平台页面确认账号状态后再重试。"
	case -401:
		return "触发全局风控或登录校验，请检查账号登录态，并手动完成平台验证。"
	case 429:
		return "请求被限流，请降低频率后再重试。"
	case 100001:
		return "请求被阻塞，可稍后重试。"
	case 900001:
		return "请求被阻塞，可稍后重试。"
	case 900002:
		return "请求被阻塞，可稍后重试。"
	case 100009:
		return "库存不足，当前票档暂无库存。"
	case 100044:
		return "检测到验证码风控，请手动完成平台验证码后再继续。"
	case 100034:
		data, _ := mapValue(response["data"])
		payMoney := int64Value(data["pay_money"])
		if payMoney > 0 {
			return fmt.Sprintf("订单金额已由接口更新为 %d 分，将自动更新任务金额后重试。", payMoney)
		}
		return "订单金额已由接口更新，将自动重试。"
	case 3:
		return "触发 5 秒盾，请等待后再重试。"
	case 221:
		return "请求被阻塞，可稍后重试。"
	case 219:
		return "库存不足，当前票档暂无库存。"
	default:
		return ""
	}
}

func formatSaleStatus(ticket map[string]any) string {
	if saleFlagNumber := int64Value(ticket["sale_flag_number"]); saleFlagNumber != 0 {
		if text, ok := saleFlagNumberMap[saleFlagNumber]; ok {
			return text
		}
	}
	if clickable, ok := ticket["clickable"].(bool); ok {
		if clickable {
			return "可购买"
		}
		return "不可购买"
	}
	return "未知状态"
}

func firstPresent(values map[string]any, keys ...string) any {
	for _, key := range keys {
		if value, ok := values[key]; ok && value != nil {
			return value
		}
	}
	return nil
}

func hotProjectValue(values map[string]any) bool {
	return boolValue(firstPresent(values, "hotProject", "isHotProject", "is_hot_project", "hot_project"))
}

func normalizePreparePToken(value string) string {
	return strings.ReplaceAll(value, "=", "")
}

func extractSalesDates(raw any) []string {
	items, ok := anySliceValue(raw)
	if !ok {
		return nil
	}
	dates := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		date := ""
		if itemMap, ok := mapValue(item); ok {
			date = stringValue(itemMap["date"])
		} else {
			date = stringValue(item)
		}
		date = strings.TrimSpace(date)
		if date == "" {
			continue
		}
		if _, exists := seen[date]; exists {
			continue
		}
		seen[date] = struct{}{}
		dates = append(dates, date)
	}
	return dates
}

func copyScreens(screens []map[string]any) []map[string]any {
	copied := make([]map[string]any, 0, len(screens))
	for _, screen := range screens {
		copied = append(copied, cloneMap(screen))
	}
	return copied
}

func mergeScreensByID(base []map[string]any, extra []map[string]any) []map[string]any {
	merged := copyScreens(base)
	seen := map[int64]struct{}{}
	for _, screen := range merged {
		id := int64Value(screen["id"])
		if id > 0 {
			seen[id] = struct{}{}
		}
	}
	for _, screen := range extra {
		id := int64Value(screen["id"])
		if id <= 0 {
			continue
		}
		if _, exists := seen[id]; exists {
			continue
		}
		seen[id] = struct{}{}
		merged = append(merged, cloneMap(screen))
	}
	return merged
}

func cloneMap(input map[string]any) map[string]any {
	output := make(map[string]any, len(input))
	for key, value := range input {
		if nested, ok := mapValue(value); ok {
			output[key] = cloneMap(nested)
			continue
		}
		if nestedList, ok := anySliceValue(value); ok {
			list := make([]any, 0, len(nestedList))
			for _, item := range nestedList {
				if itemMap, ok := mapValue(item); ok {
					list = append(list, cloneMap(itemMap))
				} else {
					list = append(list, item)
				}
			}
			output[key] = list
			continue
		}
		output[key] = value
	}
	return output
}

func optionalCode(payload map[string]any) (int64, bool) {
	if _, ok := payload["code"]; ok {
		return int64Value(payload["code"]), true
	}
	if _, ok := payload["errno"]; ok {
		return int64Value(payload["errno"]), true
	}
	return 0, false
}

func mapValue(value any) (map[string]any, bool) {
	if value == nil {
		return nil, false
	}
	result, ok := value.(map[string]any)
	return result, ok
}

func anySliceValue(value any) ([]any, bool) {
	if value == nil {
		return nil, false
	}
	result, ok := value.([]any)
	return result, ok
}

func mapSliceValue(value any) ([]map[string]any, bool) {
	items, ok := anySliceValue(value)
	if !ok {
		return nil, false
	}
	result := make([]map[string]any, 0, len(items))
	for _, item := range items {
		itemMap, ok := mapValue(item)
		if !ok {
			continue
		}
		result = append(result, itemMap)
	}
	return result, true
}

func stringValue(value any) string {
	switch typed := value.(type) {
	case string:
		return strings.TrimSpace(typed)
	case json.Number:
		return typed.String()
	case float64:
		if typed == float64(int64(typed)) {
			return strconv.FormatInt(int64(typed), 10)
		}
		return strconv.FormatFloat(typed, 'f', -1, 64)
	case int:
		return strconv.Itoa(typed)
	case int64:
		return strconv.FormatInt(typed, 10)
	default:
		return ""
	}
}

func boolValue(value any) bool {
	switch typed := value.(type) {
	case bool:
		return typed
	case json.Number:
		num, _ := typed.Int64()
		return num != 0
	case float64:
		return typed != 0
	case string:
		return typed == "true" || typed == "1"
	default:
		return false
	}
}

func int64Value(value any) int64 {
	switch typed := value.(type) {
	case json.Number:
		num, _ := typed.Int64()
		return num
	case float64:
		return int64(typed)
	case int:
		return int64(typed)
	case int64:
		return typed
	case string:
		num, _ := strconv.ParseInt(strings.TrimSpace(typed), 10, 64)
		return num
	default:
		return 0
	}
}

func firstPositiveInt(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func minPositive(values []int64) int64 {
	min := int64(0)
	for _, value := range values {
		if value <= 0 {
			continue
		}
		if min == 0 || value < min {
			min = value
		}
	}
	return min
}

func maxPositive(values []int64) int64 {
	max := int64(0)
	for _, value := range values {
		if value > max {
			max = value
		}
	}
	return max
}

func maxInt64(left int64, right int64) int64 {
	if left > right {
		return left
	}
	return right
}

func formatUnix(timestamp int64) string {
	return tickettime.FormatUnix(timestamp)
}

func formatPrice(price int64) string {
	return fmt.Sprintf("￥%s", strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.2f", float64(price)/100), "0"), "."))
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}
