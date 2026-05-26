package syncer

import (
	"fmt"
	"testing"
	"time"

	"github.com/Vcity-Team/vcitychain/blockchain"
	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
	"github.com/stretchr/testify/require"
)

type stubTimestampVerifier struct{}

func (stubTimestampVerifier) VerifyHeader(*types.Header) error {
	return fmt.Errorf("timestamp older than parent")
}

func (stubTimestampVerifier) ProcessHeaders([]*types.Header) error { return nil }

func (stubTimestampVerifier) GetBlockCreator(*types.Header) (types.Address, error) {
	return types.ZeroAddress, nil
}

func (stubTimestampVerifier) PreCommitState(*types.Block, *state.Transition) error { return nil }

type stubPreProduceBlockchain struct {
	header   *types.Header
	verifier blockchain.Verifier
}

func (s *stubPreProduceBlockchain) SubscribeEvents() blockchain.Subscription { return nil }
func (s *stubPreProduceBlockchain) UnsubscribeEvents(blockchain.Subscription) {}
func (s *stubPreProduceBlockchain) Header() *types.Header                      { return s.header }
func (s *stubPreProduceBlockchain) GetBlockByNumber(uint64, bool) (*types.Block, bool) {
	return nil, false
}
func (s *stubPreProduceBlockchain) VerifyFinalizedBlock(*types.Block) (*types.FullBlock, error) {
	return nil, nil
}
func (s *stubPreProduceBlockchain) StageSyncReceipts(types.Hash, []*types.Receipt) {}
func (s *stubPreProduceBlockchain) WriteBlock(*types.Block, string) error      { return nil }
func (s *stubPreProduceBlockchain) WriteFullBlock(*types.FullBlock, string) error { return nil }
func (s *stubPreProduceBlockchain) HealCanonicalBlockState(*types.Block) (*types.FullBlock, error) {
	return nil, nil
}
func (s *stubPreProduceBlockchain) WriteBlockWithoutConsensus(*types.Block, string) error {
	return nil
}
func (s *stubPreProduceBlockchain) GetConsensus() blockchain.Verifier { return s.verifier }

func TestPreProduceProbeRejectReason_TimestampOlderThanParent(t *testing.T) {
	parentTS := time.Date(2026, 5, 22, 12, 48, 46, 0, time.UTC)
	parent := &types.Header{
		Number:    15921378,
		Timestamp: uint64(parentTS.Unix()),
	}
	parent.Hash = types.HeaderHash(parent)

	child := &types.Header{
		Number:     15921379,
		ParentHash: parent.Hash,
		Timestamp:  uint64(parentTS.Unix()),
		Nonce:      types.ZeroNonce,
		MixHash:    types.Hash{},
		ExtraData:  make([]byte, 32),
		GasLimit:   1,
		Difficulty: 1,
	}
	child.Hash = types.HeaderHash(child)

	s := &syncer{
		logger: hclog.NewNullLogger(),
		blockchain: &stubPreProduceBlockchain{
			header:   parent,
			verifier: stubTimestampVerifier{},
		},
	}
	reason := s.preProduceProbeRejectReason(&types.Block{Header: child}, parent.Hash, parent.Number)
	require.Equal(t, "timestamp older than parent", reason)
}
