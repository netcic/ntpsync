@echo off
REM ============================================================================
REM  build-installers.bat - 生成 Windows 独立安装器（NSIS）
REM
REM  用法（在项目根目录运行）:
REM    scripts\build-installers.bat              # 用默认版本 1.10.28
REM    scripts\build-installers.bat 1.10.29      # 指定版本号
REM
REM  前置: 已运行 scripts\build.sh 产出 dist\*-v<ver>-windows-*.exe
REM  依赖: 已安装 NSIS（makensis 在 PATH 或 scoop shims）
REM
REM  产出（dist\）:
REM    Sntpserver-Setup-v<ver>-amd64.exe   服务端 x64 安装器
REM    Sntpserver-Setup-v<ver>-arm64.exe   服务端 ARM64 安装器
REM    SntpClient-Setup-v<ver>-amd64.exe   客户端 x64 安装器
REM    SntpClient-Setup-v<ver>-arm64.exe   客户端 ARM64 安装器
REM ============================================================================
setlocal EnableExtensions

set "VERSION=%~1"
if "%VERSION%"=="" set "VERSION=1.10.28"

set "NSI=%~dp0..\packaging\win\ntp-installer.nsi"

REM 检查 makensis
where makensis >nul 2>&1
if errorlevel 1 (
  echo [错误] 未找到 makensis，请先安装 NSIS：scoop install nsis
  exit /b 1
)

echo === 生成 Windows 安装器 版本=%VERSION% ===

echo [1/4] 服务端 amd64 ...
makensis -DROLE=server -DARCH=amd64 -DVERSION=%VERSION% "%NSI%" >nul
if errorlevel 1 ( echo   失败 & exit /b 1 )

echo [2/4] 服务端 arm64 ...
makensis -DROLE=server -DARCH=arm64 -DVERSION=%VERSION% "%NSI%" >nul
if errorlevel 1 ( echo   失败 & exit /b 1 )

echo [3/4] 客户端 amd64 ...
makensis -DROLE=client -DARCH=amd64 -DVERSION=%VERSION% "%NSI%" >nul
if errorlevel 1 ( echo   失败 & exit /b 1 )

echo [4/4] 客户端 arm64 ...
makensis -DROLE=client -DARCH=arm64 -DVERSION=%VERSION% "%NSI%" >nul
if errorlevel 1 ( echo   失败 & exit /b 1 )

echo.
echo === 安装器生成完成 ===
dir /b dist\*Setup*.exe 2>nul
exit /b 0
