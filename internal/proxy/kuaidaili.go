package proxy

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fdcs99/biligo/internal/model"
)

const (
	kuaidailiGetDPSEndpoint      = "dps.kdlapi.com/api/getdps"
	kuaidailiGetSecretTokenURL   = "https://auth.kdlapi.com/api/get_secret_token"
	kuaidailiDefaultPullQuantity = 5
)

func PullKuaidailiDPS(ctx context.Context, group model.ProxyGroup) ([]model.ProxyNodeInput, error) {
	config := group.APIConfig
	secretID := strings.TrimSpace(config["secretId"])
	secretKey := strings.TrimSpace(config["secretKey"])
	if secretID == "" || secretKey == "" {
		return nil, errors.New("快代理 SecretId 和 SecretKey 不能为空")
	}

	num := kuaidailiDefaultPullQuantity
	if parsed, err := strconv.Atoi(strings.TrimSpace(config["num"])); err == nil && parsed > 0 {
		num = parsed
	}
	protocol := model.NormalizeProxyProtocol(firstNonEmpty(config["proxyProtocol"], config["protocol"]))
	signType := strings.ToLower(strings.TrimSpace(config["signType"]))
	if signType == "" {
		signType = "hmacsha1"
	}

	params := map[string]string{
		"secret_id": secretID,
		"sign_type": signType,
		"num":       strconv.Itoa(num),
		"format":    "json",
		"f_auth":    "1",
	}
	for key, value := range config {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key == "" || value == "" || isReservedKuaidailiConfigKey(key) {
			continue
		}
		params[key] = value
	}

	switch signType {
	case "token":
		token := strings.TrimSpace(config["secretToken"])
		if token == "" {
			var err error
			token, err = getKuaidailiSecretToken(ctx, secretID, secretKey)
			if err != nil {
				return nil, err
			}
		}
		params["signature"] = token
	case "simple":
		params["signature"] = secretKey
	case "hmacsha1":
		params["timestamp"] = strconv.FormatInt(time.Now().Unix(), 10)
		params["signature"] = signKuaidaili("GET", kuaidailiGetDPSEndpoint, params, secretKey)
	default:
		return nil, errors.New("快代理 signType 仅支持 hmacsha1、token 或 simple")
	}

	endpoint := url.URL{Scheme: "https", Host: "dps.kdlapi.com", Path: "/api/getdps"}
	query := endpoint.Query()
	for key, value := range params {
		query.Set(key, value)
	}
	endpoint.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return nil, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("快代理接口返回状态码 %d", resp.StatusCode)
	}

	items, err := parseKuaidailiProxyResponse(body)
	if err != nil {
		return nil, err
	}
	nodes := make([]model.ProxyNodeInput, 0, len(items))
	for _, item := range items {
		node, err := parseProxyItem(item, protocol)
		if err != nil {
			return nil, err
		}
		nodes = append(nodes, node)
	}
	return nodes, nil
}

func getKuaidailiSecretToken(ctx context.Context, secretID string, secretKey string) (string, error) {
	form := url.Values{}
	form.Set("secret_id", secretID)
	form.Set("secret_key", secretKey)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, kuaidailiGetSecretTokenURL, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			SecretToken string `json:"secret_token"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", err
	}
	if payload.Code != 0 {
		return "", fmt.Errorf("获取快代理密钥令牌失败：%s", payload.Msg)
	}
	if payload.Data.SecretToken == "" {
		return "", errors.New("获取快代理密钥令牌失败：响应缺少 secret_token")
	}
	return payload.Data.SecretToken, nil
}

func signKuaidaili(method string, endpoint string, params map[string]string, secretKey string) string {
	keys := make([]string, 0, len(params))
	for key := range params {
		if key == "signature" {
			continue
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	parts := make([]string, 0, len(keys))
	for _, key := range keys {
		parts = append(parts, key+"="+params[key])
	}
	raw := method + strings.Split(endpoint, ".com")[1] + "?" + strings.Join(parts, "&")
	hash := hmac.New(sha1.New, []byte(secretKey))
	hash.Write([]byte(raw))
	return base64.StdEncoding.EncodeToString(hash.Sum(nil))
}

func parseKuaidailiProxyResponse(body []byte) ([]string, error) {
	var payload struct {
		Code int             `json:"code"`
		Msg  string          `json:"msg"`
		Data json.RawMessage `json:"data"`
	}
	if err := json.NewDecoder(bytes.NewReader(body)).Decode(&payload); err != nil {
		text := strings.TrimSpace(string(body))
		if strings.HasPrefix(text, "ERROR") {
			return nil, errors.New(text)
		}
		lines := strings.FieldsFunc(text, func(r rune) bool {
			return r == '\n' || r == '\r' || r == ','
		})
		return compactStrings(lines), nil
	}
	if payload.Code != 0 {
		return nil, fmt.Errorf("快代理接口返回错误：%s", payload.Msg)
	}
	var data struct {
		ProxyList []string `json:"proxy_list"`
	}
	if err := json.Unmarshal(payload.Data, &data); err == nil && len(data.ProxyList) > 0 {
		return data.ProxyList, nil
	}
	var list []string
	if err := json.Unmarshal(payload.Data, &list); err == nil {
		return list, nil
	}
	return nil, errors.New("快代理接口响应缺少 proxy_list")
}

func parseProxyItem(item string, protocol string) (model.ProxyNodeInput, error) {
	item = strings.TrimSpace(item)
	if item == "" {
		return model.ProxyNodeInput{}, errors.New("代理节点为空")
	}
	node := model.ProxyNodeInput{Protocol: protocol}
	if strings.Contains(item, "@") {
		parts := strings.SplitN(item, "@", 2)
		authParts := strings.SplitN(parts[0], ":", 2)
		hostParts := strings.SplitN(parts[1], ":", 2)
		if len(authParts) == 2 && len(hostParts) == 2 {
			node.Username = authParts[0]
			node.Password = authParts[1]
			node.Host = hostParts[0]
			node.Port, _ = strconv.Atoi(hostParts[1])
		} else {
			hostParts = strings.SplitN(parts[0], ":", 2)
			authParts = strings.SplitN(parts[1], ":", 2)
			if len(hostParts) == 2 && len(authParts) == 2 {
				node.Host = hostParts[0]
				node.Port, _ = strconv.Atoi(hostParts[1])
				node.Username = authParts[0]
				node.Password = authParts[1]
			}
		}
	} else {
		parts := strings.Split(item, ":")
		switch len(parts) {
		case 2:
			node.Host = parts[0]
			node.Port, _ = strconv.Atoi(parts[1])
		case 4:
			if isLikelyPort(parts[1]) {
				node.Host = parts[0]
				node.Port, _ = strconv.Atoi(parts[1])
				node.Username = parts[2]
				node.Password = parts[3]
			} else {
				node.Username = parts[0]
				node.Password = parts[1]
				node.Host = parts[2]
				node.Port, _ = strconv.Atoi(parts[3])
			}
		}
	}
	if node.Host == "" || node.Port <= 0 {
		return model.ProxyNodeInput{}, fmt.Errorf("无法解析代理节点：%s", item)
	}
	node.Name = node.Host + ":" + strconv.Itoa(node.Port)
	return node, nil
}

func isLikelyPort(value string) bool {
	port, err := strconv.Atoi(value)
	return err == nil && port > 0 && port <= 65535
}

func isReservedKuaidailiConfigKey(key string) bool {
	switch key {
	case "secretId", "secretKey", "secretToken", "signType", "num", "pullTimes", "pullBeforeMinutes", "proxyProtocol", "protocol":
		return true
	default:
		return false
	}
}

func compactStrings(values []string) []string {
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			result = append(result, value)
		}
	}
	return result
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
