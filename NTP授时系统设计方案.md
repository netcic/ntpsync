# 局域网 NTP 授时系统 · 设计方案（已冻结）

## 0. 目标
无外网的局域网内提供 NTP 授时：
- 服务端（时间源 = 本机硬件时钟）常驻对外授时，兼容标准 NTP 客户端（含交换机 / 设备）；
- 客户端工具支持「手动点一下 + 常驻后台守护」双模式校时；
- 跨 Win7 / 10 / 11 + Windows Server、Linux、国产 OS（UOS / 麒麟等），CPU x86 + ARM；
- Motrix 风格图形界面；
- 企业静默部署（命令行 / 策略批量推送，无交互）；
- 开发环境有外网，部署环境无外网。

## 1. 已锁定决策（Q1–Q6）
| 项 | 结论 |
|----|------|
| Q1 服务端时间源 | 本机硬件时钟（RTC） |
| Q2 校时模型 | 服务端常驻守护 + 客户端手动/后台双模式（已确认） |
| Q3 协议保真度 | 兼容标准 NTP 客户端，含交换机/设备授时 → 服务端须发 RFC5905 合法报文 |
| Q4 目标 OS/架构 | Win7/10/11 + Win Server；Linux；国产 OS；CPU x86 + ARM；**无 macOS** |
| Q5 分发形态 | 企业静默部署 |
| Q6 网络环境 | 开发有外网，部署无外网 |

## 2. 技术选型
**Go 单静态二进制 + Vue3（Motrix 风格 Web UI，go:embed 内嵌）**
- 后端 Go：交叉编译 win/linux × amd64/arm64，单文件静态二进制，`go:embed` 内嵌前端，部署零运行时依赖。
- 前端 Vue3：开发期 npm 构建后嵌入，Motrix 观感（深色 / 卡片 / 干净）。
- NTP 核心：Go 实现，RFC5905 兼容服务端 + SNTP 客户端（共享）。
- PAL：Go 分平台实现（Windows：w32tm/SetSystemTime + Windows 服务；Linux/国产：timedatectl/date + systemd / SysV 兜底）。
- 静默部署：Windows MSI/exe `/S` + 配置参数；Linux/国产 systemd unit + `install.sh --server <IP>`。

**选型理由**
1. Win7 无法运行现代 Electron（Chromium 已弃 Win7）→ 排除 Electron；Go + Web UI 全平台（含 Win7）覆盖。
2. 部署无外网 → Go 静态二进制零依赖，最稳。
3. 开发有外网 → `go mod` / `npm` 正常拉取，无碍。

**代价**：GUI 为本地网页而非原生窗口；须保证 Vue 构建产物随二进制发布。

## 3. 架构分层（单一事实来源：NTP 核心库）
```
UI 层    : Vue Web UI（Motrix 风格，localhost 加载）
应用编排  : 配置 / 调度 / 生命周期（含服务端守护、客户端双模式）
NTP 核心  : 64位时间戳 / 编解码 / 偏差·时延计算 / RFC5905 报文构造
网络层    : UDP（服务端监听 123 / 客户端请求·重试·超时）
PAL 层    : 读钟 / 设钟 / 提权 / 装服务（Windows / Linux·国产 两套）
```

## 4. 任务清单与依赖
| ID | 任务 | 依赖 |
|----|------|------|
| T0 | 决策冻结 | —（已完成）|
| T1 | NTP 核心库：时间戳 / 编解码 / 偏差·时延 / RFC5905 报文 | 无 |
| T2 | UDP 网络层：服务端 123 监听 + 客户端请求 | T1 |
| T3 | NTP 服务端守护：合法 NTP 报文 + 常驻 + 装服务（Win service / systemd / SysV） | T1,T2 |
| T4 | PAL-Windows：设钟（w32tm/SetSystemTime，管理员）+ 服务安装（含 Win7/Server） | T1 |
| T5 | PAL-Linux/国产：设钟（timedatectl/date/ntpdate，root）+ 服务（systemd，SysV 兜底） | T1 |
| T6 | 客户端应用：复用 OS NTP（配 peer+触发 resync）为主 + 自带 SNTP 显示偏差 + 手动/自动双模式 + 跳变护栏 | T2,T4,T5 |
| T7 | Web UI（Vue/Motrix）：服务端面板 / 客户端面板 / 设置 / 日志 / 服务端时间核对，go:embed 集成 | T3,T6 |
| T8 | 打包与静默部署：Win(7/10/11/Server)×x86_64[+ARM64] + Linux/国产×x86_64/ARM64；MSI/exe /S + systemd+install.sh；离线静态 | T7 |
| T9 | 验证：VM/真机跨 OS + 标准客户端(w32tm/chrony/交换机模拟) + 边界 + 提权拒绝 + 跳变护栏 + 静默部署演练 | T8 |
| T10 | 文档交付：部署手册 / 静默参数表 / 使用说明 | T9 |

**依赖关系 / 关键路径**
```
关键路径：T1 → T2 → T3 → T7 → T8 → T9 → T10
并行块  ：T1 → T4,T5（与 T3 并行）→ T6 → T7
```
> PAL（T4、T5）在 T1 后可并行；客户端 T6 依赖 PAL 与网络层；T7 集成后进入打包。

## 5. 风险登记表
| ID | 风险 | 状态 / 缓解 |
|----|------|------------|
| R1 | 改系统时钟需 admin/root | 提权助手；提权拒绝时明确报错降级 |
| R2 | 服务端时间源未定义 | ✅ 已解（Q1：本机硬件时钟）|
| R3 | 离线构建依赖不可达 | ✅ 已解（Go 静态 + Q6 部署无网）|
| R4 | 校时模型未定 | ✅ 已解（Q2：服务端守护 + 客户端双模式）|
| R5 | 大偏差跳变搞崩依赖时间的系统 | 阈值护栏（如 >1000s 弹确认），禁盲跳 |
| R6 | 防火墙 / UDP 123 被拦 | 启动自检 + 连通性诊断页 |
| R7 | 时区处理错误 | 核心全程 UTC，仅 UI 本地化显示 |
| R8 | macOS 限制 | N/A（Q4 已剔除 macOS）|
| R9 | 服务端并发 / 健壮性 | 单线程非阻塞 UDP + 压测覆盖 |
| R10 | 协议保真度 | ✅ 已解（Q3：RFC5905 兼容）|
| R11 | 服务端钟错污染全 LAN | 操作员核对入口 + R5 护栏兜底 |
| R12 | Win7 不支持 Electron | ✅ 已解（Go + Web UI 全平台覆盖）|
| R13 | 国产 OS 多样性（Ubuntu/CentOS 系；部分 SysVinit；龙芯 LoongArch）| 仅承诺 x86+ARM；龙芯范围外 |
| R14 | 交换机 / IoT 多为 SNTP 级，报文不合法会被拒 | T3 严格按 RFC5905 填 stratum/ref/root 字段 |
| R15 | SELinux / 国产安全模块拦截 UDP123 或设钟 | 启动自检 + 文档说明 |
| R16 | Win7 的 w32tm 默认未启用 | 客户端先 `w32tm /register` + 启用服务 |

## 6. 已知限制
- 龙芯 LoongArch 未覆盖（仅 x86 + ARM）。
- macOS 已剔除。
- 服务端精度上限 = 本机 RTC 精度（Q1）。
- 客户端持续跟随依赖 OS NTP 守护（方案A），非一次性设钟。
- 国产 OS 具体版本（UOS / Kylin 不同系）需实测验证。
- 部署环境无外网：所有依赖须在开发期固化进静态二进制 / 嵌入资源。
