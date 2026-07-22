package dpos

import (
	"sync/atomic"

	ibftsigner "github.com/Vcity-Team/vcitychain/consensus/ibft/signer"
	"github.com/Vcity-Team/vcitychain/types"
)

var headerHashSwitchHeight atomic.Uint64

// InitHeaderHash installs DPoS header hash routing. Safe to call multiple times.
func InitHeaderHash() {
	setupHeaderHashFunc()
}

// ConfigureHeaderHashSwitchHeight sets the block height where DPoS header hashing takes precedence.
// Also (re)installs the DPoS hash router so IBFT's earlier SetHeaderHash cannot remain in effect
// after the consensus switch.
func ConfigureHeaderHashSwitchHeight(height uint64) {
	setHeaderHashSwitchHeight(height)
	types.HeaderHash = func(h *types.Header) types.Hash {
		return displayHeaderHash(h)
	}
}

func setHeaderHashSwitchHeight(height uint64) {
	headerHashSwitchHeight.Store(height)
}

func hashHeaderWithCleanExtra(
	h *types.Header,
	cleanExtra []byte,
) types.Hash {
	hh := h.Copy()
	hh.ExtraData = cleanExtra

	return types.DefaultHeaderHash(hh)
}

func tryDPoSHeaderHash(h *types.Header) types.Hash {
	extra, err := GetDposExtraClean(h.ExtraData)
	if err != nil {
		return types.ZeroHash
	}

	return hashHeaderWithCleanExtra(h, extra)
}

func tryLegacyPolyBFTHeaderHash(h *types.Header) types.Hash {
	extra, err := getLegacyPolyBFTExtraClean(h.ExtraData)
	if err != nil {
		return types.ZeroHash
	}

	return hashHeaderWithCleanExtra(h, extra)
}

func tryLegacyIBFTHeaderHash(h *types.Header) types.Hash {
	hash, err := ibftsigner.HeaderHashLegacyIBFT(h)
	if err != nil {
		return types.ZeroHash
	}

	return hash
}

// DisplayHeaderHash computes a block hash for JSON-RPC display only.
// It must not be used for consensus, sync, or block storage.
func DisplayHeaderHash(h *types.Header) types.Hash {
	return displayHeaderHash(h)
}

func displayHeaderHash(h *types.Header) types.Hash {
	switchHeight := headerHashSwitchHeight.Load()

	tryInOrder := func(fns ...func(*types.Header) types.Hash) types.Hash {
		for _, fn := range fns {
			if hash := fn(h); hash != types.ZeroHash {
				return hash
			}
		}

		return types.ZeroHash
	}

	if switchHeight > 0 && h.Number >= switchHeight {
		if hash := tryInOrder(tryDPoSHeaderHash, tryLegacyPolyBFTHeaderHash, tryLegacyIBFTHeaderHash); hash != types.ZeroHash {
			return hash
		}
	} else {
		// Pre-switch polyBFT blocks often carry extended extra (5+ fields). Istanbul parsing
		// can succeed but yields a different digest than the canonical hash stored at import time.
		if hash := tryInOrder(tryLegacyPolyBFTHeaderHash, tryDPoSHeaderHash, tryLegacyIBFTHeaderHash); hash != types.ZeroHash {
			return hash
		}
	}

	return types.DefaultHeaderHash(h)
}
