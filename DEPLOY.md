# 局域网 NTP 授时系统 — 部署手册

无外网局域网内的 NTP/SNTP 授时系统，服务端以本机硬件时钟为权威时间源，向交换机、服务器、
工控机等标准 NTP 客户端授时；客户端（及设备）校时收敛到服务端。跨 Windows / Linux / 国产 OS，
x86 + ARM，支持企业静默批量部署。

---

## 一、部署架构

本项目采用**标准双组件部署**，服务端与客户端各自拆成两个进程：

| 组件 | 进程 | 运行方式 | 职责 |
|------|------|----------|------|
| **服务** | `SntpServer` / `SntpClient` | 系统服务（Session 0，静默） | 服务端授时 + Web 界面 / 客户端周期校时 + 本地 Web |

- **Web 界面**：只对局域网开放。服务端 `http://<服务端IP>:8080`（端口独立，默认 8080），
  客户端 `http://<客户端IP>:8090`（端口由 `-web` 指定）；两者都内嵌在各自二进制里（离线可用，浏览器打开即可）。
- **跨平台一致**：Windows / Linux / 国产 OS 均以系统服务方式部署，通过 Web 界面管理。

---

## 二、产物清单

### 独立安装程序（推荐分发方式）

| 文件 | 系统 | 说明 |
|------|------|------|
| `SntpServer-Setup-v<ver>-amd64.exe` | Windows x64 | 服务端安装器（GUI 向导 + `/S` 静默） |
| `SntpServer-Setup-v<ver>-arm64.exe` | Windows ARM64 | 服务端安装器（GUI 向导 + `/S` 静默） |
| `SntpClient-Setup-v<ver>-amd64.exe` | Windows x64 | 客户端安装器（GUI 向导 + `/S` 静默） |
| `SntpClient-Setup-v<ver>-arm64.exe` | Windows ARM64 | 客户端安装器（GUI 向导 + `/S` 静默） |
| `sntpserver_<ver>_<arch>.deb` / `sntpserver-<ver>-1.<arch>.rpm` | Linux / 国产 OS | 服务端原生包（systemd + 自启） |
| `sntpclient_<ver>_<arch>.deb` / `sntpclient-<ver>-1.<arch>.rpm` | Linux / 国产 OS | 客户端原生包（systemd + 自启） |

### 裸二进制 + 脚本（进阶 / GPO / Ansible）

| 文件 | 用途 |
|------|------|
| `SntpServer-v<ver>-<os>-<arch>[.exe]` | 服务端（含内嵌 Web 界面） |
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

安装完在完成页勾选「打开 Web 管理页面」（或开始菜单 → 产品名快捷方式「SntpServer / SntpClient」，
指向 `http://127.0.0.1:<端口>`；其它电脑用 `http://本机IP:<端口>`）做配置：
服务端配监听地址/上游时间源，客户端填服务端地址。参数保存在
`%ProgramData%\ntpsync\` 下的配置文件里，**重启不会丢**。

> 开始菜单里**只放一个以产品名命名的 Web 入口快捷方式**（直接落在开始菜单根目录，不套产品子文件夹）；
> 卸载统一走控制面板「添加/删除程序」，开始菜单不再单独放「卸载」快捷方式。

| 项 | SntpServer（服务端） | SntpClient（客户端） |
|---|---|---|
| 程序目录 | `%ProgramFiles%\SntpServer` | `%ProgramFiles%\SntpClient` |
| 主程序 | `SntpServer.exe` | `SntpClient.exe` |
| 系统服务名 | `SntpServer` | `SntpClient` |
| 卸载器 | `uninstall.exe` | `uninstall.exe` |
| 配置文件 | `%ProgramData%\ntpsync\ntpsyncd.json` | `%ProgramData%\ntpsync\ntpsync.json` |
| 默认 Web 端口 | 8080（`/WEB=` 可改） | 8090（`/WEB=` 可改） |

> 覆盖升级：若已存在配置文件且本次没带任何静默参数，安装器会自动保留其中的配置
> （只重新注册服务），你在 Web 里配好的内容不会被重装抹掉。显式带了参数则以参数为准。

**静默安装（企业批量部署）**：加 `/S` 参数，再带运行参数：

```bat
:: 服务端静默安装（可选配上游时间源）
SntpServer-Setup-v1.11.9-amd64.exe /S /LISTEN=0.0.0.0:123 /UPSTREAM=ntp.aliyun.com /UPSTREAM-INTERVAL=1小时 /WEB=8080

:: 客户端静默安装
SntpClient-Setup-v1.11.9-amd64.exe /S /SERVER=192.168.1.10:123 /INTERVAL=15分 /STEP=5秒 /WEB=8090
```

| 安装器参数 | 服务端 | 客户端 | 说明 |
|-----------|--------|--------|------|
| `/LISTEN=` | ✓ | — | 监听地址 |
| `/UPSTREAM=` | ✓ | — | 上游 NTP 时间源（留空用本机硬件时钟） |
| `/UPSTREAM-INTERVAL=` | ✓ | — | 上游同步间隔，默认 1小时 |
| `/SERVER=` | — | ✓ | 校时目标服务端地址 |
| `/INTERVAL=` | — | ✓ | 校时间隔 |
| `/STEP=` | — | ✓ | 校时阈值（护栏） |
| `/WEB=` | ✓ | ✓ | Web 界面端口：服务端默认 8080、客户端默认 8090（与授时端口无关） |

> 静默参数与 GUI 安装写的是**同一个配置文件**（不是服务启动命令），因此装完在 Web 里
> 改完的参数重启后依然生效；服务启动命令恒为 `<exe> run`。

> **上游时间源怎么选**：实测公网上游可靠性差异很大——`ntp.aliyun.com`（46ms）、
> `cn.pool.ntp.org`（20ms）、`time.windows.com`（114ms）均稳定；而国家授时中心
> `ntp.ntsc.ac.cn` 解析出的 3 个 IP 里 2 个长期无响应、剩下 1 个丢包率约 75%，
> **不建议用于生产**。本文档示例统一使用 `ntp.aliyun.com`。
>
> 若部署环境完全无外网，`/UPSTREAM=` 留空即可：服务端以本机硬件时钟为权威源，
> 只需保证装机时人工校准过一次时钟。

> 卸载：控制面板「添加/删除程序」→ 找到「SntpServer / SntpClient」→ 卸载；
> 或运行安装目录下的 `uninstall.exe`。会清理服务、程序文件、开始菜单项、防火墙规则、
> **运行期生成的配置文件与授时统计**（`%ProgramData%\ntpsync\` 下本产品那几份，
> 见[十一、卸载清理清单与残留核查](#十一卸载清理清单与残留核查)），以及安装/运行期写入的
> 注册表项（含 1.0.x 遗留键）。**配置会被删掉，需留档请先复制该目录**；只想换版本不想动配置，
> 用程序自带 `uninstall -keep-config`。
>
> 两个产品自 1.10.28 起各装各的目录（`%ProgramFiles%\SntpServer`、`%ProgramFiles%\SntpClient`），
> 版本号统一取自根目录 `VERSION` 文件，升版只需改那一处后再跑构建/打包脚本。
> 各管各的服务（`SntpServer` / `SntpClient`），卸载器也各自独立——
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

完成：复制二进制到 `%ProgramFiles%\SntpServer\` → 注册系统服务 `SntpServer`（开机自启 + Web 界面）
→ 放行 UDP 123 与 TCP 8080（Web 界面独立端口）。

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
| `/WEB=` | `8080` | Web 界面端口（只对局域网开放），留空不启用 |
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
uninstall-client.bat     :: 卸载客户端（服务 + 安装目录 + 防火墙 + 注册表 + 配置）
uninstall-server.bat     :: 卸载服务端（同上，多撤 UDP/TCP 授时相关放行）
```

`uninstall-*.bat` 与安装器走同一套清理清单，共 6 步：停删服务（含 1.0.x 旧服务名）→ 删安装目录
→ 撤防火墙规则 → 删开始菜单项 → 删注册表项（32 位与 `WOW6432Node` 两个视图）→ 删本角色的
配置文件与授时统计（含 `.tmp` 残留），最后 `rd` 非递归删除数据目录——**目录里还有对方产品的文件
就删不掉，这是刻意的**。完整路径表见[十一](#十一卸载清理清单与残留核查)。

若服务仍显示运行中，重启后再跑一次即可；脚本对缺失项均容错，可重复执行。

---

## 四、Linux / 国产 OS 静默部署

### 4.1 服务端（root）

```bash
bash install-server.sh --listen 0.0.0.0:123 --web 8080 --upstream ntp.aliyun.com --upstream-interval 1小时
```

完成：安装到 `/usr/local/bin/SntpServer` → 写 systemd unit（常驻授时 + Web 界面）
→ 放行防火墙（firewalld / ufw）→ enable 并启动。

### 4.2 客户端（root）

```bash
bash install-client.sh --server 192.168.1.10:123 --interval 15分 --step 5秒 --web 8090
```

完成：安装到 `/usr/local/bin/SntpClient` → 写 systemd unit（常驻 daemon 周期校时 + 本地 Web）
→ enable 并启动。

> Web 界面只对局域网开放：服务端 `http://<服务端IP>:8080`、客户端 `http://<客户端IP>:8090`。

### 4.3 卸载（root）

```bash
bash uninstall-client.sh     # 卸载客户端（systemd 服务 + 二进制 + 防火墙 + 配置）
bash uninstall-server.sh     # 卸载服务端（同上，多撤 UDP/TCP 授时相关放行）
```

共 5 步：停止并禁用 systemd 服务（含 `Sntpserver` / `ntpsyncd` 历史 unit）→ 删 unit 文件
→ 删 `/usr/local/bin/` 二进制 → 撤销 firewalld / ufw 放行（服务端含 UDP 123 与 TCP 8080/123，
客户端仅 TCP 8090，**不动 UDP 123**，那是同机服务端的事）→ 删 `/etc/ntpsync/` 下本角色的
配置文件与授时统计（含 `.tmp`），最后 `rmdir` 非递归删目录。脚本对缺失项均容错，可重复执行。

> `.deb` / `.rpm` 卸载走 `DEBIAN/postrm` 与 `%postun`，行为与脚本一致：
> 只删本包自己的配置文件，`rmdir` 非递归删 `/etc/ntpsync`；rpm 的 `%postun` 带
> `if [ "$1" -eq 0 ]` 判断，**升级时不删配置**，只有真正卸载才清。

---

## 五、命令行直装（等价于脚本，适合 Ansible 编排）

不想用脚本时，可用二进制自带子命令直接装：

```bash
# 服务端（Windows 管理员 / Linux root，可选配上游时间源）
SntpServer install -l 0.0.0.0:123 -web 8080 -upstream ntp.aliyun.com -upstream-interval 1小时

# 客户端
SntpClient install -s 192.168.1.10:123 -interval 15分 -step 5秒 -web 8090
```

### 服务管理子命令（通用）

| 命令 | 说明 |
|------|------|
| `install` | 装为系统服务 + 开机自启（服务端另自动放行防火墙） |
| `uninstall [-config PATH] [-keep-config]` | 停止并卸载**本程序自己的**服务，撤防火墙放行，删本产品的配置文件与授时统计（含 `.tmp`）。服务本来就没装时静默返回；`-keep-config` 只卸服务与防火墙、保留配置（换版本不动参数用这个） |
| `start` / `stop` | 启动 / 停止服务 |
| `status` | 查看服务状态 |

> `uninstall` 撤防火墙时**按配置文件里的实际端口**撤，并兜底清理历史默认端口
> （服务端 UDP `配置端口`/`123`、TCP `配置端口`/`8080`/`123`；客户端 TCP `配置端口`/`8090`，
> 不碰 UDP 123）。不需要管理员也能删配置，但撤服务和防火墙会因权限不足失败并给出提示。

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

1. 服务端：`SntpServer status`（Win）或 `systemctl status SntpServer`（Linux）显示运行中。
2. 客户端：`SntpClient check -s <服务端>` 返回偏差 < 100ms。
3. 客户端：`SntpClient sync -s <服务端>` 显示"已校时"或合理跳过原因。
4. 交换机：设备 NTP 状态显示已同步（stratum 由服务端 =2 决定）。
5. Web 界面：服务端 `http://<服务端IP>:8080`、客户端 `http://<客户端IP>:8090` 总览页正常（同网段访问，非仅本机）。

---

## 九、Web 界面配色（固定内置）

界面使用**固定内置浅色主题**：配色由内嵌页面 `internal/web/index.html` 顶部的 `:root`
CSS 变量统一决定，不再由服务端下发，也没有主题切换器和 `themes.json`。

| 变量 | 值 | 用途 |
|------|-----|------|
| `--bg` | `#F5F7FA` | 页面底色 |
| `--panel` / `--card` | `#FFFFFF` | 顶栏 / 卡片 |
| `--border` | `#E2E8F0` | 分割线与描边 |
| `--text` / `--muted` | `#1E293B` / `#475569` | 主文字 / 次要文字 |
| `--accent` | `#0066FF` | 品牌蓝：Logo、主按钮、选中态 |
| `--accent-soft` | `#EBF2FF` | 次要按钮、选中项底色 |
| `--green` / `--amber` / `--red` | `#10B981` / `#F59E0B` / `#EF4444` | 正常 / 告警 / 故障 |
| `--table-head` / `--row-hover` | `#F5F7FA` / `#F1F5F9` | 表头底色 / 行悬停 |

> 语义色另有一组 `-text` 变体（`--green-text` / `--amber-text` / `--red-text`）：
> 原色用于圆点、描边等视觉主体，文字用加深变体，避免 `#10B981`、`#F59E0B` 直接当小字
> 写在白底上对比不足、发飘。

- 想换配色：直接改这组 CSS 变量（`--bg` / `--panel` / `--card` / `--accent` / `--green` / `--red` 等），
  然后重新构建、替换程序即可。
- 注意 `--bar-grad` / `--bar-grad-v` 由 `border-image` 消费，只接受 `<image>`（渐变或图片），
  **写成纯色会让整条声明被浏览器丢弃**，表现为分隔线消失。要「纯色线」请写成
  `linear-gradient(90deg,当前色,当前色)`。

## 十、Web 界面的访问范围与端口

Web 界面**固定只对局域网开放**（监听 `0.0.0.0`）；端口独立配置，与授时端口无关：

| | 访问范围 | 默认端口 | 默认地址 |
|---|---|---|---|
| 服务端 | 局域网可访问（`0.0.0.0`） | `8080` | `http://服务端IP:8080` |
| 客户端 | 局域网可访问（`0.0.0.0`） | `8090` | `http://客户端IP:8090` |

```bash
# 服务端 / 客户端：-web 给的是「Web 端口」，只对局域网开放；建议同时设访问令牌
SntpServer run -l 0.0.0.0:123 -web 8080 -web-token 换成你的令牌
SntpClient daemon -s 192.168.1.10:123 -web 8090 -web-token 换成你的令牌
# 静默安装（/WEBLAN=1 已无作用，保留兼容）
SntpServer-Setup-v1.11.9-amd64.exe /S /LISTEN=0.0.0.0:123 /WEB=8080 /WEBTOKEN=换成你的令牌
```

- **Web 端口与授时端口分开**：改 `-l` 的端口**不会**动 Web 端口。现场的书签、
  防火墙规则、监控探针都按 8080/8090 配，跟着监听端口漂移只会制造事故
  （1.11.3 曾让服务端 Web 端口跟随 `-l`，本版已改回独立 8080）。
- 防火墙要放行两个端口：UDP 123（授时）+ TCP 8080/8090（界面）。
  安装器与 `install` 子命令会自动放行，手工部署的需要自己补。
- `-web-lan` / `/WEBLAN=1` 保留兼容：访问范围本来就是局域网，加与不加没有区别。
- 令牌首次访问用 `http://IP:端口/?t=令牌`，服务端种下 cookie 后正常访问（地址栏里的 `?t=` 会被清掉）。
  令牌按主机名分别记住，**每台电脑首次访问都要带一次**。
- 令牌随配置落盘（`web_token`），重启不会丢；界面里「保存配置」也不会把它覆盖掉。
- **安全提醒**：界面没有账号体系，能打开就能改上游时间源（等于能左右全网时间）。
  只在可信内网部署，并配 `-web-token`；对公网暴露时请前置反向代理做鉴权。
- 状态总览页与参数页**不再显示 Web 访问地址 / 访问范围 / Web 端口**（1.11.4 起移除）：
  访问范围固定局域网（`0.0.0.0`）是产品决定，端口由启动参数 / 安装参数 / 配置文件决定（见 10.1），
  访问入口就是上表的 `http://IP:端口`。

### 10.1 改 Web 端口

界面里没有 Web 设置项（1.11.4 起移除），端口只在三处生效，改完都要**重启服务**：

| 途径 | 写法 | 说明 |
|------|------|------|
| 启动参数 | `-web 8080` | 常驻进程同时开界面，值即端口 |
| 安装参数 | `/WEB=8080`、`--web 8080` | 安装时写进配置文件 |
| 配置文件 | `"web": "0.0.0.0:8080"` | 优先级最高，见下 |

- 访问范围不可配：主机部分无论填什么，启动时统一规整成 `0.0.0.0`（只对局域网开放）；
  只填端口也能用（如 `"web": "8080"`）。
- 端口校验仍在：`POST /api/config` 收到 `web_addr` 时校验端口 1–65535，非法直接 400
  且**不落盘、不改内存**——这个值下次启动会直接拿去 `ListenAndServe`，写坏了 Web 就起不来、
  界面也连不上，只能手改配置文件自救。授时监听地址的端口同样校验——写坏了服务端起不来，
  现场表现是「客户端全部校不到时」。
- 没有 `web` 配置项时用默认值：服务端 `0.0.0.0:8080`、客户端 `0.0.0.0:8090`；删掉 `web` 字段
  即回到默认。
- 端口被占用不再静默（1.11.6 起）：日志写「Web 管理界面启动失败：无法监听 `0.0.0.0:8080`（…）。
  端口可能已被其他程序占用…授时功能不受影响」。此前是先打「已启动」再报异常，现场看到成功与失败
  并存，容易误判成地址填错，实际是端口被别的程序（常是旧版残留进程）占着。
- 页面响应带 `Cache-Control: no-store`（1.11.6 起）：升级后浏览器不会继续用旧缓存，
  不会出现「已经删掉的区块又冒出来、以为是升级没生效」的假象。
- 浏览器标签页图标带内容指纹（1.11.9 起）：页面里是 `/favicon.ico?v=<8位哈希>`，
  哈希取自内嵌图标本体。换图标或升级后 URL 自然变化，绕开浏览器 favicon 历史库
  （Chrome `Favicons` / Firefox `favicons.sqlite` 按页面 URL 记，**响应头 no-store 清不掉**，
  F5 也不一定换）——这正是「程序里图标早换了、标签页还是旧的」的成因。
  旧版页面固定请求 `/favicon.ico`，从 1.11.9 之前升级上来的机器若仍显示旧图标，
  换一次浏览器配置文件或用无痕窗口访问即可确认已生效。`/favicon.ico` 本身照常可访问，
  书签、外部引用不受影响。

重启命令：

```powershell
# Windows（管理员）
Restart-Service SntpServer     # 客户端换成 SntpClient
```

```bash
# Linux（root）
systemctl restart SntpServer      # 客户端换成 SntpClient
```

> 访问令牌（`-web-token`）同样由启动参数 / 安装参数设置，不进界面（不回显，避免令牌泄露到浏览器）。

**配置优先级**：配置文件里的 `web` 地址**优先于**命令行 `-web`——否则旧版安装写进服务
启动参数的 `-web` 会把配置文件里的端口一直顶回去。若日志里出现
「Web 监听地址以配置文件为准…（命令行 -web 已忽略）」，说明正是这种情况；重跑一次
`install -preserve` 即可让服务启动参数不再携带 `-web`。

> **从 1.11.3 升级的提醒**：1.11.3 把服务端 Web 端口绑成了授时端口（`123`），配置文件里的
> `web` 可能存着 `0.0.0.0:123`。本版升级后服务照旧按配置文件启动，**Web 仍然听在 123**
> （1.11.4 起界面里也没有改端口的地方了）；想回到 8080，删掉配置文件里的 `web` 字段
> （缺省即 `0.0.0.0:8080`），或用 `-l 0.0.0.0:123 -web 8080` 重新执行一次 `install`。
> 此前为 123 放行过 TCP 的，记得补放行 8080。

> **1.11.5 修复「装完 8090 打不开」**：1.0.x / 1.10.x 装出来的配置里没有 `web` 字段，而覆盖升级
> 走 `-preserve` 时旧实现整份沿用旧配置、不落盘，安装器传的 `/WEB=8090` 被丢掉；服务启动参数
> 也不带 `-web`，于是进程压根没起界面 —— 表现就是「服务在跑、`http://IP:8090` 打不开」。
> 1.11.5 起：保留模式下若旧配置缺 `web`，会把本次安装的 `-web` **补写**进配置文件（其余参数
> 仍保持原值），重装一次即恢复，同时补放行对应 TCP 端口。
> 不想重装就手工修：在 `%ProgramData%\ntpsync\ntpsync.json`（服务端 `ntpsyncd.json`）里加
> `"web": "0.0.0.0:8090"`（服务端 `8080`），`Restart-Service SntpClient`，再放行该 TCP 端口。

> **服务端改名（1.11.3）**：产品名从 `Sntpserver` 改为 `SntpServer`（小写 p 改大写 P），
> 可执行文件、系统服务名、安装目录、systemd unit 一并更新。
> - Windows 服务名大小写不敏感，`SntpServer` 与老 `Sntpserver` 是同一条服务，升级无需额外操作。
> - Linux 的 unit 文件名区分大小写，安装/升级脚本会先停掉并删除旧的 `Sntpserver.service`
>   与 `/usr/local/bin/Sntpserver`，避免老服务继续占着 UDP 123 / TCP 8080。
> - 从 1.11.x 之前的 `ntpsyncd` 升级同理，脚本与 `install` 子命令都会清理。

---

## 十一、卸载清理清单与残留核查

卸载器（Windows 安装器 / `uninstall-*.bat` / `uninstall-*.sh` / deb·rpm 维护脚本 /
程序自带 `uninstall` 子命令）清理的范围，就是本程序**在安装与运行期间真正写过的那些位置**。
下面按类别逐项列出，可拿来当验收清单。

> **1.11.8 起的行为变更**：此前卸载**保留** `%ProgramData%\ntpsync` 与 `/etc/ntpsync`
> （「重装可沿用配置」），现在改为**删掉本产品自己的配置与授时统计**，并新增防火墙与注册表
> 反向清理。需要保留配置的场景（换版本、迁移、重装前留档）用 `uninstall -keep-config`，
> 或卸载前手工复制该目录。1.0.x 旧程序目录及其注册表键仍刻意不动，见 11.5。

### 11.1 配置、数据与统计文件

| 角色 | 路径（Windows） | 路径（Linux） | 内容 |
|------|-----------------|---------------|------|
| 服务端 | `%ProgramData%\ntpsync\ntpsyncd.json` | `/etc/ntpsync/ntpsyncd.json` | 运行配置（监听地址、stratum、上游、Web 端口/令牌） |
| 服务端 | `%ProgramData%\ntpsync\SntpServer-clients.json` | `/etc/ntpsync/SntpServer-clients.json` | 客户端授时统计（总览页「已服务客户端」数据源） |
| 客户端 | `%ProgramData%\ntpsync\ntpsync.json` | `/etc/ntpsync/ntpsync.json` | 运行配置（服务端地址、间隔、阈值、Web 端口/令牌） |

- **每个文件都连带删掉它的 `.tmp`**：配置用「写临时文件 → rename」保证原子性，进程被强杀时
  会留下 `xxx.json.tmp`。清单里漏了这一项，卸载后目录就永远删不掉（非空）。
- **只删自己角色那几份**：卸客户端不会动 `ntpsyncd.json` / `SntpServer-clients.json`，
  卸服务端也不会动 `ntpsync.json`。同机双装时先卸一个，另一个的配置完好。
- **目录用非递归删除**（NSIS `RMDir`、shell `rmdir`、Go `os.Remove`）：目录里只要还剩任何文件
  （包括对方产品的配置、或用户自己放进去的备份）就**静默失败、目录保留**，绝不会连带删掉别人的东西。
  检查结果里「目录仍存在」本身不算残留问题，看的是**目录里还有没有本产品的文件**。
- **本程序不写日志文件、不建缓存目录**：日志只在内存环形缓冲（Web「日志」页看最近若干条）与
  控制台输出，因此没有「日志清理」这一项。`%ProgramData%\ntpsync` 之外的系统目录一概不碰。

### 11.2 注册表项（仅 Windows）

| 键 | 值 | 写于 |
|----|----|------|
| `HKLM\Software\SntpServer` / `HKLM\Software\SntpClient` | `InstallDir`、`Version` | 安装器 |
| `HKLM\Software\Microsoft\Windows\CurrentVersion\Uninstall\SntpServer` / `…\SntpClient` | `DisplayName`、`DisplayVersion`、`UninstallString`、`DisplayIcon`、`Publisher` 等 | 安装器（「添加/删除程序」条目） |
| `HKLM\Software\Microsoft\Windows\CurrentVersion\Run` | 值 `NTP授时服务` / `NTP校时客户端` | **1.0.x 遗留**，1.10.28 起改用系统服务自启，卸载时顺手清 |
| `HKLM\Software\NTPTimeServer` / `HKLM\Software\NTPTimeClient` | 旧产品键 | **1.0.x 遗留** |
| `HKLM\Software\WOW6432Node\NTPTimeServer` / `…\NTPTimeClient` | 同上（32 位视图） | **1.0.x 遗留** |

- 前两组键在**32 位与 64 位两个注册表视图里各写一份**（`SetRegView 32` / `64`），
  所以卸载时也要两遍都删，否则 32 位卸载程序会漏掉 64 位视图里的残留。
- 1.0.x 的键**有条件清理**：只有当 `%ProgramFiles%\ntpsync` 与 `%ProgramFiles(x86)%\ntpsync`
  都不存在（即旧版确实已不在）时才删。旧程序目录还在的情况下必须留着它自己的卸载入口，
  不然「添加/删除程序」里就再也点不掉那个旧版了。
- 除此之外**不写任何注册表**：不注册文件关联、不写 `HKCR`、不写 `HKLM\SYSTEM`、
  不装驱动/不做 WFP 过滤，因此清理范围到上面这张表为止。

### 11.3 防火墙规则

规则名由端口拼成，卸载时按「当前配置里的端口 + 历史默认端口」逐条删：

| 角色 | 规则名 | 端口来源 |
|------|--------|----------|
| 服务端 | `NTP 授时服务 (UDP <port>)` | 配置端口、`123` |
| 服务端 | `NTP 授时服务 Web (TCP <port>)` | 配置 Web 端口、`8080`、`123` |
| 服务端 | `NTP Web 管理 <port>` | 1.11.3 前的旧命名，跟进清理 |
| 服务端 | `NTP 授时 <port>` | 1.10.x 的旧命名，跟进清理 |
| 客户端 | `NTP 授时服务 Web (TCP <port>)` | 配置 Web 端口、`8090` |
| 客户端 | `NTP Web 管理 <port>` | 同上，旧命名 |

> 客户端**不撤 UDP 123 放行**——那是同机服务端装的规则，撤了会把服务端打瘸。
> Linux 同理：只撤自己放行过的端口（firewalld `--remove-port` / ufw `delete allow`），
> 两者都不可用时降级为提示，不报错中断卸载。

### 11.4 服务、快捷方式与程序文件

| 项 | 内容 |
|----|------|
| 系统服务 | `SntpServer` / `SntpClient`；1.0.x 的 `ntpsyncd` / `ntpsync` 一并清（它们会和新服务抢 UDP 123） |
| systemd unit | `/etc/systemd/system/SntpServer.service`、`SntpClient.service`；旧 `Sntpserver.service`、`ntpsyncd.service`、`ntpsync.service` |
| 二进制 | `%ProgramFiles%\SntpServer\`、`%ProgramFiles%\SntpClient\`；`/usr/local/bin/SntpServer`、`SntpClient`（及旧名） |
| 开始菜单 | `%ProgramData%\Microsoft\Windows\Start Menu\Programs\SntpServer\`（`.url` + 文件夹） |
| 安装目录 | 删完 exe / `uninstall.exe` 后再 `RMDir "$INSTDIR"`，残留用户自建文件则保留 |

### 11.5 刻意不删的东西

| 项 | 原因 |
|----|------|
| `%ProgramData%\ntpsync` 目录本身（非空时） | 里面可能还有另一个产品的配置，或用户自己放的备份 |
| `%ProgramFiles%\ntpsync`（1.0.x 旧程序目录）及其卸载条目 | 属旧版本，用户确认新版正常后自行卸一次；自动删会连带删掉用户的旧文件和回退能力 |
| Windows 防火墙的**程序级**放行进/出站规则 | 本程序只用端口级规则，从不按程序路径建规则；若有人手工加过，那是他加的，卸载器不该替他决策 |
| 事件日志、Prefetch、最近使用记录 | 系统自动生成的通用痕迹，不属于「应用配置/数据」，删它们属于越界 |
| 授时统计的历史累计值 | 与服务端共库（`SntpServer-clients.json`），只随服务端卸载清理 |

### 11.6 卸载后核查残留

两个**只读**核查脚本（不删不改，可反复跑，也可用于日常巡检），逐项打印
`[PASS]` / `[FAIL]` / `[KEEP]`（刻意保留，不算失败）/ `[SKIP]`（权限不足，如非管理员查防火墙），
最后给汇总并返回退出码 **0 = 无残留 / 1 = 有残留**，可直接塞进验收流水线。

```powershell
# Windows：默认同时查服务端与客户端
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\verify-uninstall.ps1

powershell -NoProfile -ExecutionPolicy Bypass -File scripts\verify-uninstall.ps1 -Role server
powershell -NoProfile -ExecutionPolicy Bypass -File scripts\verify-uninstall.ps1 -SkipFirewall  # 非管理员
```

```bash
# Linux / 国产 OS：默认同时查服务端与客户端
bash scripts/verify-uninstall.sh
bash scripts/verify-uninstall.sh server
NTPSYNC_CFG_DIR=/tmp/ntpsync bash scripts/verify-uninstall.sh   # 配置目录非默认位置时
```

核查项与清理清单一一对应：服务（含历史名）、`Software\<产品名>` 与 `Uninstall\<产品名>`
（32/64 两视图）、`Run` 下的 1.0.x 值、1.0.x 的 `NTPTimeServer`/`NTPTimeClient` 键、
防火墙规则名、本角色的配置/统计及其 `.tmp`、数据目录、程序目录、开始菜单项。

> 这三处（Go 常量、安装器 `!define`、shell 脚本变量）容易各自漂移——改了一边忘了另一边，
> 卸载就会漏文件。`internal/pal/uninstall_test.go` 会解析 `.nsi` 与 `.sh` 源文件，
> 断言它们与 `config.ServerStatsFileName` / `pal.FirewallRule*` 等常量一致，
> `scripts/check-stage.sh` 另外断言 deb/rpm 维护脚本的行为。所以「改名忘改脚本」会在
> `go test ./...` 阶段就红掉，不会等到现场卸载才发现残留。

