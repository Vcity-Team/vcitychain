package dpos

import "fmt"

// validateDependencies 统一验证所有必需的依赖
func (d *DPoS) validateDependencies() error {
	required := []struct {
		name  string
		value interface{}
		err   error
	}{
		{"runtime", d.runtime, ErrRuntimeNotInitialized},
		{"blockchain", d.blockchain, ErrBlockchainNotAvailable},
		{"config", d.config, ErrConfigNotInitialized},
		{"state", d.state, ErrStateNotInitialized},
	}

	for _, dep := range required {
		if dep.value == nil {
			return fmt.Errorf("%s: %w", dep.name, dep.err)
		}
	}

	// 验证 config 的子字段
	if d.config != nil {
		if d.config.Blockchain == nil {
			return fmt.Errorf("config.blockchain: %w", ErrBlockchainConfigNotInitialized)
		}
	}

	return nil
}

