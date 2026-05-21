package syncer

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	heightSourceJSONRPC         = "json_rpc"
	bootJSONRPCTimeout          = 5 * time.Second
	bootJSONRPCResponseMaxBytes = 1 << 20
)

// normalizeJSONRPCEndpoint turns config values like "0.0.0.0:8545" into "http://127.0.0.1:8545".
func normalizeJSONRPCEndpoint(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
		return s
	}
	if strings.HasPrefix(s, "0.0.0.0:") {
		return "http://127.0.0.1" + strings.TrimPrefix(s, "0.0.0.0")
	}
	if strings.Contains(s, "://") {
		return s
	}
	return "http://" + s
}

func fetchEthBlockNumber(ctx context.Context, endpoint string) (uint64, error) {
	url := normalizeJSONRPCEndpoint(endpoint)
	if url == "" {
		return 0, fmt.Errorf("empty json-rpc endpoint")
	}
	payload := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: bootJSONRPCTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, bootJSONRPCResponseMaxBytes))
	if err != nil {
		return 0, err
	}
	var out struct {
		Result string `json:"result"`
		Error  *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(b, &out); err != nil {
		return 0, err
	}
	if out.Error != nil {
		return 0, fmt.Errorf("%s", out.Error.Message)
	}
	s := strings.TrimSpace(out.Result)
	if !strings.HasPrefix(s, "0x") {
		return 0, fmt.Errorf("unexpected eth_blockNumber result: %q", s)
	}
	return strconv.ParseUint(strings.TrimPrefix(s, "0x"), 16, 64)
}
