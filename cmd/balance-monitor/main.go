// balance-monitor 轮询主网 JSON-RPC 上指定地址的 eth_getBalance，
// 当余额低于配置的 min_balance 时通过飞书 Webhook 报警（适用于发奖励等热钱包监控）。
//
// 用法:
//
//	go run ./cmd/balance-monitor -config config.json
//
// 配置示例见 monitor.example.json（复制为 config.json 后修改；config.json 已被 .gitignore 忽略）
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

const nativeDecimals = 18

type config struct {
	PollIntervalSeconds int           `json:"poll_interval_seconds"`
	RPCURL              string        `json:"rpc_url"`
	FeishuWebhookURL    string        `json:"feishu_webhook_url"`
	DefaultMinBalance   string        `json:"default_min_balance"`
	Addresses           []addressSpec `json:"addresses"`
}

type addressSpec struct {
	Name        string `json:"name"`
	Address     string `json:"address"`
	MinBalance  string `json:"min_balance"`
	minBalanceWei *big.Int
}

type rpcBody struct {
	JSONRPC string        `json:"jsonrpc"`
	Method  string        `json:"method"`
	Params  []interface{} `json:"params"`
	ID      int           `json:"id"`
}

type rpcResp struct {
	Result string `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

type addressState struct {
	spec addressSpec

	mu sync.Mutex

	lowAlertSent  bool
	errorAlertSent bool
	lastError     string
	initialized   bool
	lastBalance   *big.Int
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	configPath := flag.String("config", "config.json", "path to JSON config file")
	flag.Parse()

	cfg, err := loadConfig(*configPath)
	if err != nil {
		return err
	}

	states := make([]*addressState, len(cfg.Addresses))
	for i, a := range cfg.Addresses {
		states[i] = &addressState{spec: a, lastBalance: new(big.Int)}
	}

	pollInterval := time.Duration(cfg.PollIntervalSeconds) * time.Second
	httpClient := &http.Client{Timeout: 15 * time.Second}

	fmt.Fprintf(os.Stderr, "monitoring %d address(es) via %s, poll=%s\n",
		len(cfg.Addresses), cfg.RPCURL, pollInterval)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	checkAll := func() {
		for _, st := range states {
			checkAddress(httpClient, cfg, st)
		}
	}

	checkAll()
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintln(os.Stderr, "stopped")
			return nil
		case <-ticker.C:
			checkAll()
		}
	}
}

func loadConfig(path string) (*config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config %q: %w", path, err)
	}

	var cfg config
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil, fmt.Errorf("parse config %q: %w", path, err)
	}

	if cfg.PollIntervalSeconds <= 0 {
		cfg.PollIntervalSeconds = 60
	}
	cfg.RPCURL = strings.TrimSpace(cfg.RPCURL)
	cfg.FeishuWebhookURL = strings.TrimSpace(cfg.FeishuWebhookURL)
	cfg.DefaultMinBalance = strings.TrimSpace(cfg.DefaultMinBalance)

	if cfg.RPCURL == "" {
		return nil, errors.New("rpc_url is required")
	}
	if cfg.FeishuWebhookURL == "" {
		return nil, errors.New("feishu_webhook_url is required")
	}
	if len(cfg.Addresses) == 0 {
		return nil, errors.New("addresses must contain at least one entry")
	}

	var defaultMin *big.Int
	if cfg.DefaultMinBalance != "" {
		defaultMin, err = parseNativeAmount(cfg.DefaultMinBalance)
		if err != nil {
			return nil, fmt.Errorf("default_min_balance: %w", err)
		}
	}

	for i := range cfg.Addresses {
		cfg.Addresses[i].Name = strings.TrimSpace(cfg.Addresses[i].Name)
		cfg.Addresses[i].Address = strings.TrimSpace(cfg.Addresses[i].Address)
		cfg.Addresses[i].MinBalance = strings.TrimSpace(cfg.Addresses[i].MinBalance)

		if cfg.Addresses[i].Address == "" {
			return nil, fmt.Errorf("addresses[%d].address is required", i)
		}
		if cfg.Addresses[i].Name == "" {
			cfg.Addresses[i].Name = cfg.Addresses[i].Address
		}

		minStr := cfg.Addresses[i].MinBalance
		if minStr == "" {
			if defaultMin == nil {
				return nil, fmt.Errorf("addresses[%d] needs min_balance or set default_min_balance", i)
			}
			cfg.Addresses[i].minBalanceWei = new(big.Int).Set(defaultMin)
		} else {
			cfg.Addresses[i].minBalanceWei, err = parseNativeAmount(minStr)
			if err != nil {
				return nil, fmt.Errorf("addresses[%d].min_balance: %w", i, err)
			}
		}
	}

	return &cfg, nil
}

func checkAddress(client *http.Client, cfg *config, st *addressState) {
	balance, err := ethGetBalance(client, cfg.RPCURL, st.spec.Address)
	now := time.Now()

	st.mu.Lock()
	defer st.mu.Unlock()

	if err != nil {
		st.lastError = err.Error()
		if !st.errorAlertSent {
			msg := fmt.Sprintf("[VCityChain 余额监控]\n地址: %s\nAccount: %s\n错误: 无法查询余额\n详情: %s\n时间: %s",
				st.spec.Name, st.spec.Address, st.lastError, now.Format(time.RFC3339))
			if sendErr := sendFeishuAlert(client, cfg.FeishuWebhookURL, msg); sendErr != nil {
				fmt.Fprintf(os.Stderr, "feishu alert failed for %s: %v\n", st.spec.Name, sendErr)
			} else {
				st.errorAlertSent = true
				fmt.Fprintf(os.Stderr, "ALERT rpc error: %s (%v)\n", st.spec.Name, err)
			}
		}
		return
	}

	if st.errorAlertSent {
		msg := fmt.Sprintf("[VCityChain 余额监控]\n地址: %s\nAccount: %s\n恢复: RPC 查询已恢复\n当前余额: %s\n时间: %s",
			st.spec.Name, st.spec.Address, formatNativeAmount(balance), now.Format(time.RFC3339))
		_ = sendFeishuAlert(client, cfg.FeishuWebhookURL, msg)
		st.errorAlertSent = false
		st.lastError = ""
	}

	st.lastBalance.Set(balance)
	if !st.initialized {
		fmt.Fprintf(os.Stderr, "init %s balance=%s min=%s\n",
			st.spec.Name, formatNativeAmount(balance), formatNativeAmount(st.spec.minBalanceWei))
		st.initialized = true
	}

	threshold := st.spec.minBalanceWei
	isLow := balance.Cmp(threshold) < 0

	if isLow && !st.lowAlertSent {
		msg := fmt.Sprintf("[VCityChain 余额监控]\n地址: %s\nAccount: %s\n告警: 余额低于阈值\n当前余额: %s\n最低阈值: %s\n时间: %s",
			st.spec.Name,
			st.spec.Address,
			formatNativeAmount(balance),
			formatNativeAmount(threshold),
			now.Format(time.RFC3339))
		if sendErr := sendFeishuAlert(client, cfg.FeishuWebhookURL, msg); sendErr != nil {
			fmt.Fprintf(os.Stderr, "feishu alert failed for %s: %v\n", st.spec.Name, sendErr)
			return
		}
		st.lowAlertSent = true
		fmt.Fprintf(os.Stderr, "ALERT low balance: %s balance=%s min=%s\n",
			st.spec.Name, formatNativeAmount(balance), formatNativeAmount(threshold))
		return
	}

	if !isLow && st.lowAlertSent {
		msg := fmt.Sprintf("[VCityChain 余额监控]\n地址: %s\nAccount: %s\n恢复: 余额已回到阈值以上\n当前余额: %s\n最低阈值: %s\n时间: %s",
			st.spec.Name,
			st.spec.Address,
			formatNativeAmount(balance),
			formatNativeAmount(threshold),
			now.Format(time.RFC3339))
		_ = sendFeishuAlert(client, cfg.FeishuWebhookURL, msg)
		st.lowAlertSent = false
		fmt.Fprintf(os.Stderr, "RECOVER balance: %s balance=%s\n", st.spec.Name, formatNativeAmount(balance))
	}
}

func ethGetBalance(client *http.Client, rpcURL, account string) (*big.Int, error) {
	body, err := json.Marshal(rpcBody{
		JSONRPC: "2.0",
		Method:  "eth_getBalance",
		Params:  []interface{}{account, "latest"},
		ID:      1,
	})
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest(http.MethodPost, rpcURL, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(raw))
	}

	var rr rpcResp
	if err := json.Unmarshal(raw, &rr); err != nil {
		return nil, err
	}
	if rr.Error != nil {
		return nil, fmt.Errorf("rpc error %d: %s", rr.Error.Code, rr.Error.Message)
	}

	return parseHexBigInt(rr.Result)
}

func parseHexBigInt(hexStr string) (*big.Int, error) {
	hexStr = strings.TrimSpace(hexStr)
	if hexStr == "" {
		return nil, errors.New("empty hex string")
	}
	if strings.HasPrefix(hexStr, "0x") || strings.HasPrefix(hexStr, "0X") {
		hexStr = hexStr[2:]
	}
	if hexStr == "" {
		return big.NewInt(0), nil
	}
	n := new(big.Int)
	if _, ok := n.SetString(hexStr, 16); !ok {
		return nil, fmt.Errorf("invalid hex %q", hexStr)
	}
	return n, nil
}

func parseNativeAmount(s string) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil, errors.New("empty amount")
	}

	parts := strings.SplitN(s, ".", 2)
	intPart := parts[0]
	if intPart == "" {
		intPart = "0"
	}
	if _, ok := new(big.Int).SetString(intPart, 10); !ok {
		return nil, fmt.Errorf("invalid integer part %q", intPart)
	}

	fracPart := ""
	if len(parts) == 2 {
		fracPart = parts[1]
		if fracPart == "" {
			return nil, fmt.Errorf("invalid amount %q", s)
		}
		for _, c := range fracPart {
			if c < '0' || c > '9' {
				return nil, fmt.Errorf("invalid amount %q", s)
			}
		}
		if len(fracPart) > nativeDecimals {
			return nil, fmt.Errorf("too many decimal places in %q (max %d)", s, nativeDecimals)
		}
	}

	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(nativeDecimals), nil)
	result := new(big.Int)

	intVal, ok := new(big.Int).SetString(intPart, 10)
	if !ok {
		return nil, fmt.Errorf("invalid amount %q", s)
	}
	result.Mul(intVal, scale)

	if fracPart != "" {
		fracPadded := fracPart + strings.Repeat("0", nativeDecimals-len(fracPart))
		fracVal, ok := new(big.Int).SetString(fracPadded, 10)
		if !ok {
			return nil, fmt.Errorf("invalid amount %q", s)
		}
		result.Add(result, fracVal)
	}

	return result, nil
}

func formatNativeAmount(wei *big.Int) string {
	if wei == nil {
		return "0"
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(nativeDecimals), nil)
	intPart := new(big.Int).Div(new(big.Int).Set(wei), scale)
	fracPart := new(big.Int).Mod(new(big.Int).Set(wei), scale)

	if fracPart.Sign() == 0 {
		return intPart.String()
	}

	fracStr := fmt.Sprintf("%0*s", nativeDecimals, fracPart.String())
	fracStr = strings.TrimRight(fracStr, "0")
	return intPart.String() + "." + fracStr
}

func sendFeishuAlert(client *http.Client, webhookURL, text string) error {
	payload, err := json.Marshal(map[string]interface{}{
		"msg_type": "text",
		"content": map[string]string{
			"text": text,
		},
	})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("feishu http %d: %s", resp.StatusCode, string(raw))
	}

	var result struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
	}
	if err := json.Unmarshal(raw, &result); err != nil {
		return nil
	}
	if result.Code != 0 {
		return fmt.Errorf("feishu api code=%d msg=%s", result.Code, result.Msg)
	}
	return nil
}
