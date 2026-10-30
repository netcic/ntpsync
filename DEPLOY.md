# 局域网 NTP 授时系统 — 部署手册

无外网局域网内的 NTP/SNTP 授时系统，服务端以本机硬件时钟为权威时间源，向交换机、服务器、
工控机等标准 NTP 客户端授时；客户端（及设备）校时收敛到服务端。跨 Windows / Linux / 国产 OS，
x86 + ARM，支持企业静默批量部署。

---

## 一、部署架构

本项目采用**标准双组件部署**，服务端与客户端各自拆成两个进程：

| 组件 | 进程 | 运行方式 | 职责 |
|------|------|----------|------|
| **服务** | `Sntpserver` / `SntpClient` | 系统服务（Session 0，静默） | 服务端授时 + Web 界面 / 客户端周期校时 + 本地 Web |

- **Web 界面**：服务端默认 `http://127.0.0.1:8080`，客户端默认 `http://127.0.0.1:8090`，
  两者都内嵌在各自二进制里（离线可用，浏览器打开即可）。
- **跨平台一致**：Windows / Linux / 国产 OS 均以系统服务方式部署，通过 Web 界面管理。

---

## 二、产物清单

### 独立安装程序（推荐分发方式）

| 文件 | 系统 | 说明 |
|------|------|------|
| `Sntpserver-Setup-v<ver>-amd64.exe` | Windows x64 | 服务端安装器（GUI 向导 + `/S` 静默） |
| `Sntpserver-Setup-v<ver>-arm64.exe` | Windows ARM64 | 服务端安装器（GUI 向导 + `/S` 静默） |
| `SntpClient-Setup-v<ver>-amd64.exe` | Windows x64 | 客户端安装器（GUI 向导 + `/S` 静默） |
| `SntpClient-Setup-v<ver>-arm64.exe` | Windows ARM64 | 客户端安装器（GUI 向导 + `/S` 静默） |
| `sntpserver_<ver>_<arch>.deb` / `sntpserver-<ver>-1.<arch>.rpm` | Linux / 国产 OS | 服务端原生包（systemd + 自启） |
| `sntpclient_<ver>_<arch>.deb` / `sntpclient-<ver>-1.<arch>.rpm` | Linux / 国产 OS | 客户端原生包（systemd + 自启） |

### 裸二进制 + 脚本（进阶 / GPO / Ansible）

| 文件 | 用途 |
|------|------|
| `Sntpserver-v<ver>-<os>-<arch>[.exe]` | 服务端（含内嵌 Web 界面） |
| `SntpClient-v<ver>-<os>-<arch>[.exe]` | 客户端（check/sync/daemon + 内嵌 Web） |
| `scripts/build.sh` | 一键交叉编译全矩阵 |
| `scripts/build-installers.bat` | 一键生成 4 个 Windows 安装器（需 NSIS） |
| `scripts/install-server.bat` / `.sh` | 服务端静默安装（系统服务 + Web 界面） |
| `scripts/install-client.bat` / `.sh` | 客户端静默安装（系统服务 + Web 界面） |
| `scripts/uninstall-server.bat` / `.sh` | 服务端一键卸载（服务 + 安装目录） |
| `scripts/uninstall-client.bat` / `.sh` | 客户端一键卸载（服务 + 安装目录） |
| `scripts/package.sh` | 打 deb / rpm 包（需在 Linux 机上运行） |

---

## 三、Windows 部署

### 3.0 独立安装程序（推荐，普通用户双击即可）

**GUI 向导安装**：双击对应架构的 `*-Setup-v<ver>-*.exe`，一路「下一步」即可——向导只有
「欢迎 → 安装目录 → 安装 → 完成」四步，**不再询问运行参数**。安装器会自动：
复制程序 → 注册系统服务（开机自启）→ 放行防火墙（仅服务端）→ 建开始菜单项，
并在「添加/删除程序」登记卸载项。

安装完在完成页勾选「打开 Web 管理页面」（或开始菜单 → 产品名快捷方式「Sntpserver / SntpClient」）做配置：
服务端配监听地址/上游时间源，客户端填服务端地址。参数保存在
`%ProgramData%\ntpsync\` 下的配置文件里，**重启不会丢**。

> 开始菜单里**只放一个以产品名命名的 Web 入口快捷方式**（直接落在开始菜单根目录，不套产品子文件夹）；
> 卸载统一走控制面板「添加/删除程序」，开始菜单不再单独放「卸载」快捷方式。

| 项 | Sntpserver（服务端） | SntpClient（客户端） |
|---|---|---|
| 程序目录 | `%ProgramFiles%\Sntpserver` | `%ProgramFiles%\SntpClient` |
| 主程序 | `Sntpserver.exe` | `SntpClient.exe` |
| 系统服务名 | `Sntpserver` | `SntpClient` |
| 卸载器 | `uninstall.exe` | `uninstall.exe` |
| 配置文件 | `%ProgramData%\ntpsync\ntpsyncd.json` | `%ProgramData%\ntpsync\ntpsync.json` |
| 默认 Web 端口 | 8080 | 8090 |

> 覆盖升级：若已存在配置文件且本次没带任何静默参数，安装器会自动保留其中的配置
> （只重新注册服务），你在 Web 里配好的内容不会被重装抹掉。显式带了参数则以参数为准。

**静默安装（企业批量部署）**：加 `/S` 参数，再带运行参数：

```bat
:: 服务端静默安装（可选配上游时间源）
Sntpserver-Setup-v1.10.28-amd64.exe /S /LISTEN=0.0.0.0:123 /UPSTREAM=ntp.aliyun.com /UPSTREAM-INTERVAL=1小时 /WEB=8080

:: 客户端静默安装
SntpClient-Setup-v1.10.28-amd64.exe /S /SERVER=192.168.1.10:123 /INTERVAL=15分 /STEP=5秒 /WEB=8090
```

| 安装器参数 | 服务端 | 客户端 | 说明 |
|-----------|--------|--------|------|
| `/LISTEN=` | ✓ | — | 监听地址 |
| `/UPSTREAM=` | ✓ | — | 上游 NTP 时间源（留空用本机硬件时钟） |
| `/UPSTREAM-INTERVAL=` | ✓ | — | 上游同步间隔，默认 1小时 |
| `/SERVER=` | — | ✓ | 校时目标服务端地址 |
| `/INTERVAL=` | — | ✓ | 校时间隔 |
| `/STEP=` | — | ✓ | 校时阈值（护栏） |
| `/WEB=` | ✓ | ✓ | Web 管理端口（默认 8080 / 8090） |

> 静默参数与 GUI 安装写的是**同一个配置文件**（不是服务启动命令），因此装完在 Web 里
> 改完的参数重启后依然生效；服务启动命令恒为 `<exe> run`。

> **上游时间源怎么选**：实测公网上游可靠性差异很大——`ntp.aliyun.com`（46ms）、
> `cn.pool.ntp.org`（20ms）、`time.windows.com`（114ms）均稳定；而国家授时中心
> `ntp.ntsc.ac.cn` 解析出的 3 个 IP 里 2 个长期无响应、剩下 1 个丢包率约 75%，
> **不建议用于生产**。本文档示例统一使用 `ntp.aliyun.com`。
>
> 若部署环境完全无外网，`/UPSTREAM=` 留空即可：服务端以本机硬件时钟为权威源，
> 只需保证装机时人工校准过一次时钟。

> 卸载：控制面板「添加/删除程序」→ 找到「Sntpserver / SntpClient」→ 卸载；
> 或运行安装目录下的 `uninstall.exe`。会清理服务、程序文件、开始菜单项与防火墙规则；
> **配置与日志（`%ProgramData%\ntpsync`）默认保留**，重装后可直接沿用。
>
> 两个产品自 1.10.28 起各装各的目录（`%ProgramFiles%\Sntpserver`、`%ProgramFiles%\SntpClient`），
> 版本号统一取自根目录 `VERSION` 文件，升版只需改那一处后再跑构建/打包脚本。
> 各管各的服务（`Sntpserver` / `SntpClient`），卸载器也各自独立——
> 同一台机器上同时装了服务端和客户端时，**卸客户端不会连带删掉服务端的服务**。

**从 v1.0.x 升级**：旧版程序名叫 `ntpsyncd` / `ntpsync`，装在 `%ProgramFiles%\ntpsync`，
服务名与程序同名。直接双击新版安装器即可，安装时会先把同名旧服务停掉并删除，
避免新旧两个服务抢 UDP 123（抢不到就表现为「装完反而授不上时」）。
旧程序目录与它在「添加/删除程序」里的条目保留不动，确认新版正常后手动卸一次旧版即可。
便携部署同目录的配置文件支持新名 `SntpClient.conf`，旧名 `ntpsync.conf` 仍可读。

### 3.1 脚本方式（进阶 / GPO / Ansible，服务端管理员运行）

```bat
install-server.bat /LISTEN=0.0.0.0:123 /WEB=8080 /UPSTREAM=ntp.aliyun.com /UPSTREAM-INTERVAL=1小时
```

完成：复制二进制到 `%ProgramFiles%\Sntpserver\` → 注册系统服务 `Sntpserver`（开机自启 + Web 界面）
→ 放行 UDP 123 与 TCP 8080。

### 3.2 客户端（需要校时的机器，管理员运行）

```bat
install-client.bat /SERVER=192.168.1.10:123 /INTERVAL=15分 /STEP=5秒 /WEB=8090
```

完成：复制二进制到 `%ProgramFiles%\SntpClient\` → 注册系统服务 `SntpClient`（开机自启 + 周期校时 + 本地 Web）。
客户端为出站连接，无需放行防火墙。

### 3.3 安装脚本参数

**install-server.bat**

| 参数 | 默认 | 说明 |
|------|------|------|
| `/LISTEN=` | `0.0.0.0:123` | 监听地址 |
| `/UPSTREAM=` | 空 | 上游 NTP 时间源（留空用本机硬件时钟） |
| `/UPSTREAM-INTERVAL=` | `1小时` | 上游同步间隔 |
| `/WEB=` | `8080` | Web 界面端口，留空不启用 |
| `/AUTOSTART=` | `1` | 1=注册服务并自启；0=仅复制文件 |

**install-client.bat**

| 参数 | 默认 | 说明 |
|------|------|------|
| `/SERVER=` | 必填 | 服务端地址，如 `192.168.1.10:123` |
| `/INTERVAL=` | `15分` | 校时间隔 |
| `/STEP=` | `5秒` | 校时阈值（单次允许的最大跳变） |
| `/WEB=` | `8090` | 本地 Web 界面端口，留空不启用 |
| `/AUTOSTART=` | `1` | 1=注册服务并自启；0=仅复制文件 |

### 3.4 卸载（管理员运行）

```bat
uninstall-client.bat     :: 卸载客户端（服务 + 安装目录）
uninstall-server.bat     :: 卸载服务端（服务 + 安装目录）
```

一键清理系统服务、`%ProgramFiles%\SntpClient\` 安装目录。
若服务仍显示运行中，重启后再跑一次即可。

---

## 四、Linux / 国产 OS 静默部署

### 4.1 服务端（root）

```bash
bash install-server.sh --listen 0.0.0.0:123 --web 8080 --upstream ntp.aliyun.com --upstream-interval 1小时
```

完成：安装到 `/usr/local/bin/Sntpserver` → 写 systemd unit（常驻授时 + Web 界面）
→ 放行防火墙（firewalld / ufw）→ enable 并启动。

### 4.2 客户端（root）

```bash
bash install-client.sh --server 192.168.1.10:123 --interval 15分 --step 5秒 --web 8090
```

完成：安装到 `/usr/local/bin/SntpClient` → 写 systemd unit（常驻 daemon 周期校时 + 本地 Web）
→ enable 并启动。

> Web 界面默认 `http://127.0.0.1:8080`（服务端）/ `http://127.0.0.1:8090`（客户端）。

### 4.3 卸载（root）

```bash
bash uninstall-client.sh     # 卸载客户端（systemd 服务 + 二进制）
bash uninstall-server.sh     # 卸载服务端（systemd 服务 + 二进制 + 撤销防火墙放行）
```

一键停止并禁用 systemd 服务、删除 unit 文件与 `/usr/local/bin/` 二进制，
服务端另撤销 firewalld / ufw 的 UDP 123 放行。脚本对缺失项均容错，可重复执行。

---

## 五、命令行直装（等价于脚本，适合 Ansible 编排）

不想用脚本时，可用二进制自带子命令直接装：

```bash
# 服务端（Windows 管理员 / Linux root，可选配上游时间源）
Sntpserver install -l 0.0.0.0:123 -web 127.0.0.1:8080 -upstream ntp.aliyun.com -upstream-interval 1小时

# 客户端
SntpClient install -s 192.168.1.10:123 -interval 15分 -step 5秒 -web 127.0.0.1:8090
```

### 服务管理子命令（通用）

| 命令 | 说明 |
|------|------|
| `install` | 装为系统服务 + 开机自启（服务端另自动放行防火墙） |
| `uninstall` | 停止并卸载**本程序自己的**服务（服务端 `Sntpserver` / 客户端 `SntpClient`），服务本来就没装时静默返回 |
| `start` / `stop` | 启动 / 停止服务 |
| `status` | 查看服务状态 |

---

## 六、批量推送（企业静默部署）

- **Windows + 域**：GPO 启动脚本 / SCCM 软件分发执行
  `install-server.bat` / `install-client.bat`，全程无弹窗、自动装服务 + 放行防火墙。
- **Linux 批量**：Ansible / SaltStack 分发二进制 + 脚本，一条命令铺开。
- **无网环境**：二进制均为静态、无外网依赖，U 盘/内网文件共享分发即可。

> 交换机/路由器/其他标准设备：直接在设备 NTP 配置里把服务器地址指向服务端 IP 即可
> （服务端发 RFC5905 合法报文，兼容 w32tm、chrony、ntpd、交换机 SNTP 客户端）。

---

## 七、防火墙与提权注意事项

| 项 | 说明 |
|----|------|
| UDP 123 | 服务端需放行（脚本已自动配 netsh / firewalld / ufw） |
| Web 界面 | 默认只绑 `127.0.0.1`，如需远程访问请改绑 `0.0.0.0` 并放行对应 TCP 端口 |
| 提权 | 服务端装服务、客户端改钟均需管理员/root；脚本会检测并提示 |
| 跳变护栏 | 客户端单次偏差超阈值（默认 5秒）拒绝自动改钟，防服务端钟错污染全网 |

---

## 八、验证清单

1. 服务端：`Sntpserver status`（Win）或 `systemctl status Sntpserver`（Linux）显示运行中。
2. 客户端：`SntpClient check -s <服务端>` 返回偏差 < 100ms。
3. 客户端：`SntpClient sync -s <服务端>` 显示"已校时"或合理跳过原因。
4. 交换机：设备 NTP 状态显示已同步（stratum 由服务端 =2 决定）。
5. Web 界面：服务端 `http://<服务端>:8080`、客户端 `http://127.0.0.1:8090` 总览页正常。
