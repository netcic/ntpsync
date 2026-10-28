@echo off
REM ============================================================================
REM  uninstall-client.bat - SntpClient 一键卸载（Windows，需管理员）
REM
REM  清理: 系统服务 SntpClient、程序目录 %ProgramFiles%\SntpClient
REM  保留: %ProgramData%\ntpsync 下的配置与日志（重装后可直接沿用）
REM ============================================================================
setlocal EnableExtensions

echo [1/2] 停止并删除 Windows 服务 SntpClient ...
sc stop SntpClient >nul 2>&1
sc delete SntpClient >nul 2>&1
if errorlevel 1 echo   提示: 服务可能不存在或已删除，继续...

echo [2/2] 删除程序目录 %ProgramFiles%\SntpClient ...
if exist "%ProgramFiles%\SntpClient\" rd /s /q "%ProgramFiles%\SntpClient" >nul 2>&1

reg delete "HKLM\Software\Microsoft\Windows\CurrentVersion\Run" /v "NTP校时客户端" /f >nul 2>&1

echo.
echo [完成] SntpClient 已卸载。配置与日志保留在 %ProgramData%\ntpsync。
exit /b 0
