package biliticket

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fdcs99/biligo/internal/model"
)

func TestExtractProjectIDAcceptsSharedMallLinkWithTrailingPlus(t *testing.T) {
	link := "https://mall.bilibili.com/neul-next/ticket-renovation/detail.html?from_type=mall_home_search&id=1001701+&msource=mall_48111_ssk&outsideMall=no&outsideMall=no&plat_id=124&share_from=h5&share_medium=android&share_plat=android&share_session_id=ded4ebe7-6b84-4230-b000-67e9b9e1d628&share_source=COPY&share_tag=s_i&timestamp=1781318224&unique_k=gsBTdq4#themeType=1"

	projectID, err := ExtractProjectID(link)
	if err != nil {
		t.Fatalf("ExtractProjectID returned error: %v", err)
	}
	if projectID != 1001701 {
		t.Fatalf("projectID = %d, want 1001701", projectID)
	}
}

func TestExtractProjectIDAcceptsPaddedPlainID(t *testing.T) {
	projectID, err := ExtractProjectID(" 1001701+ ")
	if err != nil {
		t.Fatalf("ExtractProjectID returned error: %v", err)
	}
	if projectID != 1001701 {
		t.Fatalf("projectID = %d, want 1001701", projectID)
	}
}

func TestNormalizeBuyerAndAddress(t *testing.T) {
	buyer := normalizeBuyer(map[string]any{
		"id":          7,
		"name":        "张三",
		"personal_id": "110101199001010000",
		"phone":       "13800000000",
	})
	if buyer.ID != 7 || buyer.Name != "张三" || buyer.PersonalID == "" || buyer.Tel == "" {
		t.Fatalf("unexpected buyer: %#v", buyer)
	}

	address := normalizeAddress(map[string]any{
		"id":    9,
		"name":  "张三",
		"phone": "13800000000",
		"prov":  "上海市",
		"city":  "上海市",
		"area":  "徐汇区",
		"addr":  "测试路 1 号",
	})
	if address.ID != 9 || address.FullAddress != "上海市上海市徐汇区测试路 1 号" {
		t.Fatalf("unexpected address: %#v", address)
	}
}

func TestCreateV2StatusMessageHints(t *testing.T) {
	tests := []struct {
		name     string
		response map[string]any
		want     string
	}{
		{
			name: "default bbr",
			response: map[string]any{
				"code":    0,
				"message": "defaultBBR blocked",
			},
			want: "状态码：0，提示信息：createV2 返回 defaultBBR 警告，继续重试。",
		},
		{
			name: "captcha",
			response: map[string]any{
				"code": 100044,
			},
			want: "状态码：100044，提示信息：检测到验证码风控",
		},
		{
			name: "pay money changed",
			response: map[string]any{
				"code": 100034,
				"data": map[string]any{
					"pay_money": 68000,
				},
			},
			want: "68000 分",
		},
		{
			name: "stock not enough",
			response: map[string]any{
				"code": 100009,
			},
			want: "状态码：100009，提示信息：库存不足",
		},
		{
			name: "nested data risk code",
			response: map[string]any{
				"code": 0,
				"data": map[string]any{
					"code":    412,
					"message": "risk",
				},
			},
			want: "状态码：412，提示信息：触发风控",
		},
		{
			name: "unknown keeps api message",
			response: map[string]any{
				"code": 123456,
				"msg":  "未知错误",
			},
			want: "状态码：123456，提示信息：未知错误",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			code := createV2Code(tt.response)
			got := createV2StatusMessage(tt.response, code)
			if !strings.Contains(got, tt.want) {
				t.Fatalf("createV2StatusMessage() = %q, want contains %q", got, tt.want)
			}
		})
	}
}

func TestDefaultBBRIsRetryableCreateWarning(t *testing.T) {
	if isCreateSuccess(map[string]any{"message": "defaultBBR"}, 0) {
		t.Fatal("defaultBBR should be treated as a retryable create warning")
	}
}

func TestHotProjectOrderSendsCTokenAndPToken(t *testing.T) {
	var prepareBody map[string]any
	var createBody map[string]any
	createBodies := make([]map[string]any, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			http.SetCookie(w, &http.Cookie{Name: "kfcTime", Value: "warm", Path: "/"})
			writeJSON(t, w, mallProjectPayloadForTest(1001701))
		case "/api/ticket/order/prepare":
			if r.Method != http.MethodPost {
				t.Fatalf("prepare method = %s, want POST", r.Method)
			}
			if r.URL.Query().Get("project_id") != "1001701" {
				t.Fatalf("prepare project_id = %q, want 1001701", r.URL.Query().Get("project_id"))
			}
			requireCookieValue(t, r, "kfcTime", "warm")
			if err := json.NewDecoder(r.Body).Decode(&prepareBody); err != nil {
				t.Fatalf("decode prepare body: %v", err)
			}
			writeJSON(t, w, map[string]any{
				"code": 0,
				"data": map[string]any{
					"token":  "prepared-token",
					"ptoken": "prepared=ptoken=",
				},
			})
		case "/api/ticket/order/createV2":
			if r.Method != http.MethodPost {
				t.Fatalf("create method = %s, want POST", r.Method)
			}
			if r.URL.Query().Get("ptoken") != "preparedptoken" {
				t.Fatalf("create query ptoken = %q, want preparedptoken", r.URL.Query().Get("ptoken"))
			}
			requireCookieValue(t, r, "kfcTime", "warm")
			if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
				t.Fatalf("decode create body: %v", err)
			}
			createBodies = append(createBodies, cloneMap(createBody))
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "order-1"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.Client(), server.URL)
	task := model.Task{
		ProjectID:    1001701,
		ScreenID:     2001,
		SKUID:        3001,
		Quantity:     1,
		OrderType:    1,
		PayMoney:     68000,
		IsHotProject: true,
	}
	prepared, err := client.PrepareOrder(context.Background(), task, "SESSDATA=test")
	if err != nil {
		t.Fatalf("PrepareOrder: %v", err)
	}
	if prepared.Token != "prepared-token" || prepared.PToken != "preparedptoken" {
		t.Fatalf("unexpected prepared result: %#v", prepared)
	}
	if stringValue(prepareBody["requestSource"]) != "neul-next" || !boolValue(prepareBody["ignoreRequestLimit"]) {
		t.Fatalf("prepare risk fields missing: %#v", prepareBody)
	}
	if stringValue(prepareBody["ticket_agent"]) != "" {
		t.Fatalf("prepare ticket_agent = %q, want empty", stringValue(prepareBody["ticket_agent"]))
	}
	prepareCToken := stringValue(prepareBody["token"])
	if prepareCToken == "" {
		t.Fatalf("prepare token is empty for hot project: %#v", prepareBody)
	}
	prepareBytes := decodeCTokenForTest(t, prepareCToken)
	if prepareBytes[1] != 0 || prepareBytes[3] != 0 || prepareBytes[6] < 1 || prepareBytes[6] > 3 {
		t.Fatalf("unexpected prepare ctoken bytes: %#v", prepareBytes)
	}
	prepareTimer := int(prepareBytes[8])<<8 | int(prepareBytes[9])
	if prepareTimer < 10 || prepareTimer > 100 || prepareBytes[10] != 0 || prepareBytes[11] != 0 {
		t.Fatalf("unexpected prepare timer fields: %#v", prepareBytes[8:12])
	}

	result, err := client.CreateOrder(context.Background(), task, "SESSDATA=test", prepared)
	if err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if result.OrderID != "order-1" {
		t.Fatalf("OrderID = %q, want order-1", result.OrderID)
	}
	if _, err := client.CreateOrder(context.Background(), task, "SESSDATA=test", prepared); err != nil {
		t.Fatalf("CreateOrder second attempt: %v", err)
	}
	if len(createBodies) != 2 {
		t.Fatalf("create calls = %d, want 2", len(createBodies))
	}
	if int64Value(createBodies[0]["timestamp"]) != int64Value(createBodies[1]["timestamp"]) ||
		stringValue(createBodies[0]["ctoken"]) != stringValue(createBodies[1]["ctoken"]) {
		t.Fatalf("create payload should be reused across attempts: %#v vs %#v", createBodies[0], createBodies[1])
	}
	createCToken := stringValue(createBody["ctoken"])
	if createCToken == "" {
		t.Fatalf("create ctoken is empty for hot project: %#v", createBody)
	}
	createBytes := decodeCTokenForTest(t, createCToken)
	if createBytes[1] > 2 || createBytes[3] > 1 || createBytes[6] < 10 || createBytes[6] > 50 {
		t.Fatalf("unexpected create ctoken bytes: %#v", createBytes)
	}
	if stringValue(createBody["ptoken"]) != "preparedptoken" {
		t.Fatalf("create body ptoken = %q, want preparedptoken", stringValue(createBody["ptoken"]))
	}
	if stringValue(createBody["orderCreateUrl"]) != server.URL+"/api/ticket/order/createV2" {
		t.Fatalf("orderCreateUrl = %q", stringValue(createBody["orderCreateUrl"]))
	}
	if stringValue(createBody["project_id"]) != "1001701" {
		t.Fatalf("create body project_id = %#v, want string 1001701", createBody["project_id"])
	}
	if !boolValue(createBody["is_hot_project"]) {
		t.Fatalf("create body is_hot_project = %#v, want true", createBody["is_hot_project"])
	}
	if stringValue(createBody["requestSource"]) != "neul-next" || !boolValue(createBody["newRisk"]) {
		t.Fatalf("create risk fields missing: %#v", createBody)
	}
	if _, ok := createBody["clickPosition"]; ok {
		t.Fatalf("clickPosition should not be sent: %#v", createBody["clickPosition"])
	}
}

func TestHotProjectCreateOrderKeepsEmptyPTokenFields(t *testing.T) {
	var createBody map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path != "/api/ticket/order/createV2" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		if !strings.Contains(r.URL.RawQuery, "ptoken=") {
			t.Fatalf("create query = %q, want explicit empty ptoken", r.URL.RawQuery)
		}
		if err := json.NewDecoder(r.Body).Decode(&createBody); err != nil {
			t.Fatalf("decode create body: %v", err)
		}
		writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"orderId": "order-1"}})
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.Client(), server.URL)
	task := model.Task{
		ProjectID:    1001701,
		ScreenID:     2001,
		SKUID:        3001,
		Quantity:     1,
		OrderType:    1,
		PayMoney:     68000,
		IsHotProject: true,
	}
	if _, err := client.CreateOrder(context.Background(), task, "SESSDATA=test", OrderPrepareResult{Token: "prepared-token"}); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	if value, ok := createBody["ptoken"]; !ok || stringValue(value) != "" {
		t.Fatalf("create body ptoken = %#v, want explicit empty string", createBody["ptoken"])
	}
	if stringValue(createBody["orderCreateUrl"]) != server.URL+"/api/ticket/order/createV2" {
		t.Fatalf("orderCreateUrl = %q", stringValue(createBody["orderCreateUrl"]))
	}
	if stringValue(createBody["project_id"]) != "1001701" {
		t.Fatalf("create body project_id = %#v, want string 1001701", createBody["project_id"])
	}
	if !boolValue(createBody["is_hot_project"]) {
		t.Fatalf("create body is_hot_project = %#v, want true", createBody["is_hot_project"])
	}
	if _, ok := createBody["clickPosition"]; ok {
		t.Fatalf("clickPosition should not be sent: %#v", createBody["clickPosition"])
	}
}

func TestWarmupShowSendsHeadRequests(t *testing.T) {
	var calls int
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
		if r.Method != http.MethodHead {
			t.Fatalf("method = %s, want HEAD", r.Method)
		}
		calls++
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.Client(), server.URL)
	if err := client.WarmupShow(context.Background(), 5); err != nil {
		t.Fatalf("WarmupShow: %v", err)
	}
	if calls != 5 {
		t.Fatalf("calls = %d, want 5", calls)
	}
}

func TestWarmupShowKeepsConnectionForNextRequest(t *testing.T) {
	var newConnections atomic.Int32
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/":
			if r.Method != http.MethodHead {
				t.Fatalf("method = %s, want HEAD", r.Method)
			}
			w.WriteHeader(http.StatusNoContent)
		case "/api/ticket/order/prepare":
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"token": "prepared-token"}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			newConnections.Add(1)
		}
	}
	server.Start()
	defer server.Close()

	client := NewClientWithBaseURL(server.Client(), server.URL)
	if err := client.WarmupShow(context.Background(), 2); err != nil {
		t.Fatalf("WarmupShow: %v", err)
	}
	var response map[string]any
	endpoint := server.URL + "/api/ticket/order/prepare?project_id=1001701"
	if err := client.doJSON(context.Background(), http.MethodPost, endpoint, map[string]any{"project_id": 1001701}, "", nil, &response); err != nil {
		t.Fatalf("doJSON: %v", err)
	}
	if newConnections.Load() != 1 {
		t.Fatalf("new connections = %d, want 1", newConnections.Load())
	}
}

func TestDoJSONReturnsHTTPStatusError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusPreconditionFailed)
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.Client(), server.URL)
	var response map[string]any
	err := client.doJSON(context.Background(), http.MethodPost, server.URL+"/api/ticket/order/createV2", nil, "", nil, &response)
	if err == nil {
		t.Fatal("doJSON returned nil error, want HTTP status error")
	}
	if got := HTTPStatusCode(err); got != http.StatusPreconditionFailed {
		t.Fatalf("HTTPStatusCode() = %d, want %d", got, http.StatusPreconditionFailed)
	}
	if got := err.Error(); got != "Bilibili 接口返回状态码 412" {
		t.Fatalf("error = %q, want HTTP status message", got)
	}
}

func TestFetchPurchaseContextMapsProjectBuyersAndAddresses(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			writeJSON(t, w, map[string]any{
				"code":    0,
				"success": true,
				"data": map[string]any{
					"projectId":   1001701,
					"projectName": "测试项目",
					"hotProject":  true,
					"screenList": []map[string]any{
						{
							"id":          2001,
							"name":        "晚场",
							"start_time":  1781318224,
							"express_fee": 0,
							"ticket_list": []map[string]any{
								{
									"id":         3001,
									"desc":       "VIP",
									"price":      68000,
									"sale_start": "2026-06-13 20:00:00",
									"clickable":  true,
								},
							},
						},
					},
					"skuVenueInfo": map[string]any{
						"name":           "测试场馆",
						"address_detail": "测试地址",
					},
				},
			})
		case "/api/ticket/linkgoods/list":
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		case "/x/web-interface/nav":
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"uname": "测试用户"}})
		case "/api/ticket/buyer/list":
			writeJSON(t, w, map[string]any{
				"code": 0,
				"data": map[string]any{
					"list": []map[string]any{
						{"id": 7, "name": "张三", "personal_id": "110101199001010000", "phone": "13800000000"},
					},
				},
			})
		case "/api/ticket/addr/list":
			writeJSON(t, w, map[string]any{
				"code": 0,
				"data": map[string]any{
					"addr_list": []map[string]any{
						{"id": 9, "name": "张三", "phone": "13800000000", "prov": "上海市", "city": "上海市", "area": "徐汇区", "addr": "测试路 1 号"},
					},
				},
			})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.Client(), server.URL)
	project, err := client.FetchPurchaseContext(context.Background(), "1001701", "SESSDATA=test")
	if err != nil {
		t.Fatalf("FetchPurchaseContext: %v", err)
	}

	if project.ProjectID != 1001701 || project.ProjectName != "测试项目" {
		t.Fatalf("unexpected project: %#v", project)
	}
	if project.Username != "测试用户" {
		t.Fatalf("Username = %q, want 测试用户", project.Username)
	}
	if len(project.TicketOptions) != 1 || project.TicketOptions[0].SKUID != 3001 {
		t.Fatalf("unexpected ticket options: %#v", project.TicketOptions)
	}
	if !project.IsHotProject || !project.TicketOptions[0].IsHotProject {
		t.Fatalf("hot project flag was not propagated: project=%v ticket=%v", project.IsHotProject, project.TicketOptions[0].IsHotProject)
	}
	if !project.TicketOptions[0].Clickable {
		t.Fatalf("ticket clickable = false, want true")
	}
	if len(project.Buyers) != 1 || project.Buyers[0].Name != "张三" {
		t.Fatalf("unexpected buyers: %#v", project.Buyers)
	}
	if len(project.Addresses) != 1 || project.Addresses[0].FullAddress != "上海市上海市徐汇区测试路 1 号" {
		t.Fatalf("unexpected addresses: %#v", project.Addresses)
	}
}

func TestFetchProjectDoesNotFallbackToOldProjectAPI(t *testing.T) {
	var oldProjectCalls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			http.Error(w, "temporary failure", http.StatusBadGateway)
		case "/api/ticket/project/getV2":
			oldProjectCalls.Add(1)
			http.Error(w, "old project api should not be requested", http.StatusInternalServerError)
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.Client(), server.URL)
	if _, err := client.FetchProject(context.Background(), "1001701", "SESSDATA=test"); err == nil {
		t.Fatal("FetchProject returned nil error, want new project api error")
	}
	if oldProjectCalls.Load() != 0 {
		t.Fatalf("old project api calls = %d, want 0", oldProjectCalls.Load())
	}
}

func TestCheckTicketStatusUsesClickableFlag(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/mall-search-items/items_detail/info":
			writeJSON(t, w, map[string]any{
				"code":    0,
				"success": true,
				"data": map[string]any{
					"projectId":   1001701,
					"projectName": "测试项目",
					"screenList": []map[string]any{
						{
							"id":   2001,
							"name": "晚场",
							"ticket_list": []map[string]any{
								{
									"id":               3001,
									"desc":             "VIP",
									"price":            68000,
									"sale_start":       "2026-06-13 20:00:00",
									"sale_flag_number": 2,
									"clickable":        false,
								},
							},
						},
					},
				},
			})
		case "/api/ticket/linkgoods/list":
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"list": []any{}}})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.Client(), server.URL)
	option, available, err := client.CheckTicketStatus(context.Background(), model.Task{
		ProjectID: 1001701,
		ScreenID:  2001,
		SKUID:     3001,
	}, "SESSDATA=test")
	if err != nil {
		t.Fatalf("CheckTicketStatus: %v", err)
	}
	if option.SaleStatus != "预售" {
		t.Fatalf("SaleStatus = %q, want 预售", option.SaleStatus)
	}
	if option.Clickable {
		t.Fatal("Clickable = true, want false")
	}
	if available {
		t.Fatal("available = true, want false when clickable is false")
	}
}

func TestFetchAccountContextDoesNotFetchProjectDetail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/x/web-interface/nav":
			writeJSON(t, w, map[string]any{"code": 0, "data": map[string]any{"uname": "测试用户"}})
		case "/api/ticket/buyer/list":
			writeJSON(t, w, map[string]any{
				"code": 0,
				"data": map[string]any{
					"list": []map[string]any{
						{"id": 7, "name": "张三", "personal_id": "110101199001010000"},
					},
				},
			})
		case "/api/ticket/addr/list":
			writeJSON(t, w, map[string]any{
				"code": 0,
				"data": map[string]any{
					"addr_list": []map[string]any{
						{"id": 9, "name": "张三", "phone": "13800000000", "prov": "上海市", "city": "上海市", "area": "徐汇区", "addr": "测试路 1 号"},
					},
				},
			})
		default:
			t.Fatalf("unexpected request path: %s", r.URL.Path)
		}
	}))
	defer server.Close()

	client := NewClientWithBaseURL(server.Client(), server.URL)
	context, err := client.FetchAccountContext(context.Background(), 1001701, "SESSDATA=test")
	if err != nil {
		t.Fatalf("FetchAccountContext: %v", err)
	}

	if context.ProjectID != 1001701 || context.Username != "测试用户" {
		t.Fatalf("unexpected context header: %#v", context)
	}
	if len(context.Buyers) != 1 || context.Buyers[0].Name != "张三" {
		t.Fatalf("unexpected buyers: %#v", context.Buyers)
	}
	if len(context.Addresses) != 1 || context.Addresses[0].ID != 9 {
		t.Fatalf("unexpected addresses: %#v", context.Addresses)
	}
}

func TestFormatUnixUsesChinaTime(t *testing.T) {
	originalLocal := time.Local
	time.Local = time.UTC
	t.Cleanup(func() {
		time.Local = originalLocal
	})

	instant, err := time.Parse(time.RFC3339, "2026-06-13T20:00:00+08:00")
	if err != nil {
		t.Fatalf("parse instant: %v", err)
	}
	if got := formatUnix(instant.Unix()); got != "2026-06-13 20:00:00" {
		t.Fatalf("formatUnix seconds = %q, want 2026-06-13 20:00:00", got)
	}
	if got := formatUnix(instant.UnixMilli()); got != "2026-06-13 20:00:00" {
		t.Fatalf("formatUnix millis = %q, want 2026-06-13 20:00:00", got)
	}
}

func requireCookieValue(t *testing.T, r *http.Request, name string, want string) {
	t.Helper()
	cookie, err := r.Cookie(name)
	if err != nil {
		t.Fatalf("cookie %s missing in %q", name, r.Header.Get("Cookie"))
	}
	if cookie.Value != want {
		t.Fatalf("cookie %s = %q, want %q", name, cookie.Value, want)
	}
}

func mallProjectPayloadForTest(projectID int64) map[string]any {
	return map[string]any{
		"code":    0,
		"success": true,
		"data": map[string]any{
			"projectId":   projectID,
			"projectName": "测试项目",
			"hotProject":  true,
			"screenList": []map[string]any{
				{
					"id":         2001,
					"name":       "晚场",
					"start_time": 1781318224,
					"ticket_list": []map[string]any{
						{
							"id":         3001,
							"desc":       "VIP",
							"price":      68000,
							"sale_start": "2026-06-13 20:00:00",
							"clickable":  true,
						},
					},
				},
			},
		},
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("Encode: %v", err)
	}
}
