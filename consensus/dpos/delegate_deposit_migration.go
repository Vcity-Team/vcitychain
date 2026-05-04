package dpos

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/state"
	"github.com/Vcity-Team/vcitychain/types"
)

// 迁移交易 calldata：与 REG 对齐 8 字节头 — "DPOS" + "MIG" + 0x00，随后 uint32(大端) 地址数量，再跟 count×20 字节地址。
// 仅当 tx.From == delegateDepositMigrationAuthorityAddress（在 escrow.go 中写死，全节点同一二进制）时执行：
// 对每个源地址：当前余额全部 SubBalance(addr) -> AddBalance(候选人托管)，用于老 To=nil 注册产生的 CREATE 地址归集。
// 项目方用该地址的私钥发交易即可，无需老用户私钥、无需各节点 YAML。

const (
	// DelegateDepositMigrationMaxAddresses is the maximum source addresses per migration transaction.
	DelegateDepositMigrationMaxAddresses = 64
	delegateDepositMigrationHeader       = 8 // "DPOS"(4) + "MIG"(3) + 0x00(1)
)

func isDelegateDepositMigrationCalldata(input []byte) bool {
	if len(input) < delegateDepositMigrationHeader+4 {
		return false
	}
	if string(input[:4]) != "DPOS" || string(input[4:7]) != "MIG" {
		return false
	}
	if input[7] != 0 {
		return false
	}
	n := binary.BigEndian.Uint32(input[8:12])
	if n == 0 || n > DelegateDepositMigrationMaxAddresses {
		return false
	}
	return len(input) >= delegateDepositMigrationHeader+4+int(n)*20
}

func parseDelegateDepositMigrationAddresses(input []byte) ([]types.Address, error) {
	if !isDelegateDepositMigrationCalldata(input) {
		return nil, fmt.Errorf("invalid DPOS deposit migration calldata")
	}
	n := binary.BigEndian.Uint32(input[8:12])
	off := delegateDepositMigrationHeader + 4
	out := make([]types.Address, 0, n)
	for i := uint32(0); i < n; i++ {
		if off+20 > len(input) {
			return nil, fmt.Errorf("migration calldata truncated at address %d", i)
		}
		out = append(out, types.BytesToAddress(input[off:off+20]))
		off += 20
	}
	return out, nil
}

// BuildDelegateDepositMigrationCalldata encodes the same wire format as parseDelegateDepositMigrationAddresses expects.
func BuildDelegateDepositMigrationCalldata(addrs []types.Address) ([]byte, error) {
	n := len(addrs)
	if n == 0 || n > DelegateDepositMigrationMaxAddresses {
		return nil, fmt.Errorf("delegate deposit migration: address count must be 1..%d", DelegateDepositMigrationMaxAddresses)
	}
	out := make([]byte, 0, delegateDepositMigrationHeader+4+n*types.AddressLength)
	out = append(out, []byte("DPOS")...)
	out = append(out, []byte("MIG")...)
	out = append(out, 0)
	var nb [4]byte
	binary.BigEndian.PutUint32(nb[:], uint32(n))
	out = append(out, nb[:]...)
	for _, a := range addrs {
		out = append(out, a.Bytes()...)
	}
	return out, nil
}

// ApplyDelegateDepositMigrationAfterTx 在 transition.Write(tx) 成功后调用：受控迁移系统交易。
func (d *DPoS) ApplyDelegateDepositMigrationAfterTx(transition *state.Transition, tx *types.Transaction, blockNumber uint64) error {
	if transition == nil || tx == nil {
		return nil
	}
	auth := d.getDelegateDepositMigrationAuthorityAddress()
	if auth == (types.Address{}) {
		return nil
	}
	if tx.From == (types.Address{}) || tx.From != auth {
		return nil
	}
	if !isDelegateDepositMigrationCalldata(tx.Input) {
		return nil
	}
	addrs, err := parseDelegateDepositMigrationAddresses(tx.Input)
	if err != nil {
		return fmt.Errorf("delegate deposit migration: %w", err)
	}
	return d.applyDelegateDepositMigrationSweep(transition, addrs, blockNumber, tx.Hash.String())
}

func (d *DPoS) applyDelegateDepositMigrationSweep(transition *state.Transition, sources []types.Address, blockNumber uint64, txHash string) error {
	txn := transition.Txn()
	escrow := d.getDelegateDepositEscrowAddress()
	stakeEsc := d.getStakeEscrowAddress()
	zero := types.Address{}

	for _, src := range sources {
		if src == zero || src == escrow || src == stakeEsc {
			d.logger.Warn("⚠️ 跳过迁移地址（零地址或托管）", "addr", src.String(), "tx", txHash)
			continue
		}
		bal := txn.GetBalance(src)
		if bal == nil || bal.Sign() <= 0 {
			continue
		}
		amount := new(big.Int).Set(bal)
		if err := txn.SubBalance(src, amount); err != nil {
			return fmt.Errorf("delegate deposit migration: SubBalance src=%s amount=%s block=%d tx=%s: %w",
				src.String(), amount.String(), blockNumber, txHash, err)
		}
		txn.AddBalance(escrow, amount)
		d.logger.Info("✅ 候选人老保证金已迁入托管",
			"block", blockNumber, "tx", txHash, "fromAddr", src.String(), "amountWei", amount.String(), "escrow", escrow.String())
	}
	return nil
}
