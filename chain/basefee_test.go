package chain

import (
	"testing"

	"github.com/Vcity-Team/vcitychain/types"
	"github.com/stretchr/testify/assert"
)

func TestEffectiveHeaderBaseFee(t *testing.T) {
	parent := &types.Header{Number: 1, BaseFee: 0}
	header := &types.Header{Number: 2, BaseFee: 0}
	london := ForksInTime{London: true}

	assert.Equal(t, uint64(0), EffectiveHeaderBaseFee(header, parent, ForksInTime{}, nil))

	assert.Equal(t, GenesisBaseFee, EffectiveHeaderBaseFee(header, parent, london, func(p *types.Header) uint64 {
		assert.Equal(t, uint64(1), p.Number)
		return GenesisBaseFee
	}))

	header.BaseFee = 5
	assert.Equal(t, uint64(5), EffectiveHeaderBaseFee(header, parent, london, nil))
}
