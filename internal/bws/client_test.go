package bws

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/fdcs99/biligo/internal/model"
)

func TestFetchInfoBuildsRequestAndNormalizesItems(t *testing.T) {
	var gotQuery string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/reserve/info" {
			t.Fatalf("path = %s, want /reserve/info", r.URL.Path)
		}
		gotQuery = r.URL.RawQuery
		if cookie := r.Header.Get("Cookie"); !strings.Contains(cookie, "bili_jct=csrf-token") {
			t.Fatalf("Cookie = %q, want bili_jct", cookie)
		}
		writeJSON(t, w, map[string]any{
			"code":    0,
			"message": "0",
			"data": map[string]any{
				"user_ticket_info": map[string]any{
					"20260710": map[string]any{
						"ticket":      "TICKET-10",
						"screen_name": "7月10日",
						"sku_name":    "普通票",
						"is_vip":      false,
					},
				},
				"user_reserve_info": map[string]any{
					"20260710": []any{
						map[string]any{"reserve_id": 99},
					},
				},
				"reserve_list": map[string]any{
					"20260710": []any{
						map[string]any{
							"reserve_id":         99,
							"act_title":          "已预约活动\n",
							"act_begin_time":     1783674000,
							"act_end_time":       1783677600,
							"reserve_begin_time": 1783670000,
							"state":              1,
							"describe_info":      "普通说明",
						},
						map[string]any{
							"reserve_id":         100,
							"act_title":          "VIP 活动",
							"act_begin_time":     1783677600,
							"reserve_begin_time": 1783670100,
							"is_vip_ticket":      1,
							"next_reserve": map[string]any{
								"reserve_begin_time": 1783670200,
							},
							"describe_info": "预约只是签售资格，现场签售需购买up主周边。",
						},
					},
				},
			},
		})
	}))
	defer server.Close()

	client := NewClient(server.Client()).WithBaseURL(server.URL)
	info, err := client.FetchInfo(context.Background(), "SESSDATA=x; bili_jct=csrf-token", model.BWSInfoInput{
		Year:         "202601",
		ReserveDates: "20260710",
		ReserveType:  model.BWSReserveTypeGoods,
	})
	if err != nil {
		t.Fatalf("FetchInfo: %v", err)
	}
	if !strings.Contains(gotQuery, "csrf=csrf-token") || !strings.Contains(gotQuery, "reserve_date=20260710") || !strings.Contains(gotQuery, "reserve_type=1") || !strings.Contains(gotQuery, "year=202601") {
		t.Fatalf("query = %s", gotQuery)
	}
	if len(info.Tickets) != 1 || info.Tickets[0].TicketNo != "TICKET-10" {
		t.Fatalf("tickets = %#v", info.Tickets)
	}
	if len(info.Items) != 2 {
		t.Fatalf("items len = %d, want 2", len(info.Items))
	}
	if !info.Items[0].Reserved || info.Items[0].Title != "已预约活动" {
		t.Fatalf("first item = %#v", info.Items[0])
	}
	if info.Items[1].EffectiveReserveBeginTime != 1783670200 || !info.Items[1].IsSecondPay {
		t.Fatalf("vip item = %#v", info.Items[1])
	}
}

func TestReservePostsExpectedFormAndMapsHTTPStatus(t *testing.T) {
	var gotForm string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/reserve/do" {
			t.Fatalf("path = %s, want /reserve/do", r.URL.Path)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		gotForm = r.Form.Encode()
		writeJSON(t, w, map[string]any{"code": 0, "message": "success"})
	}))
	defer server.Close()

	client := NewClient(server.Client()).WithBaseURL(server.URL)
	result, err := client.Reserve(context.Background(), "bili_jct=csrf-token", ReserveInput{
		TicketNo:        "TICKET-10",
		ReserveID:       123,
		Year:            "202601",
		TimestampMillis: 1783670000123,
	})
	if err != nil {
		t.Fatalf("Reserve: %v", err)
	}
	if result.Code != 0 {
		t.Fatalf("result code = %d, want 0", result.Code)
	}
	for _, part := range []string{"ticket_no=TICKET-10", "csrf=csrf-token", "inter_reserve_id=123", "year=202601", "ts=1783670000123"} {
		if !strings.Contains(gotForm, part) {
			t.Fatalf("form = %s, missing %s", gotForm, part)
		}
	}
}

func TestFetchInfoRejectsMissingCSRF(t *testing.T) {
	client := NewClient(nil)
	_, err := client.FetchInfo(context.Background(), "SESSDATA=x", model.BWSInfoInput{})
	if err == nil || !strings.Contains(err.Error(), "bili_jct") {
		t.Fatalf("err = %v, want missing bili_jct", err)
	}
}

func writeJSON(t *testing.T, w http.ResponseWriter, payload any) {
	t.Helper()
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("Encode: %v", err)
	}
}
