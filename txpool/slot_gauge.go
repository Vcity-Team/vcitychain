package txpool

import (
	"sync/atomic"

	"github.com/armon/go-metrics"

	"github.com/Vcity-Team/vcitychain/types"
)

const (
	highPressureMark = 80 // 80%
)

// Gauge for measuring pool capacity in slots
type slotGauge struct {
	height uint64 // amount of slots currently occupying the pool
	max    uint64 // max limit
}

// read returns the current height of the gauge.
// Safety check: if height exceeds max (due to previous underflow bug), clamp it to max.
func (g *slotGauge) read() uint64 {
	height := atomic.LoadUint64(&g.height)
	// If height exceeds max, it indicates a previous underflow bug
	// Clamp it to max to prevent showing >100% and causing transaction rejection
	if height > g.max {
		// Try to fix it atomically (only if it's still the bad value)
		atomic.CompareAndSwapUint64(&g.height, height, g.max)
		return g.max
	}
	return height
}

// increase increases the height of the gauge by the specified slots amount.
func (g *slotGauge) increase(slots uint64) {
	newHeight := atomic.AddUint64(&g.height, slots)
	metrics.SetGauge([]string{txPoolMetrics, "slots_used"}, float32(newHeight))
}

// increaseWithinLimit increases the height of the gauge by the specified slots amount only if the increased height is
// less than max. Returns true if the height is increased.
// Prevents overflow: checks for overflow before addition.
func (g *slotGauge) increaseWithinLimit(slots uint64) (updated bool) {
	if slots == 0 {
		return true
	}
	
	for {
		old := g.read()
		
		// Check for overflow before addition
		if old > g.max - slots {
			// Would overflow or exceed max, reject
			return false
		}
		
		newHeight := old + slots

		if newHeight > g.max {
			return false
		}

		if atomic.CompareAndSwapUint64(&g.height, old, newHeight) {
			metrics.SetGauge([]string{txPoolMetrics, "slots_used"}, float32(newHeight))

			return true
		}
		// CAS failed, retry
	}
}

// decrease decreases the height of the gauge by the specified slots amount.
// Prevents underflow: if slots > current height, sets height to 0.
// Also prevents overflow: if the result would be > max due to underflow, clamps to max.
func (g *slotGauge) decrease(slots uint64) {
	if slots == 0 {
		return
	}
	
	for {
		old := atomic.LoadUint64(&g.height)
		
		// Check for underflow
		if slots > old {
			// Underflow detected: trying to decrease more than current height
			// This indicates a bug: slots were released multiple times or incorrectly calculated
			// Set to 0 to prevent negative values (which would wrap to a huge number)
			if atomic.CompareAndSwapUint64(&g.height, old, 0) {
				metrics.SetGauge([]string{txPoolMetrics, "slots_used"}, float32(0))
				return
			}
			// CAS failed, retry
			continue
		}
		
		newHeight := old - slots
		
		// Safety check: if newHeight somehow exceeds max (shouldn't happen, but just in case)
		if newHeight > g.max {
			// This should never happen, but if it does, clamp to max
			if atomic.CompareAndSwapUint64(&g.height, old, g.max) {
				metrics.SetGauge([]string{txPoolMetrics, "slots_used"}, float32(g.max))
				return
			}
			// CAS failed, retry
			continue
		}
		
		if atomic.CompareAndSwapUint64(&g.height, old, newHeight) {
			metrics.SetGauge([]string{txPoolMetrics, "slots_used"}, float32(newHeight))
			return
		}
		// CAS failed, retry
	}
}

// highPressure checks if the gauge level
// is higher than the 0.8*max threshold
func (g *slotGauge) highPressure() bool {
	return g.read() > (highPressureMark*g.max)/100
}

// free slots returns how many slots are currently available
func (g *slotGauge) freeSlots() uint64 {
	return g.max - g.read()
}

// slotsRequired calculates the number of slots required for given transaction(s).
func slotsRequired(txs ...*types.Transaction) uint64 {
	slots := uint64(0)
	for _, tx := range txs {
		slots += (tx.Size() + txSlotSize - 1) / txSlotSize
	}

	return slots
}
