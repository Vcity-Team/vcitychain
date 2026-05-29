package types

import (
	"github.com/Vcity-Team/vcitychain/helper/keccak"
	"github.com/umbracle/fastrlp"
)

var HeaderHash func(h *Header) Hash

// This is the default header hash for the block.
// In IBFT, this header hash method is substituted
// for Istanbul Header Hash calculation
func init() {
	HeaderHash = defHeaderHash
}

var marshalArenaPool fastrlp.ArenaPool

func defHeaderHash(h *Header) (hash Hash) {
	// default header hashing
	ar := marshalArenaPool.Get()
	hasher := keccak.DefaultKeccakPool.Get()

	v := h.MarshalRLPWith(ar)
	hasher.WriteRlp(hash[:0], v)

	marshalArenaPool.Put(ar)
	keccak.DefaultKeccakPool.Put(hasher)

	return
}

// DefaultHeaderHash computes the keccak hash of the full header RLP (pre-consensus wrappers).
func DefaultHeaderHash(h *Header) Hash {
	return defHeaderHash(h)
}

// ComputeHash computes the hash of the header
func (h *Header) ComputeHash() *Header {
	h.Hash = HeaderHash(h)

	return h
}
