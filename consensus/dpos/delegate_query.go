package dpos

import (
	"fmt"
	"math/big"

	"github.com/Vcity-Team/vcitychain/types"
)

// GetDelegateRegistration 获取受托人注册信息
func (d *DPoS) GetDelegateRegistration(address types.Address) (*DelegateRegistration, error) {
	if d.state == nil || d.state.RegistrationStore == nil {
		return nil, fmt.Errorf("registration store not available")
	}

	reg, err := d.state.RegistrationStore.GetRegistration(address)
	if err != nil {
		return nil, fmt.Errorf("failed to get registration: %w", err)
	}

	return reg, nil
}

// IsDelegateRegistered 检查受托人是否已注册
func (d *DPoS) IsDelegateRegistered(address types.Address) bool {
	d.logger.Info("🔍 检查受托人注册状态", "address", address.String())

	if d.state == nil || d.state.RegistrationStore == nil {
		d.logger.Warn("❌ 注册存储不可用", "address", address.String())
		return false
	}

	reg, err := d.state.RegistrationStore.GetRegistration(address)
	if err != nil {
		d.logger.Warn("❌ 查询受托人注册信息失败",
			"address", address.String(),
			"error", err.Error())
		return false
	}

	isRegistered := reg != nil
	if isRegistered {
		d.logger.Info("✅ 受托人已注册",
			"address", address.String(),
			"name", reg.Name,
			"status", reg.Status.String())
	} else {
		d.logger.Warn("❌ 受托人未注册", "address", address.String())
	}

	return isRegistered
}

// IsDelegateCandidate 检查受托人是否为候选人状态（可以接受投票）
// 🆕 创世验证者可以直接被投票，无需注册
func (d *DPoS) IsDelegateCandidate(address types.Address) bool {
	d.logger.Info("🔍 检查受托人候选人状态", "address", address.String())

	// 🆕 创世验证者可以直接被投票，无需检查注册状态
	if d.isGenesisValidator(address) {
		d.logger.Info("✅ 受托人是创世验证者，可以直接接受投票",
			"address", address.String())
		return true
	}

	if d.state == nil || d.state.RegistrationStore == nil {
		d.logger.Warn("❌ 注册存储不可用", "address", address.String())
		return false
	}

	reg, err := d.state.RegistrationStore.GetRegistration(address)
	if err != nil {
		d.logger.Warn("❌ 查询受托人注册信息失败",
			"address", address.String(),
			"error", err.Error())
		return false
	}

	if reg == nil {
		d.logger.Warn("❌ 受托人未注册", "address", address.String())
		return false
	}

	isCandidate := reg.Status == RegStatusCandidate || reg.Status == RegStatusActive
	if isCandidate {
		d.logger.Info("✅ 受托人是候选人状态，可以接受投票",
			"address", address.String(),
			"name", reg.Name,
			"status", reg.Status.String(),
			"isActive", reg.IsActive)
	} else {
		d.logger.Warn("❌ 受托人不是候选人状态，不能接受投票",
			"address", address.String(),
			"name", reg.Name,
			"status", reg.Status.String(),
			"isActive", reg.IsActive)
	}

	return isCandidate
}

// calculateTotalVotedAmount 计算投票者的总已投票金额
func (d *DPoS) calculateTotalVotedAmount(voter types.Address) *big.Int {
	d.logger.Debug("🔍 计算投票者总已投票金额", "voter", voter.String())

	store, err := d.getStateStore()
	if err != nil {
		d.logger.Warn("StakeStore 不可用", "voter", voter.String(), "error", err)
		return big.NewInt(0)
	}

	// 从 VoterInfo 表直接获取投票者的总投票权重
	voterInfo, err := store.getVoterInfo(voter, nil)
	if err != nil {
		d.logger.Warn("从数据库获取投票者信息失败",
			"voter", voter.String(),
			"error", err)
		return big.NewInt(0)
	}

	if voterInfo != nil && voterInfo.VotingPower != nil {
		d.logger.Info("投票者总已投票金额获取成功",
			"voter", voter.String(),
			"totalVotedAmount", voterInfo.VotingPower.String())
		return new(big.Int).Set(voterInfo.VotingPower)
	}

	d.logger.Info("投票者无投票记录", "voter", voter.String())
	return big.NewInt(0)
}
