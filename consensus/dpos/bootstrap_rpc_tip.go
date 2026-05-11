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
	bootstrapEthBlockNumberTimeout = 5 * time.Second
	// bootstrapTipMaxLagBehindLocal：RPC 允许落后于本地的块数（同链延迟）；再大则视为错链，不参与封顶。
	bootstrapTipMaxLagBehindLocal = uint64(512)
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

// invalidateBootstrapRPCCache 清除 eth_blockNumber 短缓存，供出块前强制拉取最新规范链尖。
func (r *dposRuntime) invalidateBootstrapRPCCache() {
	r.bootstrapRPCMu.Lock()
	defer r.bootstrapRPCMu.Unlock()
	r.bootstrapRPCCached = 0
	r.bootstrapRPCCachedAt = time.Time{}
}

// preProduceBootstrapCanonicalCheck 在真正组装区块前强制刷新 RPC 链尖并打显著日志；
// 若规范链最新高度已 ≥ 拟出块高度（localTip+1），说明该高度应由同步拉取而非本地构建，返回 true 表示应中止出块。
func (r *dposRuntime) preProduceBootstrapCanonicalCheck(localTip uint64) bool {
	if r.config == nil {
		return false
	}
	url := strings.TrimSpace(r.config.BootstrapRPC)
	if url == "" {
		return false
	}
	nextHeight := localTip + 1
	r.invalidateBootstrapRPCCache()
	rpcTip := r.cachedBootstrapEthBlockNumber()

	gossipPeak := r.gossipPeakForBootstrapGate()
	lead := r.maxGossipLeadOverBootstrapRPC()
	rpcPlusLead := rpcTip + lead
	blended := gateWaterlineBlendBootstrapRPCGossip(rpcTip, gossipPeak, lead)

	r.logger.Info("🔭 【出块前 RPC 链尖】eth_blockNumber（已强制刷新短缓存）；门禁带宽 gossip vs rpc+lead",
		"dpos_bootstrap_rpc", url,
		"localTip", localTip,
		"plannedNextBlockNumber", nextHeight,
		"rpcEthBlockNumber", rpcTip,
		"gossipPeak", gossipPeak,
		"maxGossipLeadOverBootstrapRPC", lead,
		"rpcTipPlusLeadCap", rpcPlusLead,
		"blendedGateWaterline", blended)

	if rpcTip == 0 {
		r.logger.Warn("⚠️ 【出块前 RPC】eth_blockNumber 不可用（0），无法进行规范链尖复核；继续沿用后续门禁",
			"dpos_bootstrap_rpc", url)
		return false
	}
	if rpcTip >= nextHeight {
		r.logger.Warn("⏸️ 【出块前跳过出块】规范链高度已不低于拟出块高度，取消本地构建，请依赖同步拉取",
			"localTip", localTip,
			"plannedNextBlockNumber", nextHeight,
			"rpcEthBlockNumber", rpcTip,
			"gossipPeak", gossipPeak,
			"maxGossipLeadOverBootstrapRPC", lead,
			"rpcTipPlusLeadCap", rpcPlusLead,
			"blendedGateWaterline", blended)
		return true
	}
	return false
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

const bootstrapRPCGateCapLogInterval = 12 * time.Second

// bootstrapRPCGateTip 当配置了 dpos_bootstrap_rpc 且 eth_blockNumber 可读时返回 (tip, true)。
// 此时「落后门禁 / 追平锁」应以 RPC 为准，不再与 gossip 宣称取 max（避免虚高压顶）。
func (r *dposRuntime) bootstrapRPCGateTip() (tip uint64, ok bool) {
	if r.config == nil {
		return 0, false
	}
	if strings.TrimSpace(r.config.BootstrapRPC) == "" {
		return 0, false
	}
	tip = r.cachedBootstrapEthBlockNumber()
	if tip == 0 {
		return 0, false
	}
	return tip, true
}

// bootstrapRPCGateAuthoritative 见 bootstrapRPCGateTip。
func (r *dposRuntime) bootstrapRPCGateAuthoritative() bool {
	_, ok := r.bootstrapRPCGateTip()
	return ok
}

// logGateWaterlineCappedByBootstrapRPC：门禁被 bootstrap RPC 带宽封顶时打 INFO（12s 节流），使用 runtime 主 logger。
func (r *dposRuntime) logGateWaterlineCappedByBootstrapRPC(waterlineBefore, bootstrapTip, localTip, gateCap uint64) {
	const key = "bootstrap_rpc_gate_cap"
	r.logMutex.Lock()
	defer r.logMutex.Unlock()
	now := time.Now()
	if last, ok := r.lastLogTime[key]; ok && now.Sub(last) < bootstrapRPCGateCapLogInterval {
		return
	}
	r.lastLogTime[key] = now
	r.logger.Info("DPoS: gate waterline capped to bootstrap RPC band (tip + max gossip lead)",
		"waterlineBefore", waterlineBefore,
		"bootstrapCanonicalTip", bootstrapTip,
		"gateCapTipPlusLead", gateCap,
		"localTip", localTip)
}

// capGateWaterlineWithBootstrapRPC 在配置了 dpos_bootstrap_rpc 且 eth_blockNumber 可读时，将门禁水位限制在不超过 rpcTip+lead；
// lead 默认 5（见 maxGossipLeadOverBootstrapRPC）。若 RPC 高度远低于本地链尖，视为错链/错 URL，不按 RPC 收窄。
func (r *dposRuntime) capGateWaterlineWithBootstrapRPC(waterline uint64) uint64 {
	tip := r.cachedBootstrapEthBlockNumber()
	if tip == 0 {
		return waterline
	}
	lead := r.maxGossipLeadOverBootstrapRPC()
	maxBand := tip + lead

	var local uint64
	if r.config != nil && r.config.blockchain != nil {
		if h := r.config.blockchain.CurrentHeader(); h != nil {
			local = h.Number
		}
	}
	if local > 0 && tip+bootstrapTipMaxLagBehindLocal < local {
		r.logOnceWithInterval("bootstrap_rpc_skip_far_below_local", 120*time.Second, "warn",
			"DPoS: dpos_bootstrap_rpc eth_blockNumber far below local tip — not capping gate",
			"bootstrapTip", tip, "localTip", local, "waterlineBefore", waterline)
		return waterline
	}

	if waterline <= maxBand {
		return waterline
	}
	r.logGateWaterlineCappedByBootstrapRPC(waterline, tip, local, maxBand)
	return maxBand
}

// mustWaitForBootstrapCanonicalSync：配置了 dpos_bootstrap_rpc 且 eth_blockNumber 高于本地链尖时，禁止出块直至同步追上。
func (r *dposRuntime) mustWaitForBootstrapCanonicalSync(localTip uint64) bool {
	tip := r.cachedBootstrapEthBlockNumber()
	if tip == 0 || localTip >= tip {
		return false
	}
	r.logOnceWithInterval("behind_bootstrap_canonical", 5*time.Second, "info",
		"⏸️ 本地链尖落后于 dpos_bootstrap_rpc 链尖，暂不出块（先同步）",
		"localBlockNumber", localTip,
		"bootstrapCanonicalTip", tip,
		"lagBlocks", tip-localTip)
	return true
}
