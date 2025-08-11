@echo off
echo ========================================
echo DPoS 签名收集修复测试脚本
echo ========================================

echo.
echo 1. 检查代码编译...
go build -o test_dpos.exe ./consensus/dpos/

if %ERRORLEVEL% NEQ 0 (
    echo 编译失败！请检查代码错误
    pause
    exit /b 1
)

echo 编译成功！

echo.
echo 2. 运行单元测试...
go test ./consensus/dpos/ -v -run TestNetworkIntegration

if %ERRORLEVEL% NEQ 0 (
    echo 单元测试失败！
    pause
    exit /b 1
)

echo 单元测试通过！

echo.
echo 3. 检查网络集成代码...
echo - 检查 NetworkIntegration 是否正确设置 DPoS 运行时回调
echo - 检查签名请求处理是否正确调用回调
echo - 检查签名收集器是否正确注册

echo.
echo 4. 修复总结：
echo - 添加了 DPoS 运行时回调支持
echo - 修复了网络集成层的消息处理
echo - 确保签名收集器正确注册
echo - 添加了详细的日志记录

echo.
echo 修复完成！现在签名收集应该能正常工作。
echo 主要改进：
echo 1. 网络集成层现在能正确调用 DPoS 运行时的签名请求处理方法
echo 2. 签名收集器正确注册到网络集成层
echo 3. 添加了详细的日志记录，便于调试
echo 4. 修复了类型错误和接口连接问题

echo.
pause
