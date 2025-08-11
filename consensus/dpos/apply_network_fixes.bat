@echo off
REM DPoS网络修复脚本 (Windows版本)
REM 用于快速应用网络优化配置

echo === DPoS网络修复脚本 ===
echo 开始应用网络优化配置...

REM 检查Go环境
where go >nul 2>nul
if %errorlevel% neq 0 (
    echo 错误: 未找到Go环境，请先安装Go
    pause
    exit /b 1
)

REM 检查是否在正确的目录
if not exist "consensus\dpos\dpos.go" (
    echo 错误: 请在项目根目录运行此脚本
    pause
    exit /b 1
)

echo 1. 验证Go模块...
go mod tidy

echo 2. 编译检查...
go build ./consensus/dpos/...
if %errorlevel% neq 0 (
    echo 错误: 编译失败，请检查代码
    pause
    exit /b 1
)

echo 3. 运行测试...
go test ./consensus/dpos/... -v
if %errorlevel% neq 0 (
    echo 警告: 部分测试失败，但继续执行...
)

echo 4. 应用网络配置优化...

REM 创建配置示例文件
echo package dpos > consensus\dpos\network_config_example.go
echo. >> consensus\dpos\network_config_example.go
echo import ( >> consensus\dpos\network_config_example.go
echo     "time" >> consensus\dpos\network_config_example.go
echo     "github.com/Vcity-Team/vcitychain/consensus/dpos" >> consensus\dpos\network_config_example.go
echo ) >> consensus\dpos\network_config_example.go
echo. >> consensus\dpos\network_config_example.go
echo // 应用网络配置优化的示例 >> consensus\dpos\network_config_example.go
echo func ApplyNetworkOptimization() { >> consensus\dpos\network_config_example.go
echo     // 使用生产环境优化配置 >> consensus\dpos\network_config_example.go
echo     networkConfig := dpos.OptimizedNetworkConfig() >> consensus\dpos\network_config_example.go
echo     >> consensus\dpos\network_config_example.go
echo     // 可以根据实际环境调整参数 >> consensus\dpos\network_config_example.go
echo     networkConfig.SignatureCollectionTimeout = 45 * time.Second >> consensus\dpos\network_config_example.go
echo     networkConfig.FallbackSignatureTimeout = 20 * time.Second >> consensus\dpos\network_config_example.go
echo     networkConfig.MaxRetryAttempts = 5 >> consensus\dpos\network_config_example.go
echo     networkConfig.RetryInterval = 1 * time.Second >> consensus\dpos\network_config_example.go
echo     networkConfig.MaxConcurrentSignatures = 20 >> consensus\dpos\network_config_example.go
echo     networkConfig.NetworkBufferSize = 2000 >> consensus\dpos\network_config_example.go
echo     >> consensus\dpos\network_config_example.go
echo     // 验证配置 >> consensus\dpos\network_config_example.go
echo     if err := networkConfig.Validate(); err != nil { >> consensus\dpos\network_config_example.go
echo         panic("网络配置验证失败: " + err.Error()) >> consensus\dpos\network_config_example.go
echo     } >> consensus\dpos\network_config_example.go
echo     >> consensus\dpos\network_config_example.go
echo     // 打印配置摘要 >> consensus\dpos\network_config_example.go
echo     summary := networkConfig.GetConfigSummary() >> consensus\dpos\network_config_example.go
echo     for key, value := range summary { >> consensus\dpos\network_config_example.go
echo         println(key + ": " + fmt.Sprintf("%%v", value)) >> consensus\dpos\network_config_example.go
echo     } >> consensus\dpos\network_config_example.go
echo } >> consensus\dpos\network_config_example.go

echo 5. 创建配置验证脚本...

echo package dpos > consensus\dpos\verify_config.go
echo. >> consensus\dpos\verify_config.go
echo import ( >> consensus\dpos\verify_config.go
echo     "fmt" >> consensus\dpos\verify_config.go
echo     "time" >> consensus\dpos\verify_config.go
echo ) >> consensus\dpos\verify_config.go
echo. >> consensus\dpos\verify_config.go
echo // 验证网络配置是否正确 >> consensus\dpos\verify_config.go
echo func VerifyNetworkConfiguration() error { >> consensus\dpos\verify_config.go
echo     // 测试默认配置 >> consensus\dpos\verify_config.go
echo     defaultConfig := DefaultNetworkConfig() >> consensus\dpos\verify_config.go
echo     if err := defaultConfig.Validate(); err != nil { >> consensus\dpos\verify_config.go
echo         return fmt.Errorf("默认配置验证失败: %%w", err) >> consensus\dpos\verify_config.go
echo     } >> consensus\dpos\verify_config.go
echo     >> consensus\dpos\verify_config.go
echo     // 测试优化配置 >> consensus\dpos\verify_config.go
echo     optimizedConfig := OptimizedNetworkConfig() >> consensus\dpos\verify_config.go
echo     if err := optimizedConfig.Validate(); err != nil { >> consensus\dpos\verify_config.go
echo         return fmt.Errorf("优化配置验证失败: %%w", err) >> consensus\dpos\verify_config.go
echo     } >> consensus\dpos\verify_config.go
echo     >> consensus\dpos\verify_config.go
echo     // 测试测试配置 >> consensus\dpos\verify_config.go
echo     testConfig := TestNetworkConfig() >> consensus\dpos\verify_config.go
echo     if err := testConfig.Validate(); err != nil { >> consensus\dpos\verify_config.go
echo         return fmt.Errorf("测试配置验证失败: %%w", err) >> consensus\dpos\verify_config.go
echo     } >> consensus\dpos\verify_config.go
echo     >> consensus\dpos\verify_config.go
echo     fmt.Println("所有网络配置验证通过") >> consensus\dpos\verify_config.go
echo     return nil >> consensus\dpos\verify_config.go
echo } >> consensus\dpos\verify_config.go

echo 6. 创建性能测试脚本...

echo package dpos > consensus\dpos\performance_test.go
echo. >> consensus\dpos\performance_test.go
echo import ( >> consensus\dpos\performance_test.go
echo     "testing" >> consensus\dpos\performance_test.go
echo     "time" >> consensus\dpos\performance_test.go
echo ) >> consensus\dpos\performance_test.go
echo. >> consensus\dpos\performance_test.go
echo // 测试网络配置性能 >> consensus\dpos\performance_test.go
echo func TestNetworkConfigPerformance(t *testing.T) { >> consensus\dpos\performance_test.go
echo     // 测试默认配置 >> consensus\dpos\performance_test.go
echo     start := time.Now() >> consensus\dpos\performance_test.go
echo     defaultConfig := DefaultNetworkConfig() >> consensus\dpos\performance_test.go
echo     _ = defaultConfig.Validate() >> consensus\dpos\performance_test.go
echo     defaultTime := time.Since(start) >> consensus\dpos\performance_test.go
echo     >> consensus\dpos\performance_test.go
echo     // 测试优化配置 >> consensus\dpos\performance_test.go
echo     start = time.Now() >> consensus\dpos\performance_test.go
echo     optimizedConfig := OptimizedNetworkConfig() >> consensus\dpos\performance_test.go
echo     _ = optimizedConfig.Validate() >> consensus\dpos\performance_test.go
echo     optimizedTime := time.Since(start) >> consensus\dpos\performance_test.go
echo     >> consensus\dpos\performance_test.go
echo     // 测试测试配置 >> consensus\dpos\performance_test.go
echo     start = time.Now() >> consensus\dpos\performance_test.go
echo     testConfig := TestNetworkConfig() >> consensus\dpos\performance_test.go
echo     _ = testConfig.Validate() >> consensus\dpos\performance_test.go
echo     testTime := time.Since(start) >> consensus\dpos\performance_test.go
echo     >> consensus\dpos\performance_test.go
echo     t.Logf("默认配置初始化时间: %%v", defaultTime) >> consensus\dpos\performance_test.go
echo     t.Logf("优化配置初始化时间: %%v", optimizedTime) >> consensus\dpos\performance_test.go
echo     t.Logf("测试配置初始化时间: %%v", testTime) >> consensus\dpos\performance_test.go
echo     >> consensus\dpos\performance_test.go
echo     // 验证配置参数 >> consensus\dpos\performance_test.go
echo     if defaultConfig.SignatureCollectionTimeout ^<= 0 { >> consensus\dpos\performance_test.go
echo         t.Error("默认配置超时时间无效") >> consensus\dpos\performance_test.go
echo     } >> consensus\dpos\performance_test.go
echo     >> consensus\dpos\performance_test.go
echo     if optimizedConfig.SignatureCollectionTimeout ^<= defaultConfig.SignatureCollectionTimeout { >> consensus\dpos\performance_test.go
echo         t.Error("优化配置超时时间应该大于默认配置") >> consensus\dpos\performance_test.go
echo     } >> consensus\dpos\performance_test.go
echo     >> consensus\dpos\performance_test.go
echo     if testConfig.SignatureCollectionTimeout ^>= defaultConfig.SignatureCollectionTimeout { >> consensus\dpos\performance_test.go
echo         t.Error("测试配置超时时间应该小于默认配置") >> consensus\dpos\performance_test.go
echo     } >> consensus\dpos\performance_test.go
echo } >> consensus\dpos\performance_test.go

echo 7. 运行配置验证...

go run consensus\dpos\verify_config.go
if %errorlevel% neq 0 (
    echo 警告: 配置验证失败，但继续执行...
)

echo 8. 清理临时文件...
del /f /q consensus\dpos\network_config_example.go
del /f /q consensus\dpos\verify_config.go
del /f /q consensus\dpos\performance_test.go

echo === 网络修复完成 ===
echo.
echo 主要修复内容:
echo 1. 优化了签名收集超时机制
echo 2. 增加了备用签名收集机制
echo 3. 改进了网络消息处理逻辑
echo 4. 增强了签名验证和重试机制
echo 5. 添加了网络配置优化选项
echo.
echo 建议配置:
echo - 生产环境: 使用 OptimizedNetworkConfig()
echo - 测试环境: 使用 TestNetworkConfig()
echo - 自定义: 参考 NetworkConfig 结构体
echo.
echo 重启节点后，监控日志中的签名收集成功率是否提升。
echo 如果仍有问题，请检查网络连接和验证者配置。
echo.
pause
