<#
.SYNOPSIS
    卸载残留核查：逐项确认本程序写入的服务、注册表、防火墙、配置与快捷方式都已清干净。

.DESCRIPTION
    只读脚本——不删除、不修改任何东西。适合卸载完当场跑一遍，也适合运维巡检。

    检查范围严格限定在本程序自己的痕迹上，不会碰系统或第三方数据：
      · 系统服务：SntpServer / SntpClient（以及 1.0.x 的 ntpsyncd / ntpsync）
      · 注册表：Software\<产品名>、Uninstall\<产品名>（32/64 两个视图）、
                Run 下的 1.0.x 自启动项、1.0.x 的 NTPTimeServer / NTPTimeClient 键
      · 防火墙：本程序命名的 NTP 规则（名字带端口，按前缀匹配）
      · 数据：%ProgramData%\ntpsync 下属于该角色的配置与授时统计（含 .tmp）
      · 文件：程序目录、开始菜单项

.PARAMETER Role
    server / client / both（默认 both）。

.PARAMETER DataDir
    数据目录，默认 %ProgramData%\ntpsync。指向别处可用于自测脚本本身。

.PARAMETER SkipFirewall
    跳过防火墙检查。Get-NetFirewallRule 需要管理员权限，非管理员运行时用这个开关
    避免一条 SKIP 变成误报。

.EXAMPLE
    powershell -NoProfile -ExecutionPolicy Bypass -File scripts\verify-uninstall.ps1

.EXAMPLE
    powershell -NoProfile -ExecutionPolicy Bypass -File scripts\verify-uninstall.ps1 -Role server

.NOTES
    退出码：0 = 无残留；1 = 有残留（明细见输出表格）。
#>
[CmdletBinding()]
param(
    [ValidateSet('server', 'client', 'both')][string]$Role = 'both',
    [string]$DataDir = '',
    [switch]$SkipFirewall
)

$ErrorActionPreference = 'Continue'

if (-not $DataDir) { $DataDir = Join-Path $env:ProgramData 'ntpsync' }
$startMenu = Join-Path $env:ProgramData 'Microsoft\Windows\Start Menu\Programs'
$progFiles = @($env:ProgramW6432, $env:ProgramFiles) | Where-Object { $_ } | Select-Object -Unique
$runKey = 'HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Run'

# 每个角色只列自己的东西。这里与 packaging/win/ntp-installer.nsi 的清理清单一一对应
# （internal/pal/uninstall_test.go 会断言两边一致）。
$roles = @{
    server = @{
        Service      = 'SntpServer'
        LegacySvc    = 'ntpsyncd'
        Product      = 'SntpServer'
        LegacyProd   = 'NTPTimeServer'
        RunValue     = 'NTP授时服务'
        ConfigFile   = 'ntpsyncd.json'
        StatsFile    = 'SntpServer-clients.json'
    }
    client = @{
        Service      = 'SntpClient'
        LegacySvc    = 'ntpsync'
        Product      = 'SntpClient'
        LegacyProd   = 'NTPTimeClient'
        RunValue     = 'NTP校时客户端'
        ConfigFile   = 'ntpsync.json'
        StatsFile    = ''
    }
}

$targets = if ($Role -eq 'both') { @('server', 'client') } else { @($Role) }

$results = New-Object System.Collections.Generic.List[object]
function Add-Result {
    param([string]$Item, [string]$Target, [string]$State, [string]$Note = '')
    # 逐行打印而不是最后拼一张表：中文列宽在窄控制台里会被截断，
    # 而「哪一项是 FAIL」正是这个脚本唯一要传达的信息。
    $color = switch ($State) {
        'PASS' { 'Green' }
        'FAIL' { 'Red' }
        'SKIP' { 'Yellow' }
        default { 'DarkGray' }   # KEEP：按设计保留，不算残留
    }
    $line = "  [{0,-4}] {1}  {2}" -f $State, $Item, $Target
    if ($Note) { $line += "  — $Note" }
    Write-Host $line -ForegroundColor $color
    $results.Add([pscustomobject]@{ 项目 = $Item; 位置 = $Target; 结果 = $State; 说明 = $Note })
}

$fail = 0
function Check-Absent {
    param([string]$Item, [string]$Target, [bool]$Exists, [string]$Note = '')
    if ($Exists) {
        Add-Result $Item $Target 'FAIL' ($Note + '仍存在')
        $script:fail++
    } else {
        Add-Result $Item $Target 'PASS' $Note
    }
}

# 防火墙规则：Get-NetFirewallRule 需要管理员权限，取不到就如实报 SKIP。
$fwRules = $null
if (-not $SkipFirewall) {
    try {
        $fwRules = @(Get-NetFirewallRule -ErrorAction Stop |
            Where-Object { $_.DisplayName -like 'NTP 授时*' -or $_.DisplayName -like 'NTP Web 管理*' })
    } catch {
        $fwRules = $null
    }
}

foreach ($r in $targets) {
    $cfg = $roles[$r]
    Write-Host ""
    Write-Host "--- $r（$($cfg.Product)）---" -ForegroundColor Cyan

    # 服务
    $svc = Get-Service -Name $cfg.Service -ErrorAction SilentlyContinue
    Check-Absent '系统服务' $cfg.Service ($null -ne $svc)
    if ($cfg.LegacySvc) {
        $old = Get-Service -Name $cfg.LegacySvc -ErrorAction SilentlyContinue
        Check-Absent '旧版服务（1.0.x）' $cfg.LegacySvc ($null -ne $old)
    }

    # 注册表：本产品的两个键 + 1.0.x 的产品键 + 自启动项
    $ownKeys = @(
        "HKLM:\SOFTWARE\$($cfg.Product)",
        "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\$($cfg.Product)",
        "HKLM:\SOFTWARE\WOW6432Node\$($cfg.Product)",
        "HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\$($cfg.Product)"
    )
    foreach ($k in $ownKeys) { Check-Absent '注册表键' $k (Test-Path $k) }

    $legacyKeys = @(
        "HKLM:\SOFTWARE\$($cfg.LegacyProd)",
        "HKLM:\SOFTWARE\Microsoft\Windows\CurrentVersion\Uninstall\$($cfg.LegacyProd)",
        "HKLM:\SOFTWARE\WOW6432Node\$($cfg.LegacyProd)",
        "HKLM:\SOFTWARE\WOW6432Node\Microsoft\Windows\CurrentVersion\Uninstall\$($cfg.LegacyProd)"
    )
    foreach ($k in $legacyKeys) {
        if (Test-Path $k) {
            # 旧版还在盘上时保留键是正确行为（否则旧程序卸不掉），只提示不算失败
            $legacyDir = $progFiles | ForEach-Object { Join-Path $_ 'ntpsync' } | Where-Object { Test-Path $_ }
            if ($legacyDir) {
                Add-Result '注册表键（1.0.x）' $k 'KEEP' "旧版程序目录仍在（$($legacyDir -join ', ')），保留其卸载入口"
            } else {
                Check-Absent '注册表键（1.0.x）' $k $true
            }
        } else {
            Add-Result '注册表键（1.0.x）' $k 'PASS' ''
        }
    }

    $runVal = (Get-ItemProperty -Path $runKey -Name $cfg.RunValue -ErrorAction SilentlyContinue)
    Check-Absent '自启动项' "$runKey\$($cfg.RunValue)" ($null -ne $runVal)

    # 防火墙
    if ($null -eq $fwRules) {
        Add-Result '防火墙规则' 'NTP 授时*/NTP Web 管理*' 'SKIP' '需要管理员权限或已加 -SkipFirewall'
    } else {
        $hit = @($fwRules | Where-Object { $_.DisplayName })
        if ($hit.Count -gt 0) {
            Add-Result '防火墙规则' ($hit.DisplayName -join '; ') 'FAIL' '仍存在；可手动删除：netsh advfirewall firewall delete rule name="<规则名>"'
            $fail++
        } else {
            Add-Result '防火墙规则' 'NTP 授时*/NTP Web 管理*' 'PASS' ''
        }
    }

    # 数据文件：只查本角色的，目录里属于对方产品的文件不算残留
    $cfgPath = Join-Path $DataDir $cfg.ConfigFile
    Check-Absent '配置文件' $cfgPath (Test-Path $cfgPath)
    Check-Absent '配置文件临时文件' "$cfgPath.tmp" (Test-Path "$cfgPath.tmp")
    if ($cfg.StatsFile) {
        $statsPath = Join-Path $DataDir $cfg.StatsFile
        Check-Absent '授时统计' $statsPath (Test-Path $statsPath)
        Check-Absent '授时统计临时文件' "$statsPath.tmp" (Test-Path "$statsPath.tmp")
    }
    if (Test-Path $DataDir) {
        $left = @(Get-ChildItem $DataDir -Force -File -ErrorAction SilentlyContinue | Select-Object -ExpandProperty Name)
        if ($left.Count -eq 0) {
            Add-Result '数据目录' $DataDir 'FAIL' '目录已空却仍存在（卸载应删除）'
            $fail++
        } else {
            Add-Result '数据目录' $DataDir 'KEEP' "非空，剩余：$($left -join ', ')（属于同机另一个产品或用户自行放置，未删除）"
        }
    } else {
        Add-Result '数据目录' $DataDir 'PASS' '已删除'
    }

    # 程序目录与开始菜单
    foreach ($pf in $progFiles) {
        $dir = Join-Path $pf $cfg.Product
        Check-Absent '程序目录' $dir (Test-Path $dir)
    }
    Check-Absent '开始菜单项' (Join-Path $startMenu "$($cfg.Product).url") (Test-Path (Join-Path $startMenu "$($cfg.Product).url"))
    Check-Absent '开始菜单文件夹（1.10.x 及以前）' (Join-Path $startMenu $cfg.Product) (Test-Path (Join-Path $startMenu $cfg.Product))
}

Write-Host ""

$failed = @($results | Where-Object { $_.结果 -eq 'FAIL' })
$skipped = @($results | Where-Object { $_.结果 -eq 'SKIP' })

if ($failed.Count -gt 0) {
    Write-Host "=== 发现 $($failed.Count) 项残留 ===" -ForegroundColor Red
    foreach ($f in $failed) { Write-Host ("  · {0}：{1}" -f $f.项目, $f.位置) -ForegroundColor Red }
    if ($skipped.Count -gt 0) { Write-Host "（另有 $($skipped.Count) 项因权限跳过，建议以管理员身份重跑）" -ForegroundColor Yellow }
    exit 1
}

Write-Host "=== 未发现残留（共 $($results.Count) 项检查）===" -ForegroundColor Green
if ($skipped.Count -gt 0) { Write-Host "（其中 $($skipped.Count) 项因权限跳过，建议以管理员身份重跑以覆盖防火墙）" -ForegroundColor Yellow }
exit 0
