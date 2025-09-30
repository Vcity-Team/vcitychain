@echo off
chcp 65001 >nul
setlocal enabledelayedexpansion
echo 清理nodes目录下的节点数据...
echo.

REM 检查是否提供了路径参数
if "%~1"=="" (
    REM 如果没有提供参数，使用当前目录下的nodes
    set NODES_DIR=%~dp0nodes
    echo 使用默认路径: %NODES_DIR%
) else (
    REM 使用提供的路径
    set NODES_DIR=%~1
    echo 使用指定路径: %NODES_DIR%
)

echo.

REM 检查nodes目录是否存在
if not exist "%NODES_DIR%" (
    echo 错误: %NODES_DIR% 目录不存在！
    echo 请检查路径是否正确，或提供正确的路径作为参数
    echo 例如: cleanup_nodes_flexible.bat C:\path\to\nodes
    pause
    exit /b 1
)

REM 显示将要清理的目录
echo 将要清理的目录: %NODES_DIR%
echo 按任意键继续，或按Ctrl+C取消...
pause >nul

REM 计数器
set /a count=0

REM 循环处理node1到node99（可以根据需要调整范围）
for /L %%i in (1,1,99) do (
    set NODE_DIR=%NODES_DIR%\node%%i
    
    REM 检查node目录是否存在
    if exist "!NODE_DIR!" (
        echo 处理节点目录: !NODE_DIR!
        
        REM 删除blockchain目录
        if exist "!NODE_DIR!\blockchain" (
            echo   删除blockchain目录...
            rmdir /s /q "!NODE_DIR!\blockchain"
            if exist "!NODE_DIR!\blockchain" (
                echo   警告: blockchain目录删除失败
            ) else (
                echo   ✓ blockchain目录已删除
            )
        ) else (
            echo   blockchain目录不存在，跳过
        )
        
        REM 删除trie目录
        if exist "!NODE_DIR!\trie" (
            echo   删除trie目录...
            rmdir /s /q "!NODE_DIR!\trie"
            if exist "!NODE_DIR!\trie" (
                echo   警告: trie目录删除失败
            ) else (
                echo   ✓ trie目录已删除
            )
        ) else (
            echo   trie目录不存在，跳过
        )
        
        REM 删除consensus/dpos目录
        if exist "!NODE_DIR!\consensus\dpos" (
            echo   删除consensus\dpos目录...
            rmdir /s /q "!NODE_DIR!\consensus\dpos"
            if exist "!NODE_DIR!\consensus\dpos" (
                echo   警告: consensus\dpos目录删除失败
            ) else (
                echo   ✓ consensus\dpos目录已删除
            )
        ) else (
            echo   consensus\dpos目录不存在，跳过
        )
        
        REM 检查consensus目录是否为空，如果为空则删除
        if exist "!NODE_DIR!\consensus" (
            dir "!NODE_DIR!\consensus" /b >nul 2>&1
            if errorlevel 1 (
                echo   删除空的consensus目录...
                rmdir /s /q "!NODE_DIR!\consensus"
            )
        )
        
        set /a count+=1
        echo.
    )
)

echo.
echo 清理完成！
echo 共处理了 %count% 个节点目录
echo.

REM 显示清理后的目录结构
echo 当前nodes目录结构:
if exist "%NODES_DIR%" (
    dir "%NODES_DIR%" /b
) else (
    echo %NODES_DIR% 目录不存在
)

echo.
echo 按任意键退出...
pause >nul
