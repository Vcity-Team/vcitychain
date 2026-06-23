package txpool

import (
	"errors"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/armon/go-metrics"
)

type reorgChainIndex struct {
	txHashes    map[types.Hash]struct{}
	minedNonces map[types.Address]map[uint64]struct{}
}

func buildReorgChainIndex(p *TxPool, newChain []*types.Header) reorgChainIndex {
	idx := reorgChainIndex{
		txHashes:    make(map[types.Hash]struct{}),
		minedNonces: make(map[types.Address]map[uint64]struct{}),
	}

	for _, header := range newChain {
		block, ok := p.store.GetBlockByHash(header.Hash, true)
		if !ok {
			p.logger.Warn("reorg reinject: new chain block not found", "hash", header.Hash.String())

			continue
		}

		for _, tx := range block.Transactions {
			if tx.Type == types.StateTx {
				continue
			}

			idx.txHashes[tx.Hash] = struct{}{}

			addr, err := p.recoverTxSender(tx)
			if err != nil {
				continue
			}

			if idx.minedNonces[addr] == nil {
				idx.minedNonces[addr] = make(map[uint64]struct{})
			}

			idx.minedNonces[addr][tx.Nonce] = struct{}{}
		}
	}

	return idx
}

func (p *TxPool) reinjectDiscarded(oldChain []*types.Header, idx reorgChainIndex) {
	if len(oldChain) == 0 {
		return
	}

	stateRoot := p.store.Header().StateRoot

	for _, header := range oldChain {
		block, ok := p.store.GetBlockByHash(header.Hash, true)
		if !ok {
			p.logger.Warn("reorg reinject: old chain block not found", "hash", header.Hash.String())

			continue
		}

		for _, tx := range block.Transactions {
			p.tryReinjectTx(tx, idx, stateRoot)
		}
	}
}

func (p *TxPool) recoverTxSender(tx *types.Transaction) (types.Address, error) {
	if tx.From != types.ZeroAddress {
		return tx.From, nil
	}

	if tx.Type == types.StateTx || p.signer == nil {
		return types.ZeroAddress, errors.New("cannot recover sender")
	}

	from, err := p.signer.Sender(tx)
	if err != nil {
		return types.ZeroAddress, err
	}

	tx.From = from

	return from, nil
}

func (p *TxPool) tryReinjectTx(tx *types.Transaction, idx reorgChainIndex, stateRoot types.Hash) {
	if tx.Type == types.StateTx {
		metrics.IncrCounter([]string{txPoolMetrics, "reorg_reinject_skip_state_tx"}, 1)

		return
	}

	if _, exists := idx.txHashes[tx.Hash]; exists {
		metrics.IncrCounter([]string{txPoolMetrics, "reorg_reinject_skip_known_canonical"}, 1)

		return
	}

	from, err := p.recoverTxSender(tx)
	if err != nil {
		p.logger.Debug("reorg reinject: skip tx with invalid sender", "hash", tx.Hash.String(), "err", err)
		metrics.IncrCounter([]string{txPoolMetrics, "reorg_reinject_skip_invalid_sender"}, 1)

		return
	}

	if nonces, ok := idx.minedNonces[from]; ok {
		if _, mined := nonces[tx.Nonce]; mined {
			metrics.IncrCounter([]string{txPoolMetrics, "reorg_reinject_skip_mined_nonce"}, 1)

			return
		}
	}

	chainNonce := p.store.GetNonce(stateRoot, from)
	if tx.Nonce < chainNonce {
		metrics.IncrCounter([]string{txPoolMetrics, "reorg_reinject_skip_nonce_low"}, 1)

		return
	}

	tx.ComputeHash(p.store.Header().Number)

	if err := p.processTx(tx, reorg); err != nil {
		switch {
		case errors.Is(err, ErrAlreadyKnown):
			metrics.IncrCounter([]string{txPoolMetrics, "reorg_reinject_skip_already_known"}, 1)
		case errors.Is(err, ErrNonceTooLow):
			metrics.IncrCounter([]string{txPoolMetrics, "reorg_reinject_skip_nonce_low"}, 1)
		case errors.Is(err, ErrInsufficientFunds):
			metrics.IncrCounter([]string{txPoolMetrics, "reorg_reinject_skip_insufficient_funds"}, 1)
		default:
			p.logger.Debug("reorg reinject: tx rejected", "hash", tx.Hash.String(), "err", err)
			metrics.IncrCounter([]string{txPoolMetrics, "reorg_reinject_rejected"}, 1)
		}

		return
	}

	metrics.IncrCounter([]string{txPoolMetrics, "reorg_reinject_ok"}, 1)
	p.logger.Info("reorg reinject: tx re-added to pool",
		"hash", tx.Hash.String(),
		"from", from.String(),
		"nonce", tx.Nonce)
}
