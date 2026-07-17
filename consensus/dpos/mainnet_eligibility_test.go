package dpos

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/require"
)

func TestIsMainnetStakeFaultReason(t *testing.T) {
	require.True(t, IsMainnetStakeFaultReason(FaultReasonMainnetStakeBelowThreshold))
	require.True(t, IsMainnetStakeFaultReason("Mainnet Stake Below Threshold"))
	require.False(t, IsMainnetStakeFaultReason("missed blocks over threshold"))
	require.False(t, IsMainnetStakeFaultReason(""))
}

func TestDecideMainnetFaultAction(t *testing.T) {
	t.Run("rpc_error_skip", func(t *testing.T) {
		res := MainnetEligibilityResult{QueryError: errForTest("rpc unavailable"), Reason: "rpc_error"}
		require.Equal(t, MainnetFaultActionSkip, DecideMainnetFaultAction(res, false, ""))
		require.Equal(t, MainnetFaultActionSkip, DecideMainnetFaultAction(res, true, FaultReasonMainnetStakeBelowThreshold))
	})

	t.Run("below_threshold_mark", func(t *testing.T) {
		res := MainnetEligibilityResult{Eligible: false, Reason: "below_threshold", StakeWei: big.NewInt(0)}
		require.Equal(t, MainnetFaultActionMarkFaulty, DecideMainnetFaultAction(res, false, ""))
	})

	t.Run("eligible_clear_mainnet_fault", func(t *testing.T) {
		res := MainnetEligibilityResult{Eligible: true, Reason: "ok", StakeWei: big.NewInt(100)}
		require.Equal(t, MainnetFaultActionClearFaulty,
			DecideMainnetFaultAction(res, true, FaultReasonMainnetStakeBelowThreshold))
	})

	t.Run("eligible_does_not_clear_miss_block_fault", func(t *testing.T) {
		res := MainnetEligibilityResult{Eligible: true, Reason: "ok", StakeWei: big.NewInt(100)}
		require.Equal(t, MainnetFaultActionNone,
			DecideMainnetFaultAction(res, true, "Epoch 1: missed blocks"))
	})

	t.Run("eligible_healthy_noop", func(t *testing.T) {
		res := MainnetEligibilityResult{Eligible: true, Reason: "ok", StakeWei: big.NewInt(100)}
		require.Equal(t, MainnetFaultActionNone, DecideMainnetFaultAction(res, false, ""))
	})
}

type errForTest string

func (e errForTest) Error() string { return string(e) }

func TestQueryMainnetEligibility_HasStake_OK(t *testing.T) {
	addr := types.StringToAddress("0x70090D648966b11a501964A1fC0b045D9aEa5800")
	minStake := mustBig("1000000000000000000000000")
	stake := mustBig("2000000000000000000000000")

	srv := newFakeMainnetRPC(t, fakeMainnetHandlers{balanceLocked: stake.String()})
	defer srv.Close()

	res := QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL:   srv.URL,
		MinStake: minStake,
		Timeout:  time.Second,
	}, addr)

	require.NoError(t, res.QueryError)
	require.True(t, res.Eligible)
	require.Equal(t, "ok", res.Reason)
	require.Equal(t, 0, stake.Cmp(res.StakeWei))
}

func TestQueryMainnetEligibility_UsesMaxOfLockedAndDelegate(t *testing.T) {
	addr := types.StringToAddress("0x70090D648966b11a501964A1fC0b045D9aEa5800")
	minStake := mustBig("100")

	srv := newFakeMainnetRPC(t, fakeMainnetHandlers{
		balanceLocked: "50",
		votingDetails: "150",
	})
	defer srv.Close()

	res := QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL:   srv.URL,
		MinStake: minStake,
		Timeout:  time.Second,
	}, addr)

	require.NoError(t, res.QueryError)
	require.True(t, res.Eligible)
	require.Equal(t, 0, mustBig("150").Cmp(res.StakeWei))
}

func TestQueryMainnetEligibility_SRViaVotingPower(t *testing.T) {
	addr := types.StringToAddress("0x1b05c37cF8596F4Caa840b129C946bC656B6fa7E")
	minStake := mustBig("1000000000000000000000")
	vp := mustBig("21557000000000000000000")

	srv := newFakeMainnetRPC(t, fakeMainnetHandlers{
		balanceLocked: "0",
		votingDetails: vp.String(),
	})
	defer srv.Close()

	res := QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL:   srv.URL,
		MinStake: minStake,
		Timeout:  time.Second,
	}, addr)

	require.NoError(t, res.QueryError)
	require.True(t, res.Eligible)
	require.Equal(t, 0, vp.Cmp(res.StakeWei))
}

func TestQueryMainnetEligibility_DelegateQueryErrorNotTreatedAsZero(t *testing.T) {
	addr := types.StringToAddress("0x1b05c37cF8596F4Caa840b129C946bC656B6fa7E")
	srv := newFakeMainnetRPC(t, fakeMainnetHandlers{
		balanceLocked:   "0",
		forceDelegateErr: true,
	})
	defer srv.Close()

	res := QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL:   srv.URL,
		MinStake: mustBig("1"),
		Timeout:  time.Second,
	}, addr)

	require.Error(t, res.QueryError)
	require.Equal(t, "rpc_error", res.Reason)
	require.False(t, res.Eligible)
}

func TestQueryMainnetEligibility_NoStake_BelowThreshold(t *testing.T) {
	addr := types.StringToAddress("0x70090D648966b11a501964A1fC0b045D9aEa5800")
	minStake := mustBig("1000000000000000000000000")

	srv := newFakeMainnetRPC(t, fakeMainnetHandlers{balanceLocked: "0"})
	defer srv.Close()

	res := QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL:   srv.URL,
		MinStake: minStake,
		Timeout:  time.Second,
	}, addr)

	require.NoError(t, res.QueryError)
	require.False(t, res.Eligible)
	require.Equal(t, "below_threshold", res.Reason)
}

func TestQueryMainnetEligibility_HTTPError(t *testing.T) {
	addr := types.StringToAddress("0x70090D648966b11a501964A1fC0b045D9aEa5800")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte("boom"))
	}))
	defer srv.Close()

	res := QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL:   srv.URL,
		MinStake: big.NewInt(1),
		Timeout:  time.Second,
	}, addr)

	require.Error(t, res.QueryError)
	require.False(t, res.Eligible)
	require.Equal(t, "rpc_error", res.Reason)
	require.Equal(t, MainnetFaultActionSkip, DecideMainnetFaultAction(res, false, ""))
}

func TestQueryMainnetEligibility_Timeout(t *testing.T) {
	addr := types.StringToAddress("0x70090D648966b11a501964A1fC0b045D9aEa5800")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		time.Sleep(200 * time.Millisecond)
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  map[string]string{"lockedVoteWei": "1"},
		})
	}))
	defer srv.Close()

	res := QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL: srv.URL,
		MinStake: big.NewInt(1),
		Timeout:  50 * time.Millisecond,
		HTTPClient: &http.Client{
			Timeout: 50 * time.Millisecond,
		},
	}, addr)

	require.Error(t, res.QueryError)
	require.Equal(t, "rpc_error", res.Reason)
	require.Equal(t, MainnetFaultActionSkip, DecideMainnetFaultAction(res, true, FaultReasonMainnetStakeBelowThreshold))
}

func TestQueryMainnetEligibility_InvalidConfig(t *testing.T) {
	addr := types.StringToAddress("0x70090D648966b11a501964A1fC0b045D9aEa5800")

	res := QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL:   "",
		MinStake: big.NewInt(1),
	}, addr)
	require.Error(t, res.QueryError)
	require.Equal(t, "invalid_config", res.Reason)

	res = QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL:   "http://127.0.0.1:1",
		MinStake: big.NewInt(0),
	}, addr)
	require.Error(t, res.QueryError)
	require.Equal(t, "invalid_config", res.Reason)

	res = QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL:   "http://127.0.0.1:1",
		MinStake: big.NewInt(1),
	}, types.ZeroAddress)
	require.Error(t, res.QueryError)
	require.Equal(t, "invalid_address", res.Reason)
}

func TestQueryMainnetEligibility_JSONRPCError(t *testing.T) {
	addr := types.StringToAddress("0x70090D648966b11a501964A1fC0b045D9aEa5800")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"error":   map[string]interface{}{"code": -32000, "message": "method not found"},
		})
	}))
	defer srv.Close()

	res := QueryMainnetEligibility(MainnetEligibilityConfig{
		RPCURL:   srv.URL,
		MinStake: big.NewInt(1),
		Timeout:  time.Second,
	}, addr)
	require.Error(t, res.QueryError)
	require.True(t, strings.Contains(res.QueryError.Error(), "method not found"))
}

func TestMergeFaultFlags_PreferFaulty(t *testing.T) {
	a := types.StringToAddress("0x70090D648966b11a501964A1fC0b045D9aEa5800")
	b := types.StringToAddress("0x483464b5418a270449CA4A666c0B1B02A63d434a")
	base := []FaultFlagInfo{
		{NodeAddress: a, IsFaulty: true, Reason: "missed blocks"},
	}
	extra := []FaultFlagInfo{
		{NodeAddress: a, IsFaulty: false, Reason: FaultReasonMainnetStakeRestored},
		{NodeAddress: b, IsFaulty: true, Reason: FaultReasonMainnetStakeBelowThreshold},
	}
	merged := MergeFaultFlags(base, extra)
	require.Len(t, merged, 2)

	byAddr := map[types.Address]FaultFlagInfo{}
	for _, f := range merged {
		byAddr[f.NodeAddress] = f
	}
	require.True(t, byAddr[a].IsFaulty)
	require.Equal(t, "missed blocks", byAddr[a].Reason)
	require.True(t, byAddr[b].IsFaulty)
}

func TestMergeFaultFlags_ClearWhenNoStronger(t *testing.T) {
	a := types.StringToAddress("0x70090D648966b11a501964A1fC0b045D9aEa5800")
	base := []FaultFlagInfo{
		{NodeAddress: a, IsFaulty: false, Reason: FaultReasonMainnetStakeRestored},
	}
	merged := MergeFaultFlags(base, nil)
	require.Len(t, merged, 1)
	require.False(t, merged[0].IsFaulty)
}

type fakeMainnetHandlers struct {
	balanceLocked    string
	votingDetails    string // 非空则 dpos_getValidatorVotingDetails 返回该 votingPower
	forceDelegateErr bool
}

func newFakeMainnetRPC(t *testing.T, h fakeMainnetHandlers) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Method string `json:"method"`
		}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&req))

		var result interface{}
		switch req.Method {
		case "dpos_getBalanceInfo":
			result = map[string]string{
				"lockedVoteWei": h.balanceLocked,
				"balanceWei":    "0",
				"spendableWei":  "0",
			}
		case "dpos_getValidatorVotingDetails":
			if h.forceDelegateErr {
				w.WriteHeader(http.StatusInternalServerError)
				_, _ = w.Write([]byte("boom"))
				return
			}
			if h.votingDetails == "" {
				result = map[string]interface{}{
					"success": false,
					"error":   "validator not found",
				}
			} else {
				result = map[string]interface{}{
					"success": true,
					"validator": map[string]interface{}{
						"votingPower":     h.votingDetails,
						"totalStakedToMe": h.votingDetails,
						"isActive":        true,
					},
				}
			}
		default:
			w.WriteHeader(http.StatusBadRequest)
			_ = json.NewEncoder(w).Encode(map[string]interface{}{
				"jsonrpc": "2.0",
				"id":      1,
				"error":   map[string]interface{}{"code": -32601, "message": "unknown method " + req.Method},
			})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]interface{}{
			"jsonrpc": "2.0",
			"id":      1,
			"result":  result,
		})
	}))
}

func mustBig(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad big int: " + s)
	}
	return v
}
