@echo off
REM ============================================================================
REM  uninstall-server.bat - Sntpserver 一键卸载（Windows，需管理员）
REM
REM  清理: 系统服务 Sntpserver、程序目录 %ProgramFiles%\Sntpserver、防火墙规则
REM  保留: %ProgramData%\ntpsync 下的配置与日志（重装后可直接沿用）
REM ============================================================================
setlocal EnableExtensions

echo [1/3] 停止并删除 Windows 服务 Sntpserver ...
sc stop Sntpserver >nul 2>&1
sc delete Sntpserver >nul 2>&1
if errorlevel 1 echo   提示: 服务可能不存在或已删除，继续...

echo [2/3] 删除程序目录 %ProgramFiles%\Sntpserver ...
if exist "%ProgramFiles%\Sntpserver\" rd /s /q "%ProgramFiles%\Sntpserver" >nul 2>&1

echo [3/3] 清理防火墙规则与开机启动项 ...
netsh advfirewall firewall delete rule name="NTP 授时服务 (UDP 123)" >nul 2>&1
netsh advfirewall firewall delete rule name="NTP 授时服务 Web (TCP 8080)" >nul 2>&1
reg delete "HKLM\Software\Microsoft\Windows\CurrentVersion\Run" /v "NTP授时服务" /f >nul 2>&1

echo.
echo [完成] Sntpserver 已卸载。配置与日志保留在 %ProgramData%\ntpsync。
exit /b 0
