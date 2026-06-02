package jsonrpc

import (
	consensusdpos "github.com/Vcity-Team/vcitychain/consensus/dpos"
	"github.com/Vcity-Team/vcitychain/types"
)

type canonicalHashReader interface {
	ReadCanonicalHash(uint64) (types.Hash, bool)
}

// resolveRPCBlockHash returns the hash to expose in JSON-RPC responses.
// Consensus block hashes are returned unchanged; only zero hashes are patched for explorers.
func resolveRPCBlockHash(store canonicalHashReader, h *types.Header) types.Hash {
	if h == nil {
		return types.ZeroHash
	}
	if h.Hash != types.ZeroHash {
		return h.Hash
	}
	if h.Number > 0 && store != nil {
		if canonical, ok := store.ReadCanonicalHash(h.Number); ok && canonical != types.ZeroHash {
			return canonical
		}
	}
	return consensusdpos.DisplayHeaderHash(h)
}
