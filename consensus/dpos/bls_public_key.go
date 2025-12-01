package dpos

import (
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"

	"github.com/Vcity-Team/vcitychain/bls"
	"github.com/Vcity-Team/vcitychain/consensus/dpos/validator"
	"github.com/Vcity-Team/vcitychain/crypto"
	"github.com/Vcity-Team/vcitychain/types"
)

// readBLSPrivateKeyAndGeneratePublicKey 从私钥文件生成BLS公钥
func (d *DPoS) readBLSPrivateKeyAndGeneratePublicKey(validatorAddress types.Address) ([]byte, error) {
	// 1. 构建私钥文件路径 - 为每个验证者生成独立的BLS私钥文件
	parentDir := filepath.Dir(d.dataDir) // 获取 "node1\consensus"
	keyFilePath := filepath.Join(parentDir, "validator-bls.key")

	d.logger.Debug("🔍 BLS私钥文件路径",
		"dataDir", d.dataDir,
		"parentDir", parentDir,
		"keyFilePath", keyFilePath)

	// 2. 检查文件是否存在
	if _, err := os.Stat(keyFilePath); os.IsNotExist(err) {
		return nil, fmt.Errorf("BLS private key file not found: %s", keyFilePath)
	}

	// 3. 读取私钥文件
	privateKeyData, err := os.ReadFile(keyFilePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read BLS private key file: %w", err)
	}

	// 4. 获取十六进制字符串（去除可能的换行符）
	privateKeyHex := strings.TrimSpace(string(privateKeyData))

	// 5. 检查并修正私钥长度
	if len(privateKeyHex)%2 != 0 {
		d.logger.Info("Private key length is odd, adding leading zero",
			"originalLength", len(privateKeyHex),
			"originalKey", privateKeyHex)
		privateKeyHex = "0" + privateKeyHex
		d.logger.Info("Private key corrected",
			"correctedLength", len(privateKeyHex),
			"correctedKey", privateKeyHex)
	}

	// 6. 解析BLS私钥
	privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
	if err != nil {
		return nil, fmt.Errorf("failed to unmarshal BLS private key: %w", err)
	}

	// 7. 从私钥生成公钥
	publicKey := privateKey.PublicKey()
	publicKeyBytes := publicKey.Marshal()

	// 8. 验证公钥长度（应该是128字节）
	if len(publicKeyBytes) != 128 {
		return nil, fmt.Errorf("invalid BLS public key length: expected 128 bytes, got %d", len(publicKeyBytes))
	}

	return publicKeyBytes, nil
}

// getBLSKeyForValidator 获取指定验证者的BLS公钥
func (d *DPoS) getBLSKeyForValidator(address types.Address) (*bls.PublicKey, error) {
	// 检查是否是当前节点
	if d.key != nil && address == types.Address(d.key.Address()) {
		// 当前节点：从本地文件读取BLS私钥
		blsPublicKey, err := d.readBLSPrivateKeyAndGeneratePublicKey(address)
		if err != nil {
			return nil, fmt.Errorf("failed to read local BLS key: %w", err)
		}

		// 解析BLS公钥
		blsKey, err := bls.UnmarshalPublicKey(blsPublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal BLS key: %w", err)
		}

		d.logger.Debug("✅ 本地BLS公钥获取成功", "address", address.String())
		return blsKey, nil
	} else {
		// 其他节点：通过网络请求BLS公钥
		blsKey, err := d.requestBLSPublicKeyFromNetwork(address)
		if err != nil {
			return nil, fmt.Errorf("failed to request remote BLS key: %w", err)
		}

		d.logger.Debug("✅ 远程BLS公钥获取成功", "address", address.String())
		return blsKey, nil
	}
}

// GetBLSKeyForValidator 获取验证者的BLS公钥（公共方法）
func (d *DPoS) GetBLSKeyForValidator(address types.Address) (*bls.PublicKey, error) {
	// 检查是否是当前节点
	if d.key != nil && address == types.Address(d.key.Address()) {
		// 当前节点：从本地文件读取BLS私钥
		blsPublicKey, err := d.readBLSPrivateKeyAndGeneratePublicKey(address)
		if err != nil {
			return nil, fmt.Errorf("failed to read local BLS key: %w", err)
		}

		// 解析BLS公钥
		blsKey, err := bls.UnmarshalPublicKey(blsPublicKey)
		if err != nil {
			return nil, fmt.Errorf("failed to unmarshal BLS key: %w", err)
		}

		d.logger.Debug("✅ 本地BLS公钥获取成功", "address", address.String())
		return blsKey, nil
	} else {
		// 其他节点：通过网络请求BLS公钥
		blsKey, err := d.requestBLSPublicKeyFromNetwork(address)
		if err != nil {
			return nil, fmt.Errorf("failed to request remote BLS key: %w", err)
		}

		return blsKey, nil
	}
}

// EnsureBLSKeyForValidator 确保验证者有BLS公钥（如果为nil则获取）
func (d *DPoS) EnsureBLSKeyForValidator(validator *validator.ValidatorMetadata) error {
	if validator.BlsKey != nil {
		return nil // 已经有BLS公钥
	}

	blsKey, err := d.GetBLSKeyForValidator(validator.Address)
	if err != nil {
		return fmt.Errorf("failed to get BLS key for validator %s: %w", validator.Address.String(), err)
	}

	validator.BlsKey = blsKey
	d.logger.Info("✅ 验证者BLS公钥已设置", "address", validator.Address.String())
	return nil
}

// GetBLSKeyBytesFromGenesis 从validator-bls.key文件获取指定地址的BLS公钥
func (d *DPoS) GetBLSKeyBytesFromGenesis(address types.Address) ([]byte, error) {
	// 1. 检查是否是本地节点
	if d.key == nil || address != types.Address(d.key.Address()) {
		return nil, fmt.Errorf("只有本地节点才能从validator-bls.key文件获取BLS公钥，请求地址: %s", address.String())
	}

	// 2. 获取数据目录路径
	dataDir := d.getDataDir()
	if dataDir == "" {
		return nil, fmt.Errorf("data directory not available")
	}

	// 3. 构建BLS私钥文件路径 - 从dataDir的父目录找consensus
	parentDir := filepath.Dir(dataDir) // 获取 "node1\consensus"
	keyFilePath := filepath.Join(parentDir, "validator-bls.key")

	// 4. 检查文件是否存在
	if _, err := os.Stat(keyFilePath); os.IsNotExist(err) {
		d.logger.Debug("❌ BLS private key file not found",
			"address", address.String(),
			"filePath", keyFilePath)
		return nil, fmt.Errorf("BLS private key file not found: %s", keyFilePath)
	}

	// 4. 读取私钥文件
	privateKeyData, err := os.ReadFile(keyFilePath)
	if err != nil {
		d.logger.Error("❌ Failed to read BLS private key file",
			"address", address.String(),
			"filePath", keyFilePath,
			"error", err)
		return nil, fmt.Errorf("failed to read BLS private key file: %w", err)
	}

	// 5. 获取十六进制字符串（去除可能的换行符）
	privateKeyHex := strings.TrimSpace(string(privateKeyData))

	// 6. 检查并修正私钥长度
	if len(privateKeyHex)%2 != 0 {
		privateKeyHex = "0" + privateKeyHex
	}

	// 7. 解析BLS私钥
	privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
	if err != nil {
		d.logger.Error("❌ Failed to unmarshal BLS private key",
			"address", address.String(),
			"error", err)
		return nil, fmt.Errorf("failed to unmarshal BLS private key: %w", err)
	}

	// 8. 从私钥生成公钥
	publicKey := privateKey.PublicKey()
	publicKeyBytes := publicKey.Marshal()

	// 9. 验证公钥长度（应该是128字节）
	if len(publicKeyBytes) != 128 {
		return nil, fmt.Errorf("invalid BLS public key length: expected 128 bytes, got %d", len(publicKeyBytes))
	}

	return publicKeyBytes, nil
}

// getBLSKeyBytes 获取BLS公钥字节
func (d *DPoS) getBLSKeyBytes() ([]byte, error) {
	// 首先尝试从密钥管理器获取
	if d.config.SecretsManager != nil {
		blsKeyBytes, err := d.config.SecretsManager.GetSecret("bls")
		if err == nil && len(blsKeyBytes) > 0 {
			d.logger.Info("从密钥管理器获取BLS公钥", "length", len(blsKeyBytes))
			return blsKeyBytes, nil
		}
	}

	// 如果密钥管理器没有，尝试从validator-bls.key文件获取
	dataDir := d.getDataDir()
	if dataDir != "" {
		// 构建BLS私钥文件路径 - 从dataDir的父目录找consensus
		// dataDir = "node1\consensus\dpos"，需要回到 "node1\consensus"
		parentDir := filepath.Dir(dataDir) // 获取 "node1\consensus"
		keyFilePath := filepath.Join(parentDir, "validator-bls.key")

		// 检查文件是否存在
		if _, err := os.Stat(keyFilePath); err == nil {
			d.logger.Info("从validator-bls.key文件获取BLS公钥",
				"address", d.key.Address().String(),
				"filePath", keyFilePath)

			// 读取私钥文件
			privateKeyData, err := os.ReadFile(keyFilePath)
			if err == nil {
				// 获取十六进制字符串（去除可能的换行符）
				privateKeyHex := strings.TrimSpace(string(privateKeyData))

				// 检查并修正私钥长度
				if len(privateKeyHex)%2 != 0 {
					privateKeyHex = "0" + privateKeyHex
				}

				// 解析BLS私钥
				privateKey, err := bls.UnmarshalPrivateKey([]byte(privateKeyHex))
				if err == nil {
					// 从私钥生成公钥
					publicKey := privateKey.PublicKey()
					blsKeyBytes := publicKey.Marshal()
					d.logger.Info("成功从validator-bls.key文件获取BLS公钥",
						"address", d.key.Address().String(),
						"publicKeyLength", len(blsKeyBytes))
					return blsKeyBytes, nil
				} else {
					d.logger.Warn("解析validator-bls.key文件失败",
						"address", d.key.Address().String(),
						"filePath", keyFilePath,
						"error", err)
				}
			} else {
				d.logger.Warn("读取validator-bls.key文件失败",
					"address", d.key.Address().String(),
					"filePath", keyFilePath,
					"error", err)
			}
		} else {
			d.logger.Debug("validator-bls.key文件不存在",
				"address", d.key.Address().String(),
				"filePath", keyFilePath)
		}
	}

	// 如果都没有，生成新的BLS公钥
	d.logger.Warn("未找到BLS公钥，将生成新的BLS公钥")
	blsKey, err := d.generateBLSKey()
	if err != nil {
		return nil, fmt.Errorf("failed to generate BLS key: %w", err)
	}

	blsKeyBytes := blsKey.Marshal()
	d.logger.Info("生成新的BLS公钥", "length", len(blsKeyBytes))

	// 保存到密钥管理器
	if d.config.SecretsManager != nil {
		if err := d.config.SecretsManager.SetSecret("bls", blsKeyBytes); err != nil {
			d.logger.Warn("保存BLS公钥到密钥管理器失败", "error", err)
		}
	}

	return blsKeyBytes, nil
}

// generateBLSKey 生成新的BLS公钥
func (d *DPoS) generateBLSKey() (*bls.PublicKey, error) {
	// 使用当前节点的地址作为种子生成私钥
	addressBytes := d.key.Address().Bytes()
	seedHash := crypto.Keccak256(addressBytes)

	// 将哈希转换为大整数作为私钥
	privateKeyInt := new(big.Int).SetBytes(seedHash)

	// 使用bn256的阶数作为模数
	modulus, _ := new(big.Int).SetString("21888242871839275222246405745257275088548364400416034343698204186575808495617", 10)
	privateKeyInt.Mod(privateKeyInt, modulus)

	// 确保私钥不为零
	if isZeroOrNil(privateKeyInt) {
		privateKeyInt.Set(big.NewInt(1))
	}

	// 将私钥转换为字节并解析
	privateKeyBytes := privateKeyInt.Bytes()
	privateKey, err := bls.UnmarshalPrivateKey(privateKeyBytes)
	if err != nil {
		return nil, fmt.Errorf("failed to create BLS private key: %w", err)
	}

	// 获取公钥
	publicKey := privateKey.PublicKey()

	d.logger.Info("生成新的BLS密钥对", "address", d.key.Address().String())
	return publicKey, nil
}

// shouldParticipateInBLSSigning 判断是否应该参与BLS签名
func (d *DPoS) shouldParticipateInBLSSigning(delegate *validator.ValidatorMetadata) bool {
	return delegate.IsActive && isPositive(delegate.VotingPower)
}
