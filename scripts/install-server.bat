@echo off
REM ============================================================================
REM  install-server.bat - SntpServer 服务端静默部署（Windows，需管理员）
REM
REM  用法（以管理员身份运行）:
REM    install-server.bat
REM    install-server.bat /LISTEN=0.0.0.0:123 /WEB=8080 /UPSTREAM=ntp.aliyun.com /UPSTREAM-INTERVAL=1小时
REM
REM  参数（与安装器 /S 静默参数一致，写进配置文件而不是服务启动命令）:
REM    /LISTEN=            监听地址（默认 0.0.0.0:123）
REM    /WEB=               Web 界面端口（默认 8080，只对局域网开放），置空不启用
REM    /UPSTREAM=          上游 NTP 时间源（留空则用本机硬件时钟）
REM    /UPSTREAM-INTERVAL= 上游同步间隔（默认 1小时）
REM    /AUTOSTART=         1=注册服务并开机自启（默认）  0=只复制文件不装服务
REM
REM  说明: 服务名 SntpServer；程序装在 %ProgramFiles%\SntpServer；
REM        配置写在 %ProgramData%\ntpsync\ntpsyncd.json，装完在 Web 页面里改，重启不丢。
REM        Web 界面只对局域网开放，地址 http://本机IP:8080（Web 端口独立于授时端口）。
REM ============================================================================
setlocal EnableExtensions

set "LISTEN=0.0.0.0:123"
set "WEB=8080"
set "UPSTREAM="
set "UPSTREAM_INTERVAL=1小时"
set "AUTOSTART=1"

REM 兼容 /KEY=VALUE 与 /KEY VALUE 两种写法
:parse
if "%~1"=="" goto done
if /i "%~1"=="/LISTEN"    ( set "LISTEN=%~2"   & shift & shift & goto parse )
if /i "%~1"=="/WEB"       ( set "WEB=%~2"      & shift & shift & goto parse )
if /i "%~1"=="/UPSTREAM"  ( set "UPSTREAM=%~2" & shift & shift & goto parse )
if /i "%~1"=="/UPSTREAM-INTERVAL" ( set "UPSTREAM_INTERVAL=%~2" & shift & shift & goto parse )
if /i "%~1"=="/AUTOSTART" ( set "AUTOSTART=%~2" & shift & shift & goto parse )
for /f "tokens=1,* delims==" %%a in ("%~1") do (
  if /i "%%a"=="/LISTEN"            set "LISTEN=%%b"
  if /i "%%a"=="/WEB"               set "WEB=%%b"
  if /i "%%a"=="/UPSTREAM"          set "UPSTREAM=%%b"
  if /i "%%a"=="/UPSTREAM-INTERVAL" set "UPSTREAM_INTERVAL=%%b"
  if /i "%%a"=="/AUTOSTART"         set "AUTOSTART=%%b"
)
shift
goto parse
:done

REM ---------- 定位服务端程序 ----------
REM 发布产物名为 SntpServer-v<版本>-windows-<架构>.exe，这里用通配匹配任意版本，
REM 免得每次发版都回头改脚本。
set "BIN_SRC="
if exist "%~dp0SntpServer.exe" set "BIN_SRC=%~dp0SntpServer.exe"
if not defined BIN_SRC for %%f in ("%~dp0..\dist\SntpServer-v*-windows-amd64.exe") do if exist "%%~f" set "BIN_SRC=%%~f"
if not defined BIN_SRC for %%f in ("%~dp0..\dist\SntpServer-v*-windows-arm64.exe") do if exist "%%~f" set "BIN_SRC=%%~f"
if not defined BIN_SRC for %%f in ("%~dp0SntpServer-v*-windows-amd64.exe") do if exist "%%~f" set "BIN_SRC=%%~f"
if not defined BIN_SRC for %%f in ("%~dp0SntpServer-v*-windows-arm64.exe") do if exist "%%~f" set "BIN_SRC=%%~f"
if not defined BIN_SRC (
  echo [错误] 未找到 SntpServer 程序。
  echo        请把 SntpServer.exe 放在本脚本同目录，或先运行 scripts\build.sh 产出 dist\SntpServer-v*-windows-*.exe
  exit /b 1
)

set "INSTALL_DIR=%ProgramFiles%\SntpServer"
if not exist "%INSTALL_DIR%" mkdir "%INSTALL_DIR%"
copy /Y "%BIN_SRC%" "%INSTALL_DIR%\SntpServer.exe" >nul
if not exist "%INSTALL_DIR%\SntpServer.exe" (
  echo [错误] 复制到 %INSTALL_DIR% 失败，请以管理员身份运行本脚本。
  exit /b 1
)

if not "%AUTOSTART%"=="1" (
  echo [完成] 仅复制程序，未注册服务（AUTOSTART=0）。
  echo   手动启动: "%INSTALL_DIR%\SntpServer.exe" run -l %LISTEN%
  exit /b 0
)

REM ---------- 注册系统服务 ----------
REM 调程序自己的 install 子命令：参数写进配置文件、启动命令恒为 run，
REM 因此之后在 Web 里改的参数重启后依然生效（不会启动时被命令行覆盖）。
set "INST_ARGS=install -l %LISTEN%"
if not "%UPSTREAM%"=="" set "INST_ARGS=%INST_ARGS% -upstream %UPSTREAM% -upstream-interval %UPSTREAM_INTERVAL%"
REM Web 界面只对局域网开放；端口独立配置（默认 8080），只取端口号交给 -web
set "WEB_PORT=%WEB:*:=%"
if not "%WEB%"=="" set "INST_ARGS=%INST_ARGS% -web %WEB_PORT%"

echo [1/2] 注册 Windows 服务 SntpServer ...
"%INSTALL_DIR%\SntpServer.exe" %INST_ARGS%
if errorlevel 1 (
  echo [错误] 服务注册失败，请确认以管理员身份运行。
  exit /b 1
)
echo [2/2] 防火墙 UDP 123 由程序安装时自动放行。

echo.
echo [完成] SntpServer 已部署。
echo   服务名:   SntpServer      ^(查看状态: sc query SntpServer^)
echo   监听地址: %LISTEN%
if not "%WEB%"=="" echo   Web 管理: http://本机IP:%WEB_PORT% （局域网可访问）
echo   配置文件: %ProgramData%\ntpsync\ntpsyncd.json
exit /b 0
