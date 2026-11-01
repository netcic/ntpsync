@echo off
REM ============================================================================
REM  uninstall-client.bat - SntpClient 一键卸载（Windows，需管理员）
REM
REM  清理（只清本程序自己的东西）:
REM    · 系统服务 SntpClient，以及 1.0.x 的 ntpsync
REM    · 程序目录 %ProgramFiles%\SntpClient
REM    · 防火墙放行：Web 端口 TCP（客户端不监听授时端口，不碰 UDP 123）
REM    · 开始菜单：SntpClient.url 与 1.10.x 及以前的 SntpClient\ 文件夹
REM    · 注册表：Software\SntpClient、控制面板卸载入口（32/64 视图）、旧自启动项
REM    · 配置文件 %ProgramData%\ntpsync\ntpsync.json（含 .tmp）
REM  保留: 同机服务端的配置（ntpsyncd.json）——目录空了才会被删掉
REM
REM  核查残留: 同目录下的 verify-uninstall.ps1（见 DEPLOY.md 第十一节）
REM ============================================================================
setlocal EnableExtensions

echo [1/6] 停止并删除 Windows 服务 SntpClient ...
if exist "%~dp0SntpClient.exe" "%~dp0SntpClient.exe" uninstall >nul 2>&1
sc stop SntpClient >nul 2>&1
sc delete SntpClient >nul 2>&1
sc stop ntpsync >nul 2>&1
sc delete ntpsync >nul 2>&1
taskkill /f /im SntpClient.exe >nul 2>&1
taskkill /f /im ntpsync.exe >nul 2>&1

echo [2/6] 删除程序目录 %ProgramFiles%\SntpClient ...
if exist "%ProgramFiles%\SntpClient\" rd /s /q "%ProgramFiles%\SntpClient" >nul 2>&1

echo [3/6] 清理防火墙规则（仅 Web 端口）...
netsh advfirewall firewall delete rule name="NTP 授时服务 Web (TCP 8090)" >nul 2>&1
netsh advfirewall firewall delete rule name="NTP Web 管理 8090" >nul 2>&1

echo [4/6] 清理开始菜单项 ...
if exist "%ProgramData%\Microsoft\Windows\Start Menu\Programs\SntpClient.url" del /f /q "%ProgramData%\Microsoft\Windows\Start Menu\Programs\SntpClient.url" >nul 2>&1
if exist "%ProgramData%\Microsoft\Windows\Start Menu\Programs\SntpClient\" rd /s /q "%ProgramData%\Microsoft\Windows\Start Menu\Programs\SntpClient" >nul 2>&1

echo [5/6] 清理注册表条目 ...
reg delete "HKLM\Software\SntpClient" /f >nul 2>&1
reg delete "HKLM\Software\Microsoft\Windows\CurrentVersion\Uninstall\SntpClient" /f >nul 2>&1
reg delete "HKLM\Software\WOW6432Node\SntpClient" /f >nul 2>&1
reg delete "HKLM\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\SntpClient" /f >nul 2>&1
reg delete "HKLM\Software\Microsoft\Windows\CurrentVersion\Run" /v "NTP校时客户端" /f >nul 2>&1

echo [6/6] 删除配置（%ProgramData%\ntpsync\ntpsync.json）...
del /f /q "%ProgramData%\ntpsync\ntpsync.json" >nul 2>&1
del /f /q "%ProgramData%\ntpsync\ntpsync.json.tmp" >nul 2>&1
REM rd 不带 /s：目录非空（还有服务端配置）时自然失败，正好把对方的数据留下
rd "%ProgramData%\ntpsync" >nul 2>&1

echo.
echo [完成] SntpClient 已卸载。
exit /b 0
