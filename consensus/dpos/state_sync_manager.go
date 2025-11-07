package dpos

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"path"
	"sync"
	"time"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/bitmap"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/contractsapi"
	polybftContractsapi "github.com/Vcity-Team/vcitychain/consensus/polybft/contractsapi"

	// polybftProto "github.com/Vcity-Team/vcitychain/consensus/dpos/proto"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/signer"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/wallet"
	"github.com/Vcity-Team/vcitychain/contracts"

	"github.com/Vcity-Team/vcitychain/merkle-tree"

	"github.com/Vcity-Team/vcitychain/tracker"
	"github.com/Vcity-Team/vcitychain/types"

	"github.com/hashicorp/go-hclog"
	"github.com/libp2p/go-libp2p/core/peer"
	"github.com/umbracle/ethgo"
	bolt "go.etcd.io/bbolt"
	"google.golang.org/protobuf/proto"
)

type Runtime interface {
	IsActiveValidator() bool
}

type StateSyncProof struct {
	Proof     []types.Hash
	StateSync *contractsapi.StateSyncedEvent
}

// CommitmentVote represents a vote for a commitment
type CommitmentVote struct {
	CommitmentHash types.Hash
	Signature      []byte
	Signer         types.Address
}

// StateSyncManager is an interface that defines functions for state sync workflow
type StateSyncManager interface {
	EventSubscriber
	Init() error
	Close()
	Commitment(blockNumber uint64) (*CommitmentMessageSigned, error)
	GetStateSyncProof(stateSyncID uint64) (types.Proof, error)
	PostBlock(req *PostBlockRequest) error
	PostEpoch(req *PostEpochRequest) error
}

var _ StateSyncManager = (*dummyStateSyncManager)(nil)

// dummyStateSyncManager is used when bridge is not enabled
type dummyStateSyncManager struct{}

func (d *dummyStateSyncManager) Init() error { return nil }
func (d *dummyStateSyncManager) Close()      {}
func (d *dummyStateSyncManager) Commitment(blockNumber uint64) (*CommitmentMessageSigned, error) {
	return nil, nil
}
func (d *dummyStateSyncManager) PostBlock(req *PostBlockRequest) error { return nil }
func (d *dummyStateSyncManager) PostEpoch(req *PostEpochRequest) error { return nil }
func (d *dummyStateSyncManager) GetStateSyncProof(stateSyncID uint64) (types.Proof, error) {
	return types.Proof{}, nil
}

// EventSubscriber implementation
func (d *dummyStateSyncManager) GetLogFilters() map[types.Address][]types.Hash {
	return make(map[types.Address][]types.Hash)
}
func (d *dummyStateSyncManager) ProcessLog(header *types.Header,
	log *ethgo.Log, dbTx *bolt.Tx) error {
	return nil
}

// stateSyncConfig holds the configuration data of state sync manager
type stateSyncConfig struct {
	stateSenderAddr          types.Address
	stateSenderStartBlock    uint64
	jsonrpcAddr              string
	dataDir                  string
	topic                    topic
	key                      *wallet.Key
	maxCommitmentSize        uint64
	numBlockConfirmations    uint64
	blockTrackerPollInterval time.Duration
}

var _ StateSyncManager = (*stateSyncManager)(nil)

// stateSyncManager is a struct that manages the workflow of
// saving and querying state sync events, and creating, and submitting new commitments
type stateSyncManager struct {
	logger hclog.Logger
	state  *State

	config  *stateSyncConfig
	closeCh chan struct{}

	// per epoch fields
	lock               sync.RWMutex
	pendingCommitments []*PendingCommitment
	validatorSet       validator.ValidatorSet
	epoch              uint64
	nextCommittedIndex uint64

	runtime Runtime
}

// topic is an interface for p2p message gossiping
type topic interface {
	Publish(obj proto.Message) error
	Subscribe(handler func(obj interface{}, from peer.ID)) error
}

// newStateSyncManager creates a new instance of state sync manager
func newStateSyncManager(logger hclog.Logger, state *State, config *stateSyncConfig,
	runtime Runtime) *stateSyncManager {
	return &stateSyncManager{
		logger:  logger,
		state:   state,
		config:  config,
		closeCh: make(chan struct{}),
		runtime: runtime,
	}
}

// Init subscribes to bridge topics (getting votes) and start the event tracker routine
func (s *stateSyncManager) Init() error {
	if err := s.initTracker(); err != nil {
		return fmt.Errorf("failed to init event tracker. Error: %w", err)
	}

	if err := s.initTransport(); err != nil {
		return fmt.Errorf("failed to initialize state sync transport layer. Error: %w", err)
	}

	return nil
}

func (s *stateSyncManager) Close() {
	close(s.closeCh)
}

// initTracker starts a new event tracker (to receive new state sync events)
func (s *stateSyncManager) initTracker() error {
	ctx, cancelFn := context.WithCancel(context.Background())

	evtTracker := tracker.NewEventTracker(
		path.Join(s.config.dataDir, "/deposit.db"),
		s.config.jsonrpcAddr,
		ethgo.Address(s.config.stateSenderAddr),
		s,
		s.config.numBlockConfirmations,
		s.config.stateSenderStartBlock,
		s.logger,
		s.config.blockTrackerPollInterval)

	go func() {
		<-s.closeCh
		cancelFn()
	}()

	return evtTracker.Start(ctx)
}

// initTransport subscribes to bridge topics (getting votes for commitments)
func (s *stateSyncManager) initTransport() error {
	return s.config.topic.Subscribe(func(obj interface{}, _ peer.ID) {
		if !s.runtime.IsActiveValidator() {
			// don't save votes if not a validator
			return
		}

	})
}

// saveVote saves the gotten vote to boltDb for later quorum check and signature aggregation
func (s *stateSyncManager) saveVote(msg *TransportMessage) error {
	s.lock.RLock()
	epoch := s.epoch
	valSet := s.validatorSet
	s.lock.RUnlock()

	if valSet == nil || msg.EpochNumber != epoch {
		// Epoch metadata is undefined or received a message for the irrelevant epoch
		return nil
	}

	if err := s.verifyVoteSignature(valSet, types.StringToAddress(msg.From), msg.Signature, msg.Hash); err != nil {
		return fmt.Errorf("error verifying vote signature: %w", err)
	}

	msgVote := &MessageSignature{
		From:      msg.From,
		Signature: msg.Signature,
	}

	numSignatures, err := s.state.StateSyncStore.insertMessageVote(msg.EpochNumber, msg.Hash, msgVote, nil)
	if err != nil {
		return fmt.Errorf("error inserting message vote: %w", err)
	}

	s.logger.Info(
		"deliver message",
		"hash", hex.EncodeToString(msg.Hash),
		"sender", msg.From,
		"signatures", numSignatures,
	)

	return nil
}

// Verifies signature of the message against the public key of the signer and checks if the signer is a validator
func (s *stateSyncManager) verifyVoteSignature(valSet validator.ValidatorSet, signerAddr types.Address,
	signature []byte, hash []byte) error {
	validator := valSet.Accounts().GetValidatorMetadata(signerAddr)
	if validator == nil {
		return fmt.Errorf("unable to resolve validator %s", signerAddr)
	}

	unmarshaledSignature, err := bls.UnmarshalSignature(signature)
	if err != nil {
		return fmt.Errorf("failed to unmarshal signature from signer %s, %w", signerAddr.String(), err)
	}

	if !unmarshaledSignature.Verify(validator.BlsKey, hash, signer.DomainStateReceiver) {
		return fmt.Errorf("incorrect signature from %s", signerAddr)
	}

	return nil
}

// AddLog saves the received log from event tracker if it matches a state sync event ABI
func (s *stateSyncManager) AddLog(eventLog *ethgo.Log) error {
	event := &contractsapi.StateSyncedEvent{}

	doesMatch, err := event.ParseLog(eventLog)
	if err != nil {
		return err
	}

	if !doesMatch {
		return nil
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	// save state sync event
	if err := s.state.StateSyncStore.insertStateSyncEvent(event); err != nil {
		return fmt.Errorf("failed to insert state sync event: %w", err)
	}

	s.logger.Debug("State sync event saved",
		"id", event.ID,
		"sender", event.Sender,
		"receiver", event.Receiver,
		"data", event.Data)

	return nil
}

// Commitment returns a commitment to be submitted if there is a pending commitment with quorum
func (s *stateSyncManager) Commitment(blockNumber uint64) (*CommitmentMessageSigned, error) {
	s.lock.RLock()
	defer s.lock.RUnlock()

	var largestCommitment *CommitmentMessageSigned

	// we start from the end, since last pending commitment is the largest one
	for i := len(s.pendingCommitments) - 1; i >= 0; i-- {
		commitment := s.pendingCommitments[i]
		aggregatedSignature, publicKeys, err := s.getAggSignatureForCommitmentMessage(blockNumber, commitment)

		if err != nil {
			if errors.Is(err, errQuorumNotReached) {
				// a valid case, commitment has no quorum, we should not return an error
				s.logger.Debug("can not submit a commitment, quorum not reached",
					"from", commitment.StartID.Uint64(),
					"to", commitment.EndID.Uint64())

				continue
			}

			return nil, err
		}

		largestCommitment = &CommitmentMessageSigned{
			Message:      commitment.StateSyncCommitment,
			AggSignature: aggregatedSignature,
			PublicKeys:   publicKeys,
		}

		break
	}

	return largestCommitment, nil
}

// getAggSignatureForCommitmentMessage checks if pending commitment has quorum,
// and if it does, aggregates the signatures
func (s *stateSyncManager) getAggSignatureForCommitmentMessage(blockNumber uint64,
	commitment *PendingCommitment) (Signature, [][]byte, error) {
	validatorSet := s.validatorSet

	validatorAddrToIndex := make(map[string]int, validatorSet.Len())
	validatorsMetadata := validatorSet.Accounts()

	for i, validator := range validatorsMetadata {
		validatorAddrToIndex[validator.Address.String()] = i
	}

	commitmentHash, err := commitment.Hash()
	if err != nil {
		return Signature{}, nil, err
	}

	// get all the votes from the database for this commitment
	votes, err := s.state.StateSyncStore.getMessageVotes(commitment.Epoch, commitmentHash.Bytes())
	if err != nil {
		return Signature{}, nil, err
	}

	var signatures bls.Signatures

	publicKeys := make([][]byte, 0)
	bmap := bitmap.Bitmap{}
	signers := make(map[types.Address]struct{}, 0)

	for _, vote := range votes {
		index, exists := validatorAddrToIndex[vote.From]
		if !exists {
			continue // don't count this vote, because it does not belong to validator
		}

		signature, err := bls.UnmarshalSignature(vote.Signature)
		if err != nil {
			return Signature{}, nil, err
		}

		bmap.Set(uint64(index))

		signatures = append(signatures, signature)
		publicKeys = append(publicKeys, validatorsMetadata[index].BlsKey.Marshal())
		signers[types.StringToAddress(vote.From)] = struct{}{}
	}

	if !validatorSet.HasQuorum(blockNumber, signers) {
		return Signature{}, nil, errQuorumNotReached
	}

	aggregatedSignature, err := signatures.Aggregate().Marshal()
	if err != nil {
		return Signature{}, nil, err
	}

	result := Signature{
		AggregatedSignature: aggregatedSignature,
		Bitmap:              bmap,
	}

	return result, publicKeys, nil
}

// PostEpoch notifies the state sync manager that an epoch has changed,
// so that it can discard any previous epoch commitments, and build a new one (since validator set changed)
func (s *stateSyncManager) PostEpoch(req *PostEpochRequest) error {
	s.lock.Lock()

	s.pendingCommitments = nil
	s.validatorSet = req.ValidatorSet
	s.epoch = req.NewEpochID

	// build a new commitment at the end of the epoch
	nextCommittedIndex, err := req.SystemState.GetNextCommittedIndex()
	if err != nil {
		s.lock.Unlock()

		return err
	}

	s.nextCommittedIndex = nextCommittedIndex

	s.lock.Unlock()

	return s.buildCommitment(req.DBTx)
}

// PostBlock notifies state sync manager that a block was finalized,
// so that it can build state sync proofs if a block has a commitment submission transaction.
// Additionally, it will remove any processed state sync events and their proofs from the store.
func (s *stateSyncManager) PostBlock(req *PostBlockRequest) error {
	commitment, err := getCommitmentMessageSignedTx(req.FullBlock.Block.Transactions)
	if err != nil {
		return err
	}

	// no commitment message -> this is not end of sprint block
	if commitment == nil {
		return nil
	}

	if err := s.state.StateSyncStore.insertCommitmentMessage(commitment, req.DBTx); err != nil {
		return fmt.Errorf("insert commitment message error: %w", err)
	}

	if err := s.buildProofs(commitment.Message, req.DBTx); err != nil {
		return fmt.Errorf("build commitment proofs error: %w", err)
	}

	s.lock.Lock()
	defer s.lock.Unlock()
	// update the nextCommittedIndex since a commitment was submitted
	s.nextCommittedIndex = commitment.Message.EndID.Uint64() + 1
	// commitment was submitted, so discard what we have in memory, so we can build a new one
	s.pendingCommitments = nil

	return nil
}

// GetStateSyncProof returns the proof for the state sync
func (s *stateSyncManager) GetStateSyncProof(stateSyncID uint64) (types.Proof, error) {
	stateSyncProof, err := s.state.StateSyncStore.getStateSyncProof(stateSyncID)
	if err != nil {
		return types.Proof{}, fmt.Errorf("cannot get state sync proof for StateSync id %d: %w", stateSyncID, err)
	}

	if stateSyncProof == nil {
		// check if we might've missed a commitment. if it is so, we didn't build proofs for it while syncing
		// if we are all synced up, commitment will be saved through PostBlock, but we won't have proofs,
		// so we will build them now and save them to db so that we have proofs for missed commitment
		commitment, err := s.state.StateSyncStore.getCommitmentForStateSync(stateSyncID)
		if err != nil {
			return types.Proof{}, fmt.Errorf("cannot find commitment for StateSync id %d: %w", stateSyncID, err)
		}

		if err := s.buildProofs(commitment.Message, nil); err != nil {
			return types.Proof{}, fmt.Errorf("cannot build proofs for commitment for StateSync id %d: %w", stateSyncID, err)
		}

		stateSyncProof, err = s.state.StateSyncStore.getStateSyncProof(stateSyncID)
		if err != nil {
			return types.Proof{}, fmt.Errorf("cannot get state sync proof for StateSync id %d: %w", stateSyncID, err)
		}
	}

	return types.Proof{
		Data: stateSyncProof.Proof,
		Metadata: map[string]interface{}{
			"StateSync": stateSyncProof.StateSync,
		},
	}, nil
}

// buildProofs builds state sync proofs for the submitted commitment and saves them in boltDb for later execution
func (s *stateSyncManager) buildProofs(commitmentMsg interface{}, dbTx *bolt.Tx) error {
	// 实现状态同步证明构建
	commitment, ok := commitmentMsg.(*contractsapi.StateSyncCommitment)
	if !ok {
		return fmt.Errorf("invalid commitment message type")
	}

	from := commitment.StartID.Uint64()
	to := commitment.EndID.Uint64()

	s.logger.Debug(
		"[buildProofs] Building proofs for commitment...",
		"fromIndex", from,
		"toIndex", to,
	)

	// 获取状态同步事件
	stateSyncEvents, err := s.state.StateSyncStore.getStateSyncEventsForCommitment(from, to, dbTx)
	if err != nil {
		return fmt.Errorf("failed to get state sync events: %w", err)
	}

	// 构建证明
	stateSyncProofs := make([]*StateSyncProof, 0, len(stateSyncEvents))
	for _, event := range stateSyncEvents {
		// 构建默克尔证明
		proof, err := s.buildMerkleProof(event, stateSyncEvents, dbTx)
		if err != nil {
			return fmt.Errorf("failed to build merkle proof for event %d: %w", event.ID.Uint64(), err)
		}

		stateSyncProofs = append(stateSyncProofs, &StateSyncProof{
			Proof:     proof,
			StateSync: event,
		})
	}

	s.logger.Debug(
		"[buildProofs] Building proofs for commitment finished.",
		"fromIndex", from,
		"toIndex", to,
		"proofsCount", len(stateSyncProofs),
	)

	return s.state.StateSyncStore.insertStateSyncProofs(stateSyncProofs, dbTx)
}

// calculateCommitmentRoot 计算承诺根哈希
func (s *stateSyncManager) calculateCommitmentRoot(events []*contractsapi.StateSyncedEvent) types.Hash {
	if len(events) == 0 {
		return types.ZeroHash
	}

	// 构建所有事件的哈希列表
	hashes := make([][]byte, 0, len(events))
	for _, event := range events {
		data, err := event.Encode()
		if err != nil {
			s.logger.Error("failed to encode event for root calculation", "error", err)
			continue
		}
		hashes = append(hashes, data)
	}

	// 构建默克尔树并获取根
	tree, err := merkle.NewMerkleTree(hashes)
	if err != nil {
		s.logger.Error("failed to create merkle tree for root calculation", "error", err)
		return types.ZeroHash
	}

	return tree.Hash()
}

// calculateCommitmentHash 计算承诺哈希
func (s *stateSyncManager) calculateCommitmentHash(commitment *polybftContractsapi.StateSyncCommitment) types.Hash {
	// TODO: 实现承诺编码逻辑
	// 这里需要根据实际的承诺结构来实现编码
	return types.ZeroHash
}

// signCommitment 签名承诺
func (s *stateSyncManager) signCommitment(commitment *polybftContractsapi.StateSyncCommitment) ([]byte, error) {
	hash := s.calculateCommitmentHash(commitment)

	// 使用钱包密钥签名
	signature, err := s.config.key.Sign(hash.Bytes())
	if err != nil {
		return nil, fmt.Errorf("failed to sign commitment: %w", err)
	}

	return signature, nil
}

// buildMerkleProof 构建默克尔证明
func (s *stateSyncManager) buildMerkleProof(event *contractsapi.StateSyncedEvent, allEvents []*contractsapi.StateSyncedEvent, dbTx *bolt.Tx) ([]types.Hash, error) {
	// 构建所有事件的哈希列表
	hashes := make([][]byte, 0, len(allEvents))
	for _, e := range allEvents {
		// 序列化事件
		data, err := e.Encode()
		if err != nil {
			return nil, fmt.Errorf("failed to encode event: %w", err)
		}
		hashes = append(hashes, data)
	}

	// 构建默克尔树
	tree, err := merkle.NewMerkleTree(hashes)
	if err != nil {
		return nil, fmt.Errorf("failed to create merkle tree: %w", err)
	}

	// 找到目标事件的索引
	targetIndex := -1
	for i, e := range allEvents {
		if e.ID.Cmp(event.ID) == 0 {
			targetIndex = i
			break
		}
	}

	if targetIndex == -1 {
		return nil, fmt.Errorf("target event not found in all events")
	}

	// 生成证明路径 - 需要传递叶子节点的数据
	leafData, err := event.Encode()
	if err != nil {
		return nil, fmt.Errorf("failed to encode event: %w", err)
	}
	proof, err := tree.GenerateProof(leafData)
	if err != nil {
		return nil, fmt.Errorf("failed to generate proof: %w", err)
	}

	// 转换为types.Hash类型
	result := make([]types.Hash, len(proof))
	for i, p := range proof {
		result[i] = p
	}

	return result, nil
}

// buildCommitment builds a new commitment, signs it and gossips its vote for it
func (s *stateSyncManager) buildCommitment(dbTx *bolt.Tx) error {
	if !s.runtime.IsActiveValidator() {
		// don't build commitment if not a validator
		return nil
	}

	s.lock.Lock()
	defer s.lock.Unlock()

	stateSyncEvents, err := s.state.StateSyncStore.getStateSyncEventsForCommitment(s.nextCommittedIndex,
		s.nextCommittedIndex+s.config.maxCommitmentSize-1, dbTx)
	if err != nil && !errors.Is(err, errNotEnoughStateSyncs) {
		return fmt.Errorf("failed to get state sync events for commitment. Error: %w", err)
	}

	if len(stateSyncEvents) == 0 {
		// there are no state sync events
		return nil
	}

	if len(s.pendingCommitments) > 0 &&
		s.pendingCommitments[len(s.pendingCommitments)-1].StartID.Cmp(stateSyncEvents[len(stateSyncEvents)-1].ID) >= 0 {
		// already built a commitment of this size which is pending to be submitted
		return nil
	}

	// 构建承诺消息
	commitment := &polybftContractsapi.StateSyncCommitment{
		StartID: stateSyncEvents[0].ID,
		EndID:   stateSyncEvents[len(stateSyncEvents)-1].ID,
		Root:    s.calculateCommitmentRoot(stateSyncEvents),
	}

	// 签名承诺
	signature, err := s.signCommitment(commitment)
	if err != nil {
		return fmt.Errorf("failed to sign commitment: %w", err)
	}

	// TODO: 创建已签名的承诺消息
	// signedCommitment := &CommitmentMessageSigned{
	// 	Message: commitment,
	// 	// TODO: 设置聚合签名
	// }

	// 添加到待处理承诺列表
	s.pendingCommitments = append(s.pendingCommitments, &PendingCommitment{
		StateSyncCommitment: commitment,
		Epoch:               s.epoch,
		// TODO: 设置默克尔树
	})

	// 广播投票
	s.multicast(&CommitmentVote{
		CommitmentHash: s.calculateCommitmentHash(commitment),
		Signature:      signature,
		Signer:         types.Address(s.config.key.Address()),
	})

	s.logger.Info("Built new commitment",
		"startID", commitment.StartID,
		"endID", commitment.EndID,
		"eventsCount", len(stateSyncEvents))

	return nil
}

// multicast publishes given message to the rest of the network
func (s *stateSyncManager) multicast(msg interface{}) {
	data, err := json.Marshal(msg)
	if err != nil {
		s.logger.Warn("failed to marshal bridge message", "err", err)
		return
	}

	// TODO: 暂时注释掉 proto 相关代码，等 proto 问题解决后再启用
	// 暂时使用 data 变量避免编译错误
	_ = data
	/*
		err = s.config.topic.Publish(&TransportMessage{Data: data})
		if err != nil {
			s.logger.Warn("failed to gossip bridge message", "err", err)
			return
		}
	*/

	s.logger.Info("State sync message publishing temporarily disabled")
}

// EventSubscriber implementation

// GetLogFilters returns a map of log filters for getting desired events,
// where the key is the address of contract that emits desired events,
// and the value is a slice of signatures of events we want to get.
// This function is the implementation of EventSubscriber interface
func (s *stateSyncManager) GetLogFilters() map[types.Address][]types.Hash {
	var stateSyncResultEvent contractsapi.StateSyncResultEvent

	return map[types.Address][]types.Hash{
		contracts.StateReceiverContract: {types.Hash(stateSyncResultEvent.Sig())},
	}
}

// ProcessLog is the implementation of EventSubscriber interface,
// used to handle a log defined in GetLogFilters, provided by event provider
func (s *stateSyncManager) ProcessLog(header *types.Header, log *ethgo.Log, dbTx *bolt.Tx) error {
	var stateSyncResultEvent contractsapi.StateSyncResultEvent

	doesMatch, err := stateSyncResultEvent.ParseLog(log)
	if err != nil {
		return err
	}

	if !doesMatch {
		return nil
	}

	s.logger.Debug("State sync result event processed",
		"counter", stateSyncResultEvent.Counter,
		"status", stateSyncResultEvent.Status)

	return nil
}
