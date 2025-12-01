package dpos

// ensureStateStore 确保 state 和 StakeStore 已初始化
func (d *DPoS) ensureStateStore() error {
	if d.state == nil {
		return ErrStateNotInitialized
	}
	if d.state.StakeStore == nil {
		return ErrStakeStoreNotAvailable
	}
	return nil
}

// getStateStore 安全获取 StakeStore，如果未初始化则返回错误
func (d *DPoS) getStateStore() (*StakeStore, error) {
	if err := d.ensureStateStore(); err != nil {
		return nil, err
	}
	return d.state.StakeStore, nil
}

