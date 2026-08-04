package jsonrpc

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"strings"
	"testing"

	"github.com/Vcity-Team/vcitychain/consensus/dpos"
	dposCore "github.com/Vcity-Team/vcitychain/consensus/dpos/core"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// createTxTestStore is a minimal dposStore for Create*Transaction unit tests.
type createTxTestStore struct {
	nonce    uint64
	baseFee  uint64
	balances map[types.Address]*big.Int
	header   *types.Header
	engine   interface{}
}

func newCreateTxTestStore(engine interface{}) *createTxTestStore {
	root := types.StringToHash("0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	return &createTxTestStore{
		nonce:   7,
		baseFee: 1_000_000_000,
		balances: map[types.Address]*big.Int{
			types.StringToAddress("0x1111111111111111111111111111111111111111"): mustBig("10000000000000000000000"),
			types.StringToAddress("0x2222222222222222222222222222222222222222"): mustBig("10000000000000000000000"),
			types.StringToAddress("0x3333333333333333333333333333333333333333"): mustBig("10000000000000000000000"),
		},
		header: &types.Header{Number: 100, StateRoot: root},
		engine: engine,
	}
}

func mustBig(s string) *big.Int {
	v, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad big int: " + s)
	}
	return v
}

func (s *createTxTestStore) GetAccount(root types.Hash, addr types.Address) (*Account, error) {
	bal := s.balances[addr]
	if bal == nil {
		bal = big.NewInt(0)
	}
	return &Account{Balance: bal, Nonce: s.nonce}, nil
}
func (s *createTxTestStore) GetBalance(root types.Hash, addr types.Address) (*big.Int, error) {
	if bal, ok := s.balances[addr]; ok {
		return new(big.Int).Set(bal), nil
	}
	return big.NewInt(0), nil
}
func (s *createTxTestStore) GetDPoSState() (*dpos.State, error) { return nil, nil }
func (s *createTxTestStore) GetValidators() (validator.AccountSet, error) {
	return nil, nil
}
func (s *createTxTestStore) GetValidatorsWithFilter(bool) (validator.AccountSet, error) {
	return nil, nil
}
func (s *createTxTestStore) GetStakingInfo() ([]*dpos.StakeInfo, error) { return nil, nil }
func (s *createTxTestStore) GetPendingTx(types.Hash) (*types.Transaction, bool) {
	return nil, false
}
func (s *createTxTestStore) GetNetwork() interface{}  { return nil }
func (s *createTxTestStore) GetServer() interface{}   { return nil }
func (s *createTxTestStore) GetTxPool() interface{}   { return nil }
func (s *createTxTestStore) ReadTxLookup(types.Hash) (types.Hash, bool) {
	return types.Hash{}, false
}
func (s *createTxTestStore) GetBlockByHash(types.Hash, bool) (*types.Block, bool) {
	return nil, false
}
func (s *createTxTestStore) GetHeaderByNumber(uint64) (*types.Header, bool) {
	return s.header, s.header != nil
}
func (s *createTxTestStore) GetNonce(types.Address) uint64 { return s.nonce }
func (s *createTxTestStore) GetBaseFee() uint64            { return s.baseFee }
func (s *createTxTestStore) Header() *types.Header         { return s.header }
func (s *createTxTestStore) GetDPoSEngine() interface{}    { return s.engine }

// fakeVoteEngine satisfies vote prechecks without *dpos.DPoS (skips ValidateVoteOnly).
type fakeVoteEngine struct{}

func (f *fakeVoteEngine) IsDelegateRegistered(types.Address) bool { return true }
func (f *fakeVoteEngine) IsDelegateCandidate(types.Address) bool  { return true }
func (f *fakeVoteEngine) IsGenesisValidator(types.Address) bool   { return false }

// fakeGovEngine satisfies governanceEngine for create-parameter-proposal.
type fakeGovEngine struct{}

func (f *fakeGovEngine) GetParameterProposal(string) (*dpos.ParameterProposal, error) {
	return nil, nil
}
func (f *fakeGovEngine) GetActiveProposals() ([]*dpos.ParameterProposal, error) { return nil, nil }
func (f *fakeGovEngine) GetVotableCurrentParameters() map[string]*dpos.ParameterInfo {
	return nil
}
func (f *fakeGovEngine) GetCurrentProposalPeriod() map[string]interface{} { return nil }
func (f *fakeGovEngine) IsParameterVotable(parameter string) bool {
	return parameter == "min_freeze_period"
}
func (f *fakeGovEngine) CheckRecoveryPrerequisites(types.Address) error { return nil }
func (f *fakeGovEngine) SignProposalForTx(*dpos.ParameterProposal, string) ([]byte, error) {
	return nil, nil
}
func (f *fakeGovEngine) SignRecoveryProposalForTx(*dpos.ParameterProposal, string) ([]byte, error) {
	return nil, nil
}
func (f *fakeGovEngine) SignVoteForTx(*dpos.ParameterVote, string) ([]byte, error) {
	return nil, nil
}
func (f *fakeGovEngine) GetCurrentBlockNumber() uint64 { return 1 }
func (f *fakeGovEngine) GetSuperRepresentatives() (validator.AccountSet, error) {
	return nil, nil
}

func newCreateTxEndpoint(t *testing.T, engine interface{}) *DPOS {
	t.Helper()
	return NewDPOS(hclog.NewNullLogger(), newCreateTxTestStore(engine), 1337)
}

func assertUnsignedDraft(t *testing.T, raw interface{}, wantFrom string, dataPrefix string) *UnsignedTxDraft {
	t.Helper()
	draft, ok := raw.(*UnsignedTxDraft)
	if !ok {
		// CreateVoteTransaction returns CreateVoteTransactionResponse
		if voteDraft, ok := raw.(*CreateVoteTransactionResponse); ok {
			if !voteDraft.Success {
				t.Fatalf("vote draft failed: %s", voteDraft.Error)
			}
			if voteDraft.ChainID == "" || voteDraft.From == "" || voteDraft.Nonce == "" ||
				voteDraft.GasPrice == "" || voteDraft.Gas == "" || voteDraft.Data == "" {
				t.Fatalf("missing required draft fields: %+v", voteDraft)
			}
			if !strings.HasPrefix(strings.ToLower(voteDraft.ChainID), "0x") {
				t.Fatalf("chainId should be hex: %s", voteDraft.ChainID)
			}
			if !strings.EqualFold(voteDraft.From, wantFrom) {
				t.Fatalf("from: got %s want %s", voteDraft.From, wantFrom)
			}
			if dataPrefix != "" && !strings.HasPrefix(strings.ToLower(voteDraft.Data), strings.ToLower(dataPrefix)) {
				t.Fatalf("data prefix: got %s want prefix %s", voteDraft.Data, dataPrefix)
			}
			if voteDraft.Type != 0 {
				t.Fatalf("type want 0 got %d", voteDraft.Type)
			}
			// adapt for callers that only care about success shape
			return &UnsignedTxDraft{
				Success:  true,
				ChainID:  voteDraft.ChainID,
				From:     voteDraft.From,
				Nonce:    voteDraft.Nonce,
				GasPrice: voteDraft.GasPrice,
				Gas:      voteDraft.Gas,
				GasLimit: voteDraft.GasLimit,
				To:       voteDraft.To,
				Value:    voteDraft.Value,
				Data:     voteDraft.Data,
				Type:     voteDraft.Type,
			}
		}
		t.Fatalf("unexpected response type %T", raw)
	}
	if !draft.Success {
		t.Fatalf("draft failed: %s", draft.Error)
	}
	if draft.ChainID == "" || draft.From == "" || draft.Nonce == "" ||
		draft.GasPrice == "" || draft.Gas == "" || draft.GasLimit == "" || draft.Data == "" {
		t.Fatalf("missing required draft fields: %+v", draft)
	}
	if !strings.HasPrefix(strings.ToLower(draft.ChainID), "0x") {
		t.Fatalf("chainId should be hex: %s", draft.ChainID)
	}
	if !strings.EqualFold(draft.From, wantFrom) {
		t.Fatalf("from: got %s want %s", draft.From, wantFrom)
	}
	if dataPrefix != "" && !strings.HasPrefix(strings.ToLower(draft.Data), strings.ToLower(dataPrefix)) {
		t.Fatalf("data prefix: got %s want prefix %s", draft.Data, dataPrefix)
	}
	if draft.Type != 0 {
		t.Fatalf("type want 0 got %d", draft.Type)
	}
	return draft
}

func TestCreateVoteTransaction_ReturnsUnsignedDraft(t *testing.T) {
	d := newCreateTxEndpoint(t, &fakeVoteEngine{})
	voter := "0x1111111111111111111111111111111111111111"
	candidate := "0x2222222222222222222222222222222222222222"
	amount := "1000000000000000000" // 1 token min

	raw, err := d.CreateVoteTransaction(context.Background(), []interface{}{voter, candidate, amount})
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	draft := assertUnsignedDraft(t, raw, voter, "0x44504f53") // "DPOS"
	if draft.To != nil {
		t.Fatalf("vote to should be null, got %v", draft.To)
	}
	if draft.Value != "0x0" {
		t.Fatalf("vote value want 0x0 got %s", draft.Value)
	}
}

func TestCreateVoteTransaction_RejectsMissingFields(t *testing.T) {
	d := newCreateTxEndpoint(t, &fakeVoteEngine{})
	raw, err := d.CreateVoteTransaction(context.Background(), []interface{}{"0x1111111111111111111111111111111111111111"})
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	resp := raw.(*CreateVoteTransactionResponse)
	if resp.Success {
		t.Fatal("expected failure")
	}
}

func TestCreateUpdateCommissionTransaction_ReturnsUnsignedDraft(t *testing.T) {
	d := newCreateTxEndpoint(t, nil)
	validatorAddr := "0x1111111111111111111111111111111111111111"

	raw, err := d.CreateUpdateCommissionTransaction(context.Background(), []interface{}{validatorAddr, float64(1000)})
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	draft := assertUnsignedDraft(t, raw, validatorAddr, "0x44504f53434f4d") // DPOSCOM
	if draft.To != nil {
		t.Fatalf("commission to should be null")
	}
}

func TestCreateUpdateCommissionTransaction_RejectsOutOfRange(t *testing.T) {
	d := newCreateTxEndpoint(t, nil)
	raw, err := d.CreateUpdateCommissionTransaction(context.Background(), map[string]interface{}{
		"validator":      "0x1111111111111111111111111111111111111111",
		"commissionRate": float64(100),
	})
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	draft := raw.(*UnsignedTxDraft)
	if draft.Success {
		t.Fatal("expected failure for out-of-range rate")
	}
}

func TestCreateRegisterDelegateTransaction_ReturnsUnsignedDraft(t *testing.T) {
	engine := newMinimalDPoSEngine(t)
	d := newCreateTxEndpoint(t, engine)
	registrant := "0x1111111111111111111111111111111111111111"

	raw, err := d.CreateRegisterDelegateTransaction(context.Background(), map[string]interface{}{
		"registrant":  registrant,
		"name":        "test-node",
		"website":     "https://example.com",
		"description": "unit test",
	})
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	draft := assertUnsignedDraft(t, raw, registrant, "0x44504f53524547") // DPOSREG
	if draft.To == nil {
		t.Fatal("register to should be escrow address")
	}
	escrow := dpos.DelegateDepositEscrowAddr().String()
	if !strings.EqualFold(*draft.To, escrow) {
		t.Fatalf("to: got %s want %s", *draft.To, escrow)
	}
	if draft.DepositWei == "" || draft.DepositWei == "0" {
		t.Fatalf("expected non-zero depositWei, got %s", draft.DepositWei)
	}
	if draft.Value == "0x0" {
		t.Fatal("register value should equal deposit")
	}
}

func newMinimalDPoSEngine(t *testing.T) *dpos.DPoS {
	t.Helper()
	eng, err := dpos.NewDPoS(&dposCore.DPoSOptions{
		Logger: hclog.NewNullLogger(),
		Config: &dpos.DPoSConfig{},
	})
	if err != nil {
		t.Fatalf("NewDPoS: %v", err)
	}
	return eng
}

func TestCreateWithdrawDelegateTransaction_ReturnsUnsignedDraft(t *testing.T) {
	d := newCreateTxEndpoint(t, &fakeVoteEngine{})
	addr := "0x1111111111111111111111111111111111111111"

	raw, err := d.CreateWithdrawDelegateTransaction(context.Background(), map[string]interface{}{
		"address": addr,
	})
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	draft := assertUnsignedDraft(t, raw, addr, "0x44504f5343414e") // DPOSCAN
	if draft.To == nil {
		t.Fatal("withdraw to should be escrow")
	}
	if !strings.EqualFold(*draft.To, dpos.DelegateDepositEscrowAddr().String()) {
		t.Fatalf("to mismatch: %s", *draft.To)
	}
	if draft.Value != "0x0" {
		t.Fatalf("withdraw value want 0x0 got %s", draft.Value)
	}
}

func TestCreateParameterProposalTransaction_ReturnsDomainHashAndDraft(t *testing.T) {
	d := newCreateTxEndpoint(t, &fakeGovEngine{})
	proposer := "0x1111111111111111111111111111111111111111"

	raw, err := d.CreateParameterProposalTransaction(context.Background(), map[string]interface{}{
		"parameter":   "min_freeze_period",
		"newValue":    float64(86400),
		"description": "test",
		"proposer":    proposer,
	})
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	draft := assertUnsignedDraft(t, raw, proposer, "0x7b") // JSON '{'
	if draft.DomainHash == "" || len(draft.DomainHash) != 66 { // 0x + 64 hex
		t.Fatalf("domainHash want 32 bytes hex, got %s", draft.DomainHash)
	}
	if draft.CreatedAt == 0 {
		t.Fatal("createdAt required")
	}
	if draft.To == nil || !strings.EqualFold(*draft.To, proposer) {
		t.Fatalf("proposal to should be self-call, got %v", draft.To)
	}
	// data should be JSON without requiring private key fields from client request
	dataBytes, err := hex.DecodeString(strings.TrimPrefix(draft.Data, "0x"))
	if err != nil {
		t.Fatalf("decode data: %v", err)
	}
	var payload map[string]interface{}
	if err := json.Unmarshal(dataBytes, &payload); err != nil {
		t.Fatalf("data not json: %v", err)
	}
	if payload["proposalType"] != "parameter" {
		t.Fatalf("proposalType: %v", payload["proposalType"])
	}
}

func TestCreateRecoveryProposalTransaction_ReturnsDomainHashAndDraft(t *testing.T) {
	d := newCreateTxEndpoint(t, nil)
	proposer := "0x1111111111111111111111111111111111111111"
	validatorAddr := "0x2222222222222222222222222222222222222222"

	raw, err := d.CreateRecoveryProposalTransaction(context.Background(), map[string]interface{}{
		"proposer":         proposer,
		"validatorAddress": validatorAddr,
		"recoveryReason":   "test recovery",
		"description":      "desc",
	})
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	draft := assertUnsignedDraft(t, raw, proposer, "0x7b")
	if draft.DomainHash == "" {
		t.Fatal("domainHash required")
	}
	dataBytes, _ := hex.DecodeString(strings.TrimPrefix(draft.Data, "0x"))
	var payload map[string]interface{}
	if err := json.Unmarshal(dataBytes, &payload); err != nil {
		t.Fatalf("data not json: %v", err)
	}
	if payload["proposalType"] != "validator_recovery" {
		t.Fatalf("proposalType: %v", payload["proposalType"])
	}
	if payload["parameter"] != validatorAddr {
		t.Fatalf("parameter should be validator address, got %v", payload["parameter"])
	}
}

func TestCreateVoteOnParameterProposalTransaction_ReturnsDomainHashAndDraft(t *testing.T) {
	d := newCreateTxEndpoint(t, nil)
	voter := "0x1111111111111111111111111111111111111111"

	raw, err := d.CreateVoteOnParameterProposalTransaction(context.Background(), []interface{}{
		"proposal_abcd1234abcd1234",
		voter,
		true,
	})
	if err != nil {
		t.Fatalf("rpc error: %v", err)
	}
	draft := assertUnsignedDraft(t, raw, voter, "0x7b")
	if draft.DomainHash == "" || len(draft.DomainHash) != 66 {
		t.Fatalf("domainHash invalid: %s", draft.DomainHash)
	}
	if draft.To == nil || !strings.EqualFold(*draft.To, voter) {
		t.Fatalf("vote to should be self-call")
	}
}
