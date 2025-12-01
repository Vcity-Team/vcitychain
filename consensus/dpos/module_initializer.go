package dpos

import "fmt"

// moduleInitializer 模块初始化器，统一管理模块的初始化逻辑
type moduleInitializer struct {
	name      string
	initFunc  func()
	checkFunc func() bool
	required  bool // 是否必需（必需模块初始化失败会返回错误）
}

// initializeModules 统一初始化所有模块
func (d *DPoS) initializeModules() error {
	initializers := []moduleInitializer{
		{
			name:      "consensus",
			initFunc:  d.initConsensusModule,
			checkFunc: func() bool { return d.consensus != nil },
			required:  true,
		},
		{
			name:      "validator",
			initFunc:  d.initValidatorModule,
			checkFunc: func() bool { return d.validator != nil },
			required:  true,
		},
		{
			name:      "epoch",
			initFunc:  d.initEpochModule,
			checkFunc: func() bool { return d.epoch != nil },
			required:  true,
		},
		{
			name:      "reward",
			initFunc:  d.initRewardModule,
			checkFunc: func() bool { return d.reward != nil },
			required:  false, // reward 模块不是必需的
		},
		{
			name:      "fault",
			initFunc:  d.initFaultModule,
			checkFunc: func() bool { return d.fault != nil },
			required:  true,
		},
		{
			name:      "query",
			initFunc:  d.initQueryModule,
			checkFunc: func() bool { return d.query != nil },
			required:  false, // query 模块不是必需的
		},
		{
			name:      "governance",
			initFunc:  d.initGovernanceModule,
			checkFunc: func() bool { return d.governance != nil },
			required:  false, // governance 模块不是必需的
		},
		{
			name:      "epochLifecycle",
			initFunc:  d.initEpochLifecycleModule,
			checkFunc: func() bool { return d.epochLifecycle != nil },
			required:  false, // epochLifecycle 模块不是必需的
		},
		{
			name:      "network",
			initFunc:  d.initNetworkModule,
			checkFunc: func() bool { return d.network != nil },
			required:  true,
		},
		{
			name:      "bls",
			initFunc:  d.initBLSModule,
			checkFunc: func() bool { return d.bls != nil },
			required:  true,
		},
		{
			name:      "state",
			initFunc:  d.initStateModule,
			checkFunc: func() bool { return d.stateMgr != nil },
			required:  true,
		},
	}

	for _, init := range initializers {
		// 如果模块已初始化，跳过
		if init.checkFunc() {
			continue
		}

		// 执行初始化
		init.initFunc()

		// 检查初始化结果
		if !init.checkFunc() {
			if init.required {
				return fmt.Errorf("%s module not initialized", init.name)
			}
			// 非必需模块初始化失败时只记录警告
			d.logger.Warn("Optional module initialization failed", "module", init.name)
		} else {
			d.logger.Debug("Module initialized successfully", "module", init.name)
		}
	}

	return nil
}

