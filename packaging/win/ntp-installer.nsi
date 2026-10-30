; ============================================================================
;  ntp-installer.nsi — 局域网 NTP 授时系统 Windows 安装器模板
;
;  通过 -D 宏区分「服务端 / 客户端」角色，编译时传入：
;    makensis -DROLE=server -DARCH=amd64 -DVERSION=1.10.28 ntp-installer.nsi
;    makensis -DROLE=client -DARCH=amd64 -DVERSION=1.10.28 ntp-installer.nsi
;
;  产物命名：<产品名>-Setup-v<版本>-<架构>.exe
;    Sntpserver-Setup-v1.10.28-amd64.exe / SntpClient-Setup-v1.10.28-amd64.exe
;
;  设计要点（2026-10 改版）：
;    1. 向导只留四步：欢迎 → 安装目录 → 安装进度 → 完成。
;       删掉了「运行参数」配置页——参数统一在装完后的 Web 管理界面里配，
;       安装器只负责把程序和默认配置落地，普通用户点四次「下一步」就能装完。
;    2. 服务注册不再自己调 sc create，而是调程序自带的 `install` 子命令：
;       由程序决定「写配置文件还是塞启动参数」，避免安装器与程序各说一套。
;       这样安装参数不会被固化进服务启动命令，用户在 Web 里改完重启后依然生效。
;    3. 已有配置文件且本次没给任何静默参数（典型场景：覆盖升级）时自动加
;       -preserve，保留用户在 Web 里配过的监听地址/上游/校时目标等。
;    4. 静默安装参数与旧版完全兼容：
;         服务端 /S /LISTEN= /UPSTREAM= /UPSTREAM-INTERVAL= /WEB=
;         客户端 /S /SERVER= /INTERVAL= /STEP= /WEB=
; ============================================================================

; ---- 编译参数（默认值，可由 -D 覆盖）----
!ifndef ROLE
  !define ROLE "server"
!endif
!ifndef ARCH
  !define ARCH "amd64"
!endif
!ifndef VERSION
  !define VERSION "1.10.28"
!endif

; ---- 产品元信息（按角色区分）----
!if ${ROLE} == "server"
  !define PRODUCT_NAME      "Sntpserver"
  !define PRODUCT_NAME_EN   "Sntpserver"
  !define LEGACY_PRODUCT_EN "NTPTimeServer"
  !define LEGACY_SERVICE    "ntpsyncd"
  !define BIN_NAME          "Sntpserver"
  !define SERVICE_NAME      "Sntpserver"
  !define SERVICE_DISPLAY   "Sntpserver"
  !define SERVICE_DESC      "以本机硬件时钟为权威时间源，向局域网设备提供 NTP 授时"
  !define DEFAULT_WEB       "8080"
  !define CFG_FILE          "ntpsyncd.json"
  !define WELCOME_TEXT      "本向导将在本机安装「Sntpserver」，为局域网内的设备提供标准 NTP 授时。$\r$\n$\r$\n  程序目录：默认 C:\Program Files\Sntpserver$\r$\n  系统服务：Sntpserver（开机自启，UDP 123）$\r$\n  Web 管理：http://127.0.0.1:${DEFAULT_WEB}$\r$\n$\r$\n安装完成后在 Web 管理页面配置上游时间源、监听地址等参数；\r$\n安装过程不需要联网，可随时在「添加/删除程序」中卸载。"
  !define FINISH_TEXT       "「${PRODUCT_NAME}」已安装并在后台运行。$\r$\n$\r$\n  服务名：Sntpserver（已设为开机自启）$\r$\n  Web 管理：http://127.0.0.1:${DEFAULT_WEB}$\r$\n$\r$\n下一步建议：打开 Web 管理页面确认监听地址与上游时间源，$\r$\n然后把本机的 IP:123 填到需要校时的客户端上。"
!else
  !define PRODUCT_NAME      "SntpClient"
  !define PRODUCT_NAME_EN   "SntpClient"
  !define LEGACY_PRODUCT_EN "NTPTimeClient"
  !define LEGACY_SERVICE    "ntpsync"
  !define BIN_NAME          "SntpClient"
  !define SERVICE_NAME      "SntpClient"
  !define SERVICE_DISPLAY   "SntpClient"
  !define SERVICE_DESC      "开机自启，周期性向局域网 NTP 服务端校准本机时钟"
  !define DEFAULT_WEB       "8090"
  !define CFG_FILE          "ntpsync.json"
  !define WELCOME_TEXT      "本向导将在本机安装「SntpClient」，开机后自动向局域网 NTP 服务端校准本机时钟。$\r$\n$\r$\n  程序目录：默认 C:\Program Files\SntpClient$\r$\n  系统服务：SntpClient（开机自启，周期校时）$\r$\n  Web 管理：http://127.0.0.1:${DEFAULT_WEB}$\r$\n$\r$\n安装完成后在 Web 管理页面填写服务端地址（如 192.168.1.10:123）；\r$\n安装过程不需要联网，可随时在「添加/删除程序」中卸载。"
  !define FINISH_TEXT       "「${PRODUCT_NAME}」已安装并在后台运行。$\r$\n$\r$\n  服务名：SntpClient（已设为开机自启）$\r$\n  Web 管理：http://127.0.0.1:${DEFAULT_WEB}$\r$\n$\r$\n下一步建议：打开 Web 管理页面填写服务端地址（如 192.168.1.10:123），$\r$\n保存后立即生效并写入配置文件，重启也不会丢。"
!endif

!ifndef ICON_FILE
  !define ICON_FILE "..\..\assets\app.ico"
!endif

; 卸载器文件名：两个产品自 1.10.28 起各装各的目录
; （%ProgramFiles%\Sntpserver 与 %ProgramFiles%\SntpClient），
; 不再共用同一个 uninstall.exe，也就不存在「后装的覆盖先装的、点客户端卸载却跑了
; 服务端脚本」的问题——那条历史 bug 的根由随目录分离一起消失。
!define UNINST_NAME "uninstall.exe"

; ============================================================================
;  基础设置
; ============================================================================
Name "${PRODUCT_NAME}"
OutFile "..\..\dist\${BIN_NAME}-Setup-v${VERSION}-${ARCH}.exe"
InstallDir "$PROGRAMFILES64\${BIN_NAME}"
InstallDirRegKey HKLM "Software\${PRODUCT_NAME_EN}" "InstallDir"
RequestExecutionLevel admin
Unicode true
SetCompressor /SOLID lzma
XPStyle on                 ; 使用系统主题绘制控件，Win10/11 下不再是 Win95 观感
BrandingText "${PRODUCT_NAME} v${VERSION}"
; 安装日志默认收起，需要排错时点「显示详细信息」展开——普通用户看到的是一条干净的进度条
ShowInstDetails hide
ShowUninstDetails hide

!include "MUI2.nsh"
!include "FileFunc.nsh"
!include "LogicLib.nsh"

; ---- 版本信息 ----
VIProductVersion "${VERSION}.0"
VIAddVersionKey /LANG=2052 "ProductName" "${PRODUCT_NAME}"
VIAddVersionKey /LANG=2052 "CompanyName" "局域网NTP授时系统"
VIAddVersionKey /LANG=2052 "FileDescription" "${PRODUCT_NAME} 安装程序"
VIAddVersionKey /LANG=2052 "FileVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "ProductVersion" "${VERSION}"
VIAddVersionKey /LANG=2052 "LegalCopyright" "局域网 NTP 授时系统"

; ---- 界面文案 ----
!define MUI_ABORTWARNING
!define MUI_UNABORTWARNING
!define MUI_ICON "${ICON_FILE}"
!define MUI_UNICON "${ICON_FILE}"

!define MUI_WELCOMEPAGE_TITLE "${PRODUCT_NAME}"
!define MUI_WELCOMEPAGE_TITLE_3LINES
!define MUI_WELCOMEPAGE_TEXT "${WELCOME_TEXT}"

!define MUI_DIRECTORYPAGE_TEXT_TOP "选择「${PRODUCT_NAME}」的安装位置。$\r$\n默认路径按 64 位系统选择，通常无需修改。"

!define MUI_FINISHPAGE_TITLE "安装完成"
!define MUI_FINISHPAGE_TITLE_3LINES
!define MUI_FINISHPAGE_TEXT "${FINISH_TEXT}"
!define MUI_FINISHPAGE_RUN
!define MUI_FINISHPAGE_RUN_TEXT "打开 Web 管理页面（推荐先在这里完成配置）"
!define MUI_FINISHPAGE_RUN_FUNCTION OpenWebUI
!define MUI_FINISHPAGE_NOREBOOTSUPPORT

!define MUI_UNCONFIRMPAGE_TEXT_TOP "即将卸载「${PRODUCT_NAME}」。$\r$\n$\r$\n  · 停止并删除系统服务 ${SERVICE_NAME}$\r$\n  · 删除程序文件、开始菜单项与防火墙规则$\r$\n  · 保留 %ProgramData% 目录下的 ntpsync 配置与日志，重装后可继续使用"

; ---- 向导页面（无参数配置页）----
!insertmacro MUI_PAGE_WELCOME
!insertmacro MUI_PAGE_DIRECTORY
!insertmacro MUI_PAGE_INSTFILES
!insertmacro MUI_PAGE_FINISH

!insertmacro MUI_UNPAGE_CONFIRM
!insertmacro MUI_UNPAGE_INSTFILES

!insertmacro MUI_LANGUAGE "SimpChinese"

; ============================================================================
;  运行时变量（仅由静默参数填充；GUI 模式下保持默认值）
; ============================================================================
Var ListenAddr
Var ServerAddr
Var WebPort
Var Interval
Var Step
Var Upstream
Var UpstreamInterval
Var CfgPath        ; 配置文件路径，用于判断是否需要 -preserve
Var Preserve       ; 1 = 保留已有配置（覆盖升级场景）
Var HadSilentArg   ; 1 = 命令行给过静默参数

; ============================================================================
;  完成页「打开 Web 管理页面」
; ============================================================================
Function OpenWebUI
  ExecShell "open" "http://127.0.0.1:$WebPort"
FunctionEnd

; ============================================================================
;  安装区段 1：程序文件 + 卸载信息 + 开始菜单
; ============================================================================
Section "主程序文件" SecMain
  SetShellVarContext all

  ; ---- 先停旧进程，否则 exe 被占用覆盖不了 ----
  DetailPrint "停止旧版服务与进程…"
  nsExec::ExecToLog 'sc stop ${SERVICE_NAME}'
  Sleep 1500
  nsExec::ExecToLog 'sc delete ${SERVICE_NAME}'
  nsExec::ExecToLog 'taskkill /F /IM ${BIN_NAME}.exe'
  Sleep 500

  ; ---- 清理 v1.0.x 遗留 ----
  ; 旧版本叫 ntpsyncd / ntpsync，装在 %ProgramFiles%\ntpsync。改名换目录后，
  ; 旧服务仍在开机自启并占着 UDP 123，新服务抢不到端口——表现就是「装完反而
  ; 授不上时」。这里只停旧服务、删旧服务注册，旧程序目录与它的「添加/删除程序」
  ; 入口都保留：用户想彻底清掉旧版，从控制面板卸一次即可。
  DetailPrint "清理旧版本服务 ${LEGACY_SERVICE}…"
  nsExec::ExecToLog 'sc stop ${LEGACY_SERVICE}'
  Sleep 800
  nsExec::ExecToLog 'sc delete ${LEGACY_SERVICE}'
  nsExec::ExecToLog 'taskkill /F /IM ${LEGACY_SERVICE}.exe'

  SetOutPath "$INSTDIR"
  DetailPrint "复制程序文件到 $INSTDIR"
  !if ${ARCH} == "arm64"
    File "/oname=${BIN_NAME}.exe" "..\..\dist\${BIN_NAME}-v${VERSION}-windows-arm64.exe"
  !else
    File "/oname=${BIN_NAME}.exe" "..\..\dist\${BIN_NAME}-v${VERSION}-windows-amd64.exe"
  !endif

  WriteRegStr HKLM "Software\${PRODUCT_NAME_EN}" "InstallDir" "$INSTDIR"
  WriteRegStr HKLM "Software\${PRODUCT_NAME_EN}" "Version" "${VERSION}"

  ; ---- 控制面板「添加/删除程序」 ----
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME_EN}" \
    "DisplayName" "${PRODUCT_NAME}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME_EN}" \
    "DisplayVersion" "${VERSION}"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME_EN}" \
    "DisplayIcon" "$INSTDIR\${BIN_NAME}.exe"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME_EN}" \
    "UninstallString" '"$INSTDIR\${UNINST_NAME}"'
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME_EN}" \
    "Publisher" "局域网NTP授时系统"
  WriteRegStr HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME_EN}" \
    "InstallLocation" "$INSTDIR"
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME_EN}" \
    "NoModify" 1
  WriteRegDWORD HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME_EN}" \
    "NoRepair" 1

  WriteUninstaller "$INSTDIR\${UNINST_NAME}"

  ; ---- 开始菜单：只放一个「产品名.url」快捷方式，直接落在开始菜单根目录（不再套一层
  ;      产品子文件夹）。Web 管理页面是唯一的配置入口，名字就用产品名 Sntpserver /
  ;      SntpClient，用户一眼能认出来。卸载统一走控制面板「添加/删除程序」，开始菜单
  ;      不再单独放「卸载」快捷方式，避免与产品名快捷方式并列造成混淆。 ----
  DetailPrint "创建开始菜单项…"
  WriteINIStr "$SMPROGRAMS\${PRODUCT_NAME}.url" "InternetShortcut" "URL" "http://127.0.0.1:$WebPort"
  ; .url 默认用浏览器图标，看不出是咱们的程序。IconFile/IconIndex 指向本机 exe
  ; （图标已内嵌在 exe 里），不额外复制 ico 到安装目录。
  WriteINIStr "$SMPROGRAMS\${PRODUCT_NAME}.url" "InternetShortcut" "IconFile" "$INSTDIR\${BIN_NAME}.exe"
  WriteINIStr "$SMPROGRAMS\${PRODUCT_NAME}.url" "InternetShortcut" "IconIndex" "0"
SectionEnd

; ============================================================================
;  安装区段 2：注册服务（交给程序自己的 install 子命令）
; ============================================================================
Section "注册系统服务" SecService
  ; 组装 install 参数：默认值已在 .onInit 里填好，静默参数会覆盖对应项
  !if ${ROLE} == "server"
    StrCpy $2 "install -l $ListenAddr"
    ${If} $Upstream != ""
      StrCpy $2 "$2 -upstream $Upstream -upstream-interval $UpstreamInterval"
    ${EndIf}
    StrCpy $2 "$2 -web 127.0.0.1:$WebPort"
  !else
    StrCpy $2 "install -s $ServerAddr -interval $Interval -step $Step -web 127.0.0.1:$WebPort"
  !endif
  ${If} $Preserve == "1"
    StrCpy $2 "$2 -preserve"
    DetailPrint "检测到已有配置，保留其中的参数（仅重新注册服务）"
  ${EndIf}

  DetailPrint "注册服务 ${SERVICE_NAME}：${BIN_NAME}.exe $2"
  nsExec::ExecToLog '"$INSTDIR\${BIN_NAME}.exe" $2'
  Pop $0
  ${If} $0 != "0"
    DetailPrint "服务注册失败，退出码 $0"
    MessageBox MB_OK|MB_ICONSTOP \
      "服务注册失败，安装未完成。$\r$\n$\r$\n可能是服务名被占用或权限不足。可打开「显示详细信息」查看日志，$\r$\n或以管理员身份在命令行重试：$\r$\n$\r$\n  cd /d $INSTDIR$\r$\n  ${BIN_NAME}.exe $2"
    SetErrorLevel 1
    Abort
  ${EndIf}

  ; ---- Web 管理端口放行（仅服务端；UDP 123 已由程序 install 放行）----
  !if ${ROLE} == "server"
    DetailPrint "放行 Web 管理端口 TCP $WebPort"
    nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="NTP 授时服务 Web (TCP $WebPort)"'
    nsExec::ExecToLog 'netsh advfirewall firewall add rule name="NTP 授时服务 Web (TCP $WebPort)" dir=in action=allow protocol=TCP localport=$WebPort'
  !endif
SectionEnd

; ============================================================================
;  卸载
; ============================================================================
Section "Uninstall"
  SetShellVarContext all

  DetailPrint "停止服务与进程…"
  ; 这里直接用 sc，不调 ${BIN_NAME}.exe uninstall：万一程序文件已损坏或被删，
  ; 卸载仍需要能把服务清掉。服务名是编译期写死的 ${SERVICE_NAME}，
  ; 与对面那个产品的服务名不同，不会误删。
  nsExec::ExecToLog 'sc stop ${SERVICE_NAME}'
  Sleep 1500
  nsExec::ExecToLog 'sc delete ${SERVICE_NAME}'
  nsExec::ExecToLog 'taskkill /F /IM ${BIN_NAME}.exe'
  Sleep 500

  DeleteRegValue HKLM "Software\Microsoft\Windows\CurrentVersion\Run" "NTP授时服务"
  DeleteRegValue HKLM "Software\Microsoft\Windows\CurrentVersion\Run" "NTP校时客户端"

  ; ---- 清防火墙规则：含旧版本遗留的规则名，避免升级后留垃圾 ----
  !if ${ROLE} == "server"
    nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="NTP 授时服务 (UDP 123)"'
    nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="NTP 授时 123"'
    nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="NTP 授时服务 Web (TCP ${DEFAULT_WEB})"'
    nsExec::ExecToLog 'netsh advfirewall firewall delete rule name="NTP Web 管理 ${DEFAULT_WEB}"'
  !endif

  DetailPrint "删除程序文件…"
  Delete "$SMPROGRAMS\${PRODUCT_NAME}.url"
  Delete "$INSTDIR\${BIN_NAME}.exe"
  Delete "$INSTDIR\${UNINST_NAME}"
  ; 目录按产品分开后，卸载器也各删各的，不需要再判断「对方装了没」。
  Delete "$INSTDIR\uninstall.exe"
  RMDir "$INSTDIR"

  DeleteRegKey HKLM "Software\${PRODUCT_NAME_EN}"
  DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME_EN}"

  DetailPrint "配置与日志已保留在 %ProgramData% 目录下的 ntpsync（重装后可直接沿用）"
SectionEnd

; ============================================================================
;  初始化：默认值 + 静默参数解析 + 升级保护判定
;  服务端: /S /LISTEN=0.0.0.0:123 /UPSTREAM=ntp.aliyun.com /UPSTREAM-INTERVAL=1小时 /WEB=8080
;  客户端: /S /SERVER=127.0.0.1:123 /INTERVAL=15分 /STEP=5秒 /WEB=8090
; ============================================================================
Function .onInit
  ; 默认值：GUI 模式下向导不再询问参数，全部取这里（与程序内置默认一致）
  StrCpy $ListenAddr "0.0.0.0:123"
  StrCpy $ServerAddr "127.0.0.1:123"
  StrCpy $WebPort "${DEFAULT_WEB}"
  StrCpy $Interval "15分"
  StrCpy $Step "5秒"
  StrCpy $Upstream ""
  StrCpy $UpstreamInterval "1小时"
  StrCpy $HadSilentArg "0"
  StrCpy $Preserve "0"
  ; 配置文件路径：与程序使用同一个环境变量（本机 NSIS 版本没有 $PROGRAMDATA 常量，
  ; 直接用 ReadEnvStr 读环境变量，取不到再退回默认位置）。
  ReadEnvStr $CfgPath "ProgramData"
  ${If} $CfgPath == ""
    StrCpy $CfgPath "C:\ProgramData"
  ${EndIf}
  StrCpy $CfgPath "$CfgPath\ntpsync\${CFG_FILE}"

  ${GetParameters} $R0

  ${GetOptions} $R0 "/S" $R1
  IfErrors +2 0
  SetSilent silent

  ${GetOptions} $R0 "/LISTEN=" $R2
  ${IfNot} ${Errors}
    StrCpy $ListenAddr "$R2"
    StrCpy $HadSilentArg "1"
  ${EndIf}

  ${GetOptions} $R0 "/SERVER=" $R2
  ${IfNot} ${Errors}
    StrCpy $ServerAddr "$R2"
    StrCpy $HadSilentArg "1"
  ${EndIf}

  ${GetOptions} $R0 "/WEB=" $R2
  ${IfNot} ${Errors}
    StrCpy $WebPort "$R2"
    StrCpy $HadSilentArg "1"
  ${EndIf}

  ${GetOptions} $R0 "/INTERVAL=" $R2
  ${IfNot} ${Errors}
    StrCpy $Interval "$R2"
    StrCpy $HadSilentArg "1"
  ${EndIf}

  ${GetOptions} $R0 "/STEP=" $R2
  ${IfNot} ${Errors}
    StrCpy $Step "$R2"
    StrCpy $HadSilentArg "1"
  ${EndIf}

  ${GetOptions} $R0 "/UPSTREAM=" $R2
  ${IfNot} ${Errors}
    StrCpy $Upstream "$R2"
    StrCpy $HadSilentArg "1"
  ${EndIf}

  ${GetOptions} $R0 "/UPSTREAM-INTERVAL=" $R2
  ${IfNot} ${Errors}
    StrCpy $UpstreamInterval "$R2"
    StrCpy $HadSilentArg "1"
  ${EndIf}

  ; 端口留空时回退默认，避免 Web 界面打不开
  ${If} $WebPort == ""
    StrCpy $WebPort "${DEFAULT_WEB}"
  ${EndIf}

  ; 升级保护：已有配置文件、且本次没显式指定任何参数 → 用户是「在 Web 里配好再升级」，
  ; 保留原配置，只重新注册服务。显式给了参数说明运维要覆盖，那就照办。
  ${If} $HadSilentArg == "0"
  ${AndIf} ${FileExists} "$CfgPath"
    StrCpy $Preserve "1"
  ${EndIf}
FunctionEnd
