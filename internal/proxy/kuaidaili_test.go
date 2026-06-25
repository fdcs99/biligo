package proxy

import (
	"encoding/json"
	"testing"

	"github.com/fdcs99/biligo/internal/model"
)

func TestParseKuaidailiProxyItems(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"code": 0,
		"data": map[string]any{
			"proxy_list": []string{
				"127.0.0.1:8080:user:pass",
				"user2:pass2:127.0.0.2:8081",
			},
		},
	})
	items, err := parseKuaidailiProxyResponse(body)
	if err != nil {
		t.Fatalf("parseKuaidailiProxyResponse: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("items len = %d, want 2", len(items))
	}
	first, err := parseProxyItem(items[0], model.ProxyProtocolHTTP)
	if err != nil {
		t.Fatalf("parse first proxy: %v", err)
	}
	if first.Host != "127.0.0.1" || first.Port != 8080 || first.Username != "user" || first.Password != "pass" {
		t.Fatalf("unexpected first proxy: %#v", first)
	}
	second, err := parseProxyItem(items[1], model.ProxyProtocolSOCKS5)
	if err != nil {
		t.Fatalf("parse second proxy: %v", err)
	}
	if second.Host != "127.0.0.2" || second.Port != 8081 || second.Username != "user2" || second.Password != "pass2" || second.Protocol != model.ProxyProtocolSOCKS5 {
		t.Fatalf("unexpected second proxy: %#v", second)
	}
}

func TestKuaidailiReservedConfigKeysIncludeRuntimeOptions(t *testing.T) {
	for _, key := range []string{"num", "pullTimes", "pullBeforeMinutes", "proxyProtocol"} {
		if !isReservedKuaidailiConfigKey(key) {
			t.Fatalf("%s should not be passed through to Kuaidaili query params", key)
		}
	}
}
