@echo off
REM ============================================================================
REM  install-client.bat - SntpClient 客户端静默部署（Windows，需管理员）
REM
REM  用法（以管理员身份运行）:
REM    install-client.bat
REM    install-client.bat /SERVER=192.168.1.10:123 /INTERVAL=15分 /STEP=5秒 /WEB=8090
REM
REM  参数（与安装器 /S 静默参数一致，写进配置文件而不是服务启动命令）:
REM    /SERVER=   校时目标服务端地址（默认 127.0.0.1:123）
REM    /INTERVAL= 校时间隔（默认 15分）
REM    /STEP=     校时阈值：单次允许的最大跳变（默认 5秒）
REM    /WEB=      本地 Web 管理端口（默认 8090，置空不启用）
REM    /AUTOSTART= 1=注册服务并开机自启（默认）  0=只复制文件不装服务
REM
REM  说明: 服务名 SntpClient；程序装在 %ProgramFiles%\SntpClient；
REM        配置写在 %ProgramData%\ntpsync\ntpsync.json，装完在 Web 页面里改，重启不丢。
REM ============================================================================
setlocal EnableExtensions

set "SERVER=127.0.0.1:123"
set "INTERVAL=15分"
set "STEP=5秒"
set "WEB=8090"
set "AUTOSTART=1"

REM 兼容 /KEY=VALUE 与 /KEY VALUE 两种写法
:parse
if "%~1"=="" goto done
if /i "%~1"=="/SERVER"    ( set "SERVER=%~2"   & shift & shift & goto parse )
if /i "%~1"=="/INTERVAL"  ( set "INTERVAL=%~2" & shift & shift & goto parse )
if /i "%~1"=="/STEP"      ( set "STEP=%~2"     & shift & shift & goto parse )
if /i "%~1"=="/WEB"       ( set "WEB=%~2"      & shift & shift & goto parse )
if /i "%~1"=="/AUTOSTART" ( set "AUTOSTART=%~2" & shift & shift & goto parse )
for /f "tokens=1,* delims==" %%a in ("%~1") do (
  if /i "%%a"=="/SERVER"    set "SERVER=%%b"
  if /i "%%a"=="/INTERVAL"  set "INTERVAL=%%b"
  if /i "%%a"=="/STEP"      set "STEP=%%b"
  if /i "%%a"=="/WEB"       set "WEB=%%b"
  if /i "%%a"=="/AUTOSTART" set "AUTOSTART=%%b"
)
shift
goto parse
:done

REM ---------- 定位客户端程序 ----------
REM 发布产物名为 SntpClient-v<版本>-windows-<架构>.exe，这里用通配匹配任意版本，
REM 免得每次发版都回头改脚本。
set "BIN_SRC="
if exist "%~dp0SntpClient.exe" set "BIN_SRC=%~dp0SntpClient.exe"
if not defined BIN_SRC for %%f in ("%~dp0..\dist\SntpClient-v*-windows-amd64.exe") do if exist "%%~f" set "BIN_SRC=%%~f"
if not defined BIN_SRC for %%f in ("%~dp0..\dist\SntpClient-v*-windows-arm64.exe") do if exist "%%~f" set "BIN_SRC=%%~f"
if not defined BIN_SRC for %%f in ("%~dp0SntpClient-v*-windows-amd64.exe") do if exist "%%~f" set "BIN_SRC=%%~f"
if not defined BIN_SRC for %%f in ("%~dp0SntpClient-v*-windows-arm64.exe") do if exist "%%~f" set "BIN_SRC=%%~f"
if not defined BIN_SRC (
  echo [错误] 未找到 SntpClient 程序。
  echo        请把 SntpClient.exe 放在本脚本同目录，或先运行 scripts\build.sh 产出 dist\SntpClient-v*-windows-*.exe
  exit /b 1
)

set "INSTALL_DIR=%ProgramFiles%\SntpClient"
if not exist "%INSTALL_DIR%" mkdir "%INSTALL_DIR%"
copy /Y "%BIN_SRC%" "%INSTALL_DIR%\SntpClient.exe" >nul
if not exist "%INSTALL_DIR%\SntpClient.exe" (
  echo [错误] 复制到 %INSTALL_DIR% 失败，请以管理员身份运行本脚本。
  exit /b 1
)

if not "%AUTOSTART%"=="1" (
  echo [完成] 仅复制程序，未注册服务（AUTOSTART=0）。
  echo   手动校时: "%INSTALL_DIR%\SntpClient.exe" sync -s %SERVER%
  exit /b 0
)

REM ---------- 注册系统服务 ----------
REM 调程序自己的 install 子命令：参数写进配置文件、启动命令恒为 run。
set "INST_ARGS=install -s %SERVER% -interval %INTERVAL% -step %STEP%"
if not "%WEB%"=="" set "INST_ARGS=%INST_ARGS% -web 127.0.0.1:%WEB%"

echo [1/1] 注册 Windows 服务 SntpClient ...
"%INSTALL_DIR%\SntpClient.exe" %INST_ARGS%
if errorlevel 1 (
  echo [错误] 服务注册失败，请确认以管理员身份运行。
  exit /b 1
)

echo.
echo [完成] SntpClient 已部署。
echo   服务名:   SntpClient      ^(查看状态: sc query SntpClient^)
echo   校时目标: %SERVER%
echo   间隔/阈值: %INTERVAL% / %STEP%
if not "%WEB%"=="" echo   Web 管理: http://127.0.0.1:%WEB%
echo   配置文件: %ProgramData%\ntpsync\ntpsync.json
exit /b 0
