package dpos

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"strings"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
)

// 主网准入：在主网有质押（投票锁仓或作为 delegate 有效质押取较大值）≥ 门槛即可。

// 写入 FaultFlags.Reason，便于与漏块故障区分；恢复时只清这类 reason。
const (
	FaultReasonMainnetStakeBelowThreshold = "mainnet stake below threshold"
	FaultReasonMainnetStakeRestored       = "mainnet stake restored"
)

// MainnetEligibilityConfig 主网准入查询配置。
type MainnetEligibilityConfig struct {
	RPCURL     string
	MinStake   *big.Int
	Timeout    time.Duration
	HTTPClient *http.Client // 可选；单测可注入
}

// MainnetEligibilityResult 一次主网资格查询结果。
type MainnetEligibilityResult struct {
	Eligible   bool
	StakeWei   *big.Int
	Reason     string // ok / below_threshold / rpc_error / invalid_config / ...
	QueryError error  // 非 nil：超时或 RPC/解析失败；复查时不得据此改故障态
}

// MainnetFaultAction 档位2：根据查询结果与当前故障态决定动作。
type MainnetFaultAction int

const (
	MainnetFaultActionNone MainnetFaultAction = iota
	MainnetFaultActionMarkFaulty
	MainnetFaultActionClearFaulty
	MainnetFaultActionSkip // RPC 失败等：本轮不改
)

// IsMainnetStakeFaultReason 是否主网质押类故障（仅此类可被「恢复」自动清除）。
func IsMainnetStakeFaultReason(reason string) bool {
	r := strings.ToLower(strings.TrimSpace(reason))
	return strings.Contains(r, "mainnet stake below threshold") ||
		strings.Contains(r, "mainnet stake")
}

// DecideMainnetFaultAction 档位2决策：明确不足→标故障；达标且现为主网故障→清；RPC失败→跳过。
func DecideMainnetFaultAction(res MainnetEligibilityResult, currentlyFaulty bool, currentFaultReason string) MainnetFaultAction {
	if res.QueryError != nil {
		return MainnetFaultActionSkip
	}
	if !res.Eligible {
		return MainnetFaultActionMarkFaulty
	}
	if currentlyFaulty && IsMainnetStakeFaultReason(currentFaultReason) {
		return MainnetFaultActionClearFaulty
	}
	return MainnetFaultActionNone
}

// QueryMainnetEligibility 查主网该地址是否有足够质押。
// 质押额 = max(投票锁仓 lockedVoteWei, 作为 delegate 的有效质押)。
func QueryMainnetEligibility(cfg MainnetEligibilityConfig, addr types.Address) MainnetEligibilityResult {
	out := MainnetEligibilityResult{StakeWei: big.NewInt(0)}

	if strings.TrimSpace(cfg.RPCURL) == "" {
		out.Reason = "invalid_config"
		out.QueryError = fmt.Errorf("mainnet eligibility rpc url is empty")
		return out
	}
	if cfg.MinStake == nil || cfg.MinStake.Sign() <= 0 {
		out.Reason = "invalid_config"
		out.QueryError = fmt.Errorf("mainnet min stake must be > 0")
		return out
	}
	if addr == types.ZeroAddress {
		out.Reason = "invalid_address"
		out.QueryError = fmt.Errorf("address is zero")
		return out
	}

	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 3 * time.Second
	}
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: timeout}
	}

	lockedWei, lockedErr := queryLockedVoteWei(client, cfg.RPCURL, addr)
	delegateWei, delegateErr := queryDelegateEffectiveWei(client, cfg.RPCURL, addr)
	if lockedErr != nil && delegateErr != nil {
		out.Reason = "rpc_error"
		out.QueryError = fmt.Errorf("locked: %v; delegate: %v", lockedErr, delegateErr)
		return out
	}

	primaryStake := big.NewInt(0)
	if lockedErr == nil && lockedWei != nil && lockedWei.Cmp(primaryStake) > 0 {
		primaryStake = lockedWei
	}
	if delegateErr == nil && delegateWei != nil && delegateWei.Cmp(primaryStake) > 0 {
		primaryStake = delegateWei
	}

	// 一边成功为 0、另一边查询失败时，不能当成「无质押」——否则 SR 权重接口超时会被误判为 below_threshold。
	if primaryStake.Sign() == 0 && (lockedErr != nil || delegateErr != nil) {
		out.Reason = "rpc_error"
		out.QueryError = fmt.Errorf("locked: %v; delegate: %v", lockedErr, delegateErr)
		return out
	}

	out.StakeWei = new(big.Int).Set(primaryStake)
	if primaryStake.Cmp(cfg.MinStake) >= 0 {
		out.Eligible = true
		out.Reason = "ok"
		return out
	}
	out.Eligible = false
	out.Reason = "below_threshold"
	return out
}

func queryLockedVoteWei(client *http.Client, rpcURL string, addr types.Address) (*big.Int, error) {
	// 与现有应用一致：对象参数 {"address":"0x..."}（不是 ["0x..."]，也不要包成 [{"address":...}]）
	raw, err := callJSONRPC(client, rpcURL, "dpos_getBalanceInfo", map[string]interface{}{
		"address": addr.String(),
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		LockedVoteWei string `json:"lockedVoteWei"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode dpos_getBalanceInfo: %w", err)
	}
	return parseWeiString(resp.LockedVoteWei)
}

func queryDelegateEffectiveWei(client *http.Client, rpcURL string, addr types.Address) (*big.Int, error) {
	// 只走指定地址查询，避免 dpos_getStakingInfo 全量扫描（主网质押地址多时很慢/易超时）。
	return queryValidatorVotingDetailsWei(client, rpcURL, addr)
}

func queryValidatorVotingDetailsWei(client *http.Client, rpcURL string, addr types.Address) (*big.Int, error) {
	raw, err := callJSONRPC(client, rpcURL, "dpos_getValidatorVotingDetails", map[string]interface{}{
		"validator": addr.String(),
	})
	if err != nil {
		return nil, err
	}
	var resp struct {
		Success   bool `json:"success"`
		Validator *struct {
			VotingPower     string `json:"votingPower"`
			TotalStakedToMe string `json:"totalStakedToMe"`
		} `json:"validator"`
		Error string `json:"error"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode dpos_getValidatorVotingDetails: %w", err)
	}
	// success=false / 无 validator：该地址无 SR 权重，按 0 处理（不是 RPC 故障）
	if !resp.Success || resp.Validator == nil {
		return big.NewInt(0), nil
	}
	best := big.NewInt(0)
	for _, s := range []string{resp.Validator.VotingPower, resp.Validator.TotalStakedToMe} {
		if s == "" {
			continue
		}
		v, err := parseWeiString(s)
		if err != nil {
			continue
		}
		if v.Cmp(best) > 0 {
			best = v
		}
	}
	return best, nil
}

func parseWeiString(s string) (*big.Int, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return big.NewInt(0), nil
	}
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		return nil, fmt.Errorf("invalid wei string %q", s)
	}
	return v, nil
}

type jsonRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	Method  string      `json:"method"`
	Params  interface{} `json:"params"`
	ID      int         `json:"id"`
}

type jsonRPCResponse struct {
	Result json.RawMessage `json:"result"`
	Error  *struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	} `json:"error"`
}

func callJSONRPC(client *http.Client, rpcURL, method string, params interface{}) (json.RawMessage, error) {
	if params == nil {
		params = []interface{}{}
	}
	payload, err := json.Marshal(jsonRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
		ID:      1,
	})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, rpcURL, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, truncateForErr(body, 200))
	}
	var rpcResp jsonRPCResponse
	if err := json.Unmarshal(body, &rpcResp); err != nil {
		return nil, fmt.Errorf("decode json-rpc: %w", err)
	}
	if rpcResp.Error != nil {
		return nil, fmt.Errorf("json-rpc error %d: %s", rpcResp.Error.Code, rpcResp.Error.Message)
	}
	if len(rpcResp.Result) == 0 || string(rpcResp.Result) == "null" {
		return nil, fmt.Errorf("json-rpc empty result for %s", method)
	}
	return rpcResp.Result, nil
}

func truncateForErr(b []byte, n int) string {
	if len(b) <= n {
		return string(b)
	}
	return string(b[:n]) + "..."
}

// mainnetEligibilityConfigFromDPoS 从 DPoS 配置组装查询参数。

// mainnetEligibilityEnabled 配置了主网 RPC 且门槛>0 即启用（注册拦截 + epoch 复查一起开）。
func (d *DPoS) mainnetEligibilityEnabled() bool {
	if d == nil || d.config == nil {
		return false
	}
	if strings.TrimSpace(d.config.MainnetEligibilityRPC) == "" {
		return false
	}
	if d.config.MainnetMinStakeWei == nil || d.config.MainnetMinStakeWei.Sign() <= 0 {
		return false
	}
	return true
}

func (d *DPoS) mainnetEligibilityConfigFromDPoS() MainnetEligibilityConfig {
	cfg := MainnetEligibilityConfig{}
	if d == nil || d.config == nil {
		return cfg
	}
	cfg.RPCURL = strings.TrimSpace(d.config.MainnetEligibilityRPC)
	cfg.MinStake = d.config.MainnetMinStakeWei
	cfg.Timeout = d.config.MainnetEligibilityTimeout
	return cfg
}

// EnforceMainnetEligibilityForRegister 档位1：注册前查主网；开关关则直接放行。
// RPC 不可用时拒绝注册（避免绕过）。请在持有 d.lock 之外调用。
func (d *DPoS) EnforceMainnetEligibilityForRegister(addr types.Address) error {
	if !d.mainnetEligibilityEnabled() {
		// 在 INFO 级别告诉你：为什么“看起来没拦截”，通常是 RPC/门槛未正确配置或未生效。
		if d.logger != nil {
			min := "0"
			if d.config != nil && d.config.MainnetMinStakeWei != nil {
				min = d.config.MainnetMinStakeWei.String()
			}
			rpc := ""
			if d.config != nil {
				rpc = strings.TrimSpace(d.config.MainnetEligibilityRPC)
			}
			d.logger.Info("主网准入未启用，跳过注册拦截",
				"address", addr.String(),
				"mainnet_eligibility_rpc_empty", rpc == "",
				"mainnet_min_stake_wei", min)
		}
		return nil
	}

	if d.logger != nil {
		d.logger.Info("主网准入校验注册",
			"address", addr.String(),
			"minStakeWei", func() string {
				if d.config != nil && d.config.MainnetMinStakeWei != nil {
					return d.config.MainnetMinStakeWei.String()
				}
				return "0"
			}())
	}

	res := QueryMainnetEligibility(d.mainnetEligibilityConfigFromDPoS(), addr)
	if res.QueryError != nil {
		if d.logger != nil {
			d.logger.Error("主网准入查询失败，拒绝注册",
				"address", addr.String(),
				"reason", res.Reason,
				"error", res.QueryError)
		}
		return fmt.Errorf("mainnet eligibility check unavailable: %w", res.QueryError)
	}

	min := "0"
	if d.config != nil && d.config.MainnetMinStakeWei != nil {
		min = d.config.MainnetMinStakeWei.String()
	}
	have := "0"
	if res.StakeWei != nil {
		have = res.StakeWei.String()
	}

	if !res.Eligible {
		if d.logger != nil {
			d.logger.Error("主网准入不通过，拒绝注册",
				"address", addr.String(),
				"haveStakeWei", have,
				"minStakeWei", min,
				"reason", res.Reason)
		}
		return fmt.Errorf("mainnet stake below threshold: have %s want >= %s", have, min)
	}

	if d.logger != nil {
		d.logger.Info("主网准入通过，允许注册",
			"address", addr.String(),
			"haveStakeWei", have,
			"minStakeWei", min)
	}
	return nil
}

// MergeFaultFlags 合并漏块故障与主网准入故障；同一地址以 IsFaulty=true 优先。
func MergeFaultFlags(base, extra []FaultFlagInfo) []FaultFlagInfo {
	if len(extra) == 0 {
		return base
	}
	if len(base) == 0 {
		return append([]FaultFlagInfo(nil), extra...)
	}
	byAddr := make(map[types.Address]FaultFlagInfo, len(base)+len(extra))
	order := make([]types.Address, 0, len(base)+len(extra))
	add := func(f FaultFlagInfo) {
		existing, ok := byAddr[f.NodeAddress]
		if !ok {
			byAddr[f.NodeAddress] = f
			order = append(order, f.NodeAddress)
			return
		}
		if f.IsFaulty {
			byAddr[f.NodeAddress] = f
			return
		}
		if !existing.IsFaulty {
			byAddr[f.NodeAddress] = f
		}
	}
	for _, f := range base {
		add(f)
	}
	for _, f := range extra {
		add(f)
	}
	out := make([]FaultFlagInfo, 0, len(order))
	for _, addr := range order {
		out = append(out, byAddr[addr])
	}
	return out
}

// detectMainnetEligibilityFaults 档位2：epoch 结束时复查主网质押，产出 FaultFlags。
// QueryError → 跳过该地址；不足 → IsFaulty=true；达标且现为命网故障 → IsFaulty=false。
func (d *DPoS) detectMainnetEligibilityFaults(blockNumber uint64) []FaultFlagInfo {
	if !d.mainnetEligibilityEnabled() {
		return nil
	}
	if strings.TrimSpace(d.config.MainnetEligibilityRPC) == "" ||
		d.config.MainnetMinStakeWei == nil || d.config.MainnetMinStakeWei.Sign() <= 0 {
		if d.logger != nil {
			d.logger.Warn("⚠️ 主网准入复查已开启但 RPC/门槛未配置，跳过")
		}
		return nil
	}

	addrs := d.collectMainnetEligibilityRecheckAddresses()
	if len(addrs) == 0 {
		return nil
	}

	epochNumber := uint64(0)
	if meta := d.getEpochForBlock(blockNumber - 1); meta != nil {
		epochNumber = meta.Number
	}
	now := uint64(time.Now().Unix())
	cfg := d.mainnetEligibilityConfigFromDPoS()

	var out []FaultFlagInfo
	for _, addr := range addrs {
		res := QueryMainnetEligibility(cfg, addr)
		faultInfo := d.getValidatorFaultInfo(addr)
		currentlyFaulty := false
		currentReason := ""
		if faultInfo != nil {
			if v, ok := faultInfo["isFaulty"].(bool); ok {
				currentlyFaulty = v
			}
			if r, ok := faultInfo["reason"].(string); ok {
				currentReason = r
			}
		}
		action := DecideMainnetFaultAction(res, currentlyFaulty, currentReason)
		switch action {
		case MainnetFaultActionMarkFaulty:
			out = append(out, FaultFlagInfo{
				NodeAddress:     addr,
				IsFaulty:        true,
				LastUpdateTime:  now,
				EpochNumber:     epochNumber,
				LastFaultyEpoch: epochNumber,
				Reason:          FaultReasonMainnetStakeBelowThreshold,
			})
			if d.logger != nil {
				d.logger.Info("🚫 [主网准入复查] 标故障",
					"address", addr.String(),
					"stakeWei", func() string {
						if res.StakeWei == nil {
							return "0"
						}
						return res.StakeWei.String()
					}(),
					"epoch", epochNumber)
			}
		case MainnetFaultActionClearFaulty:
			out = append(out, FaultFlagInfo{
				NodeAddress:    addr,
				IsFaulty:       false,
				LastUpdateTime: now,
				EpochNumber:    epochNumber,
				Reason:         FaultReasonMainnetStakeRestored,
			})
			if d.logger != nil {
				d.logger.Info("✅ [主网准入复查] 主网质押已恢复，清除故障",
					"address", addr.String(),
					"epoch", epochNumber)
			}
		case MainnetFaultActionSkip:
			if d.logger != nil {
				d.logger.Warn("⚠️ [主网准入复查] RPC 失败，本轮跳过",
					"address", addr.String(),
					"error", res.QueryError)
			}
		}
	}
	return out
}

func (d *DPoS) collectMainnetEligibilityRecheckAddresses() []types.Address {
	seen := make(map[types.Address]struct{})
	var out []types.Address
	add := func(addr types.Address) {
		if addr == types.ZeroAddress {
			return
		}
		if _, ok := seen[addr]; ok {
			return
		}
		seen[addr] = struct{}{}
		out = append(out, addr)
	}

	if set, err := d.GetSortedValidatorsWithLimit(); err == nil {
		for _, v := range set {
			if v != nil {
				add(v.Address)
			}
		}
	}
	if regs, err := d.GetDelegateRegistrations(); err == nil {
		for _, r := range regs {
			if r != nil {
				add(r.Address)
			}
		}
	}
	return out
}


// applyMainnetStakeFaultClear 仅当当前 DB 故障为「主网质押类」时清除；返回是否已清除。
func (d *DPoS) applyMainnetStakeFaultClear(flag FaultFlagInfo) bool {
	if d == nil || flag.IsFaulty {
		return false
	}
	if flag.Reason != FaultReasonMainnetStakeRestored && !IsMainnetStakeFaultReason(flag.Reason) {
		return false
	}
	info := d.getValidatorFaultInfo(flag.NodeAddress)
	if info == nil {
		return false
	}
	isFaulty, _ := info["isFaulty"].(bool)
	reason, _ := info["reason"].(string)
	if !isFaulty || !IsMainnetStakeFaultReason(reason) {
		return false
	}
	if err := d.saveFaultStatusToDatabase(flag); err != nil {
		if d.logger != nil {
			d.logger.Warn("clear mainnet fault write failed", "address", flag.NodeAddress.String(), "error", err)
		}
		return false
	}
	d.updateMemoryFaultStatus(flag)
	return true
}
