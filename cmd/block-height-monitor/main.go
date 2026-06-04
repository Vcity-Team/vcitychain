// block-height-monitor 轮询多个主网 JSON-RPC 节点的 eth_blockNumber，
// 若某节点在配置的 stale_threshold_seconds 内区块高度未增长，则通过飞书 Webhook 报警。
//
// 用法:
//
//	go run ./cmd/block-height-monitor -config config.json
//
// 配置示例见 monitor.example.json（复制为 config.json 后修改；config.json 已被 .gitignore 忽略）
package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

type config struct {
	PollIntervalSeconds   int        `json:"poll_interval_seconds"`
	StaleThresholdSeconds int        `json:"stale_threshold_seconds"`
	FeishuWebhookURL      string     `json:"feishu_webhook_url"`
	Nodes                 []nodeSpec `json:"nodes"`
}

type nodeSpec struct {
	Name string `json:"name"`
	URL  string `json:"url"`
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

type nodeState struct {
	spec nodeSpec

	mu sync.Mutex

	lastHeight      uint64
	lastChangeAt    time.Time
	staleAlertSent  bool
	errorAlertSent  bool
	lastError       string
	lastErrorAt     time.Time
	initialized     bool
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

	states := make([]*nodeState, len(cfg.Nodes))
	for i, n := range cfg.Nodes {
		states[i] = &nodeState{spec: n}
	}

	pollInterval := time.Duration(cfg.PollIntervalSeconds) * time.Second
	staleThreshold := time.Duration(cfg.StaleThresholdSeconds) * time.Second
	httpClient := &http.Client{Timeout: 15 * time.Second}

	fmt.Fprintf(os.Stderr, "monitoring %d node(s), poll=%s, stale_threshold=%s\n",
		len(cfg.Nodes), pollInterval, staleThreshold)

	ctx, stop := signal.NotifyContext(nil, os.Interrupt, syscall.SIGTERM)
	defer stop()

	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()

	checkAll := func() {
		for _, st := range states {
			checkNode(httpClient, cfg, st, staleThreshold)
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
		cfg.PollIntervalSeconds = 10
	}
	if cfg.StaleThresholdSeconds <= 0 {
		return nil, errors.New("stale_threshold_seconds must be > 0")
	}
	if strings.TrimSpace(cfg.FeishuWebhookURL) == "" {
		return nil, errors.New("feishu_webhook_url is required")
	}
	if len(cfg.Nodes) == 0 {
		return nil, errors.New("nodes must contain at least one RPC endpoint")
	}

	for i := range cfg.Nodes {
		cfg.Nodes[i].URL = strings.TrimSpace(cfg.Nodes[i].URL)
		cfg.Nodes[i].Name = strings.TrimSpace(cfg.Nodes[i].Name)
		if cfg.Nodes[i].URL == "" {
			return nil, fmt.Errorf("nodes[%d].url is required", i)
		}
		if cfg.Nodes[i].Name == "" {
			cfg.Nodes[i].Name = cfg.Nodes[i].URL
		}
	}

	return &cfg, nil
}

func checkNode(client *http.Client, cfg *config, st *nodeState, staleThreshold time.Duration) {
	height, err := ethBlockNumber(client, st.spec.URL)
	now := time.Now()

	st.mu.Lock()
	defer st.mu.Unlock()

	if err != nil {
		st.lastError = err.Error()
		st.lastErrorAt = now
		if !st.errorAlertSent {
			msg := fmt.Sprintf("[VCityChain 区块高度监控]\n节点: %s\nRPC: %s\n错误: 无法获取区块高度\n详情: %s\n时间: %s",
				st.spec.Name, st.spec.URL, st.lastError, now.Format(time.RFC3339))
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
		msg := fmt.Sprintf("[VCityChain 区块高度监控]\n节点: %s\nRPC: %s\n恢复: RPC 已恢复\n当前高度: %d\n时间: %s",
			st.spec.Name, st.spec.URL, height, now.Format(time.RFC3339))
		_ = sendFeishuAlert(client, cfg.FeishuWebhookURL, msg)
		st.errorAlertSent = false
		st.lastError = ""
	}

	if !st.initialized || height > st.lastHeight {
		if !st.initialized {
			fmt.Fprintf(os.Stderr, "init %s height=%d\n", st.spec.Name, height)
		} else if height > st.lastHeight {
			fmt.Fprintf(os.Stderr, "height up %s %d -> %d\n", st.spec.Name, st.lastHeight, height)
		}
		st.lastHeight = height
		st.lastChangeAt = now
		st.initialized = true
		if st.staleAlertSent {
			msg := fmt.Sprintf("[VCityChain 区块高度监控]\n节点: %s\nRPC: %s\n恢复: 区块高度已恢复增长\n当前高度: %d\n时间: %s",
				st.spec.Name, st.spec.URL, height, now.Format(time.RFC3339))
			_ = sendFeishuAlert(client, cfg.FeishuWebhookURL, msg)
		}
		st.staleAlertSent = false
		return
	}

	stalledFor := now.Sub(st.lastChangeAt)
	if stalledFor >= staleThreshold && !st.staleAlertSent {
		msg := fmt.Sprintf("[VCityChain 区块高度监控]\n节点: %s\nRPC: %s\n告警: 区块高度超过 %d 秒未变化\n当前高度: %d\n已停滞: %s\n时间: %s",
			st.spec.Name,
			st.spec.URL,
			cfg.StaleThresholdSeconds,
			st.lastHeight,
			stalledFor.Round(time.Second),
			now.Format(time.RFC3339))
		if sendErr := sendFeishuAlert(client, cfg.FeishuWebhookURL, msg); sendErr != nil {
			fmt.Fprintf(os.Stderr, "feishu alert failed for %s: %v\n", st.spec.Name, sendErr)
			return
		}
		st.staleAlertSent = true
		fmt.Fprintf(os.Stderr, "ALERT stale height: %s height=%d stalled=%s\n", st.spec.Name, st.lastHeight, stalledFor.Round(time.Second))
	}
}

func ethBlockNumber(client *http.Client, rpcURL string) (uint64, error) {
	body, err := json.Marshal(rpcBody{
		JSONRPC: "2.0",
		Method:  "eth_blockNumber",
		Params:  []interface{}{},
		ID:      1,
	})
	if err != nil {
		return 0, err
	}

	req, err := http.NewRequest(http.MethodPost, rpcURL, bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("http %d: %s", resp.StatusCode, string(raw))
	}

	var rr rpcResp
	if err := json.Unmarshal(raw, &rr); err != nil {
		return 0, err
	}
	if rr.Error != nil {
		return 0, fmt.Errorf("rpc error %d: %s", rr.Error.Code, rr.Error.Message)
	}

	height, err := parseHexUint64(rr.Result)
	if err != nil {
		return 0, fmt.Errorf("parse block number %q: %w", rr.Result, err)
	}
	return height, nil
}

func parseHexUint64(hexStr string) (uint64, error) {
	hexStr = strings.TrimSpace(hexStr)
	if hexStr == "" {
		return 0, errors.New("empty hex string")
	}
	if strings.HasPrefix(hexStr, "0x") || strings.HasPrefix(hexStr, "0X") {
		hexStr = hexStr[2:]
	}
	return strconv.ParseUint(hexStr, 16, 64)
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
