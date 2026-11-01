@echo off
REM ============================================================================
REM  uninstall-server.bat - SntpServer 一键卸载（Windows，需管理员）
REM
REM  清理（只清本程序自己的东西）:
REM    · 系统服务 SntpServer，以及 1.0.x 的 ntpsyncd / 1.11.x 的 Sntpserver
REM    · 程序目录 %ProgramFiles%\SntpServer
REM    · 防火墙放行：UDP 123、Web 端口 TCP（含历史默认端口）
REM    · 开始菜单：SntpServer.url 与 1.10.x 及以前的 SntpServer\ 文件夹
REM    · 注册表：Software\SntpServer、控制面板卸载入口（32/64 视图）、旧自启动项
REM    · 配置与授时统计：%ProgramData%\ntpsync 下属于服务端的两个文件（含 .tmp）
REM  保留: 同机客户端的配置（ntpsync.json）——目录空了才会被删掉
REM
REM  核查残留: 同目录下的 verify-uninstall.ps1（见 DEPLOY.md 第十一节）
REM ============================================================================
setlocal EnableExtensions

echo [1/6] 停止并删除 Windows 服务 SntpServer ...
REM 先让程序自己按配置里的端口撤销防火墙放行、删掉配置；程序文件损坏时下面的
REM sc / netsh / del 仍然能兜底，所以这一步失败不影响后续步骤。
if exist "%~dp0SntpServer.exe" "%~dp0SntpServer.exe" uninstall >nul 2>&1
sc stop SntpServer >nul 2>&1
sc delete SntpServer >nul 2>&1
sc stop ntpsyncd >nul 2>&1
sc delete ntpsyncd >nul 2>&1
taskkill /f /im SntpServer.exe >nul 2>&1
taskkill /f /im ntpsyncd.exe >nul 2>&1

echo [2/6] 删除程序目录 %ProgramFiles%\SntpServer ...
if exist "%ProgramFiles%\SntpServer\" rd /s /q "%ProgramFiles%\SntpServer" >nul 2>&1

echo [3/6] 清理防火墙规则 ...
netsh advfirewall firewall delete rule name="NTP 授时服务 (UDP 123)" >nul 2>&1
netsh advfirewall firewall delete rule name="NTP 授时 123" >nul 2>&1
netsh advfirewall firewall delete rule name="NTP 授时服务 Web (TCP 8080)" >nul 2>&1
netsh advfirewall firewall delete rule name="NTP Web 管理 8080" >nul 2>&1
netsh advfirewall firewall delete rule name="NTP 授时服务 Web (TCP 123)" >nul 2>&1
netsh advfirewall firewall delete rule name="NTP Web 管理 123" >nul 2>&1

echo [4/6] 清理开始菜单项 ...
if exist "%ProgramData%\Microsoft\Windows\Start Menu\Programs\SntpServer.url" del /f /q "%ProgramData%\Microsoft\Windows\Start Menu\Programs\SntpServer.url" >nul 2>&1
if exist "%ProgramData%\Microsoft\Windows\Start Menu\Programs\SntpServer\" rd /s /q "%ProgramData%\Microsoft\Windows\Start Menu\Programs\SntpServer" >nul 2>&1

echo [5/6] 清理注册表条目 ...
reg delete "HKLM\Software\SntpServer" /f >nul 2>&1
reg delete "HKLM\Software\Microsoft\Windows\CurrentVersion\Uninstall\SntpServer" /f >nul 2>&1
reg delete "HKLM\Software\WOW6432Node\SntpServer" /f >nul 2>&1
reg delete "HKLM\Software\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\SntpServer" /f >nul 2>&1
reg delete "HKLM\Software\Microsoft\Windows\CurrentVersion\Run" /v "NTP授时服务" /f >nul 2>&1

echo [6/6] 删除配置与授时统计（%ProgramData%\ntpsync，只删服务端那两个文件）...
del /f /q "%ProgramData%\ntpsync\ntpsyncd.json" >nul 2>&1
del /f /q "%ProgramData%\ntpsync\ntpsyncd.json.tmp" >nul 2>&1
del /f /q "%ProgramData%\ntpsync\SntpServer-clients.json" >nul 2>&1
del /f /q "%ProgramData%\ntpsync\SntpServer-clients.json.tmp" >nul 2>&1
REM rd 不带 /s：目录非空（还有客户端配置）时自然失败，正好把对方的数据留下
rd "%ProgramData%\ntpsync" >nul 2>&1

echo.
echo [完成] SntpServer 已卸载。
exit /b 0
