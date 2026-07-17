package dpos

import (
	"fmt"
	"testing"
	"time"

	"github.com/Vcity-Team/vcitychain/consensus/dpos/wallet"
	"github.com/Vcity-Team/vcitychain/types"
	"github.com/hashicorp/go-hclog"
)

// BenchmarkBlockBuilderFill 对比串行 Fill 与 DAG Fill 的耗时（不依赖运行中的节点）。
//
// 用法：
//   go test ./consensus/dpos -bench=BenchmarkBlockBuilderFill -benchtime=3x -count=1
//   go test ./consensus/dpos -bench=BenchmarkBlockBuilderFill/Accounts200 -benchtime=5x
func BenchmarkBlockBuilderFill(b *testing.B) {
	sizes := []int{100, 200, 400}
	for _, n := range sizes {
		b.Run(fmt.Sprintf("Accounts%d", n), func(b *testing.B) {
			benchmarkBlockBuilderFillAccounts(b, n, false)
			benchmarkBlockBuilderFillAccounts(b, n, true)
		})
	}
}

func benchmarkBlockBuilderFillAccounts(b *testing.B, numAccounts int, enableDAG bool) {
	mode := "Serial"
	if enableDAG {
		mode = "DAG"
	}
	b.Run(mode, func(b *testing.B) {
		accounts := make([]*wallet.Account, numAccounts)
		recipients := make([]types.Address, numAccounts)
		for i := 0; i < numAccounts; i++ {
			accounts[i] = testWalletAccount(b)
			recipients[i] = types.Address(testWalletAccount(b).Ecdsa.Address())
		}

		logger := hclog.NewNullLogger()
		gasLimit := uint64(30_000_000)

		b.ReportAllocs()
		b.ResetTimer()

		for i := 0; i < b.N; i++ {
			b.StopTimer()
			executor, parent, signer := setupParallelBlockBuilderTest(b, accounts)
			executor.SetEnableParallelExecution(true)
			txs := make([]*types.Transaction, numAccounts)
			for j := 0; j < numAccounts; j++ {
				txs[j] = signedTransfer(b, signer, accounts[j], recipients[j], 1)
			}
			pool := newStubTxPool(txs...)
			builder := NewBlockBuilder(&BlockBuilderParams{
				BlockTime:          time.Second,
				Parent:             parent,
				Coinbase:           types.ZeroAddress,
				Executor:           executor,
				GasLimit:           gasLimit,
				TxPool:             pool,
				Logger:             logger,
				EnableDAGExecution: enableDAG,
			})
			bb, ok := builder.(*BlockBuilder)
			if !ok {
				b.Fatalf("unexpected block builder type")
			}
			if err := bb.Reset(); err != nil {
				b.Fatalf("reset: %v", err)
			}
			b.StartTimer()

			if err := bb.Fill(); err != nil {
				b.Fatalf("fill: %v", err)
			}

			b.StopTimer()
			if len(bb.txns) != numAccounts {
				b.Fatalf("filled %d txs, want %d", len(bb.txns), numAccounts)
			}
		}
	})
}

// TestBlockBuilderFillTimingReport 单次打印 Serial vs DAG 填块耗时（便于本地快速对比）。
func TestBlockBuilderFillTimingReport(t *testing.T) {
	if testing.Short() {
		t.Skip("short mode")
	}

	const numAccounts = 200
	accounts := make([]*wallet.Account, numAccounts)
	recipients := make([]types.Address, numAccounts)
	for i := 0; i < numAccounts; i++ {
		accounts[i] = testWalletAccount(t)
		recipients[i] = types.Address(testWalletAccount(t).Ecdsa.Address())
	}

	logger := hclog.NewNullLogger()
	runOnce := func(enableDAG bool) (time.Duration, int) {
		executor, parent, signer := setupParallelBlockBuilderTest(t, accounts)
		executor.SetEnableParallelExecution(true)
		txs := make([]*types.Transaction, numAccounts)
		for i := 0; i < numAccounts; i++ {
			txs[i] = signedTransfer(t, signer, accounts[i], recipients[i], 1)
		}
		pool := newStubTxPool(txs...)
		builder := NewBlockBuilder(&BlockBuilderParams{
			BlockTime:          time.Second,
			Parent:             parent,
			Coinbase:           types.ZeroAddress,
			Executor:           executor,
			GasLimit:           parent.GasLimit,
			TxPool:             pool,
			Logger:             logger,
			EnableDAGExecution: enableDAG,
		})
		bb, ok := builder.(*BlockBuilder)
		if !ok {
			t.Fatalf("unexpected block builder type")
		}
		if err := bb.Reset(); err != nil {
			t.Fatalf("reset: %v", err)
		}
		start := time.Now()
		if err := bb.Fill(); err != nil {
			t.Fatalf("fill: %v", err)
		}
		return time.Since(start), len(bb.txns)
	}

	const runs = 5
	var serialTotal, dagTotal time.Duration
	serialTxs, dagTxs := 0, 0
	for i := 0; i < runs; i++ {
		d, n := runOnce(false)
		serialTotal += d
		serialTxs = n
		d, n = runOnce(true)
		dagTotal += d
		dagTxs = n
	}

	serialAvg := serialTotal / runs
	dagAvg := dagTotal / runs
	speedup := float64(serialAvg) / float64(dagAvg)

	t.Logf("Fill timing (%d independent EOA transfers, %d runs):", numAccounts, runs)
	t.Logf("  Serial avg: %v  (txs=%d)", serialAvg, serialTxs)
	t.Logf("  DAG    avg: %v  (txs=%d)", dagAvg, dagTxs)
	t.Logf("  Speedup:     %.2fx", speedup)

	if dagTxs != numAccounts || serialTxs != numAccounts {
		t.Fatalf("unexpected tx count serial=%d dag=%d want=%d", serialTxs, dagTxs, numAccounts)
	}

	// DAG should not be slower than serial on independent accounts.
	if dagAvg > serialAvg*2 {
		t.Logf("warning: DAG slower than expected; check dependency detection overhead")
	}
}
