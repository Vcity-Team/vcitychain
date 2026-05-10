package dpos

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
	bootstrapEthBlockNumberCacheTTL = 5 * time.Second
	bootstrapEthBlockNumberTimeout  = 5 * time.Second
)

// cachedBootstrapEthBlockNumber returns eth_blockNumber from dpos_bootstrap_rpc with short TTL; 0 if unset or on error.
func (r *dposRuntime) cachedBootstrapEthBlockNumber() uint64 {
	if r.config == nil {
		return 0
	}
	url := strings.TrimSpace(r.config.BootstrapRPC)
	if url == "" {
		return 0
	}
	r.bootstrapRPCMu.Lock()
	defer r.bootstrapRPCMu.Unlock()
	if r.bootstrapRPCCached > 0 && time.Since(r.bootstrapRPCCachedAt) < bootstrapEthBlockNumberCacheTTL {
		return r.bootstrapRPCCached
	}
	ctx, cancel := context.WithTimeout(context.Background(), bootstrapEthBlockNumberTimeout)
	defer cancel()
	n, err := fetchEthBlockNumber(ctx, url)
	if err != nil {
		r.logger.Debug("dpos_bootstrap_rpc eth_blockNumber failed", "url", url, "err", err)
		return 0
	}
	r.bootstrapRPCCached = n
	r.bootstrapRPCCachedAt = time.Now()
	return n
}

func fetchEthBlockNumber(ctx context.Context, endpoint string) (uint64, error) {
	payload := []byte(`{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}`)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return 0, err
	}
	req.Header.Set("Content-Type", "application/json")
	client := &http.Client{Timeout: bootstrapEthBlockNumberTimeout}
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
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

// capGateWaterlineWithBootstrapRPC limits gate waterline to canonical tip from dpos_bootstrap_rpc when configured and RPC succeeds.
func (r *dposRuntime) capGateWaterlineWithBootstrapRPC(waterline uint64) uint64 {
	tip := r.cachedBootstrapEthBlockNumber()
	if tip == 0 || waterline <= tip {
		return waterline
	}
	r.logOnceWithInterval("bootstrap_rpc_gate_cap", 12*time.Second, "info",
		"DPoS: gate waterline capped by dpos_bootstrap_rpc eth_blockNumber",
		"waterlineBefore", waterline,
		"bootstrapCanonicalTip", tip)
	return tip
}
