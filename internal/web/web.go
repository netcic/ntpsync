// Package web 提供内嵌的 Web 管理界面与 REST API。
//
// 前端为单文件 Vue3（Motrix 风格，浅色/卡片式/顶部导航），通过 go:embed 内嵌，
// 本地 HTTP 服务加载，离线可用、兼容 Win7 等旧浏览器（不依赖 WebView/Electron）。
//
// REST API（均返回 JSON）：
//
//	GET  /api/status       服务端运行状态（running/stratum/uptime/requests/lastPeer/addr）
//	GET  /api/server/time  服务端当前时间（用于时间核对）
//	POST /api/client/check 客户端只测偏差（body: {server}）
//	POST /api/client/sync  客户端校时（body: {server, step?}）
//	GET  /api/config       读取配置
//	POST /api/config       保存配置（body: {server, interval, step, listen, web_addr, ...}）
//	GET  /api/logs         最近日志
package web

import (
	"crypto/sha256"
	"crypto/subtle"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"ntpsync/internal/client"
	"ntpsync/internal/duration"
	"ntpsync/internal/pal"
	"ntpsync/internal/server"
)

//go:embed index.html favicon.ico
var staticFS embed.FS

// ClientStatus 描述客户端守护进程的运行时状态（供 Web 界面展示）。
type ClientStatus struct {
	ServerAddr string    // 正在校时的服务端地址
	Interval   string    // 校时间隔
	Step       string    // 校时阈值（单次允许的最大跳变）
	LastOffset string    // 最近一次校时的时钟偏差
	LastDelay  string    // 最近一次往返时延
	LastResult string    // 最近一次结果描述（已调整/跳过/偏差 X）
	LastAt     time.Time // 最近一次校时时刻
}

// Handler 聚合 Web 界面依赖的运行时对象。
type Handler struct {
	Server *server.Server // 服务端守护实例（可为 nil，表示纯客户端模式）
	Config *Config

	// Version 为程序版本号，构建时用 -ldflags -X main.version= 注入。
	// 界面上常驻显示：现场排查时第一件事就是确认「跑的到底是哪一版」，
	// 只靠文件名判断不可靠（部署时经常被改名或复制到别处）。
	Version string

	// clientStatus 记录客户端守护的运行时状态；由客户端 daemon 周期更新。
	clientMu     sync.Mutex
	clientStatus *ClientStatus

	// upstreamSyncFn 为「立即向上游校时一次」的执行函数，由上层注入
	// （web 包不直接依赖同步实现，避免与 client/server 产生循环依赖）。
	// 入参为上游地址，使界面刚改完配置即可直接试同步，无需重启服务。
	// 失败详情由被调函数写入服务端上游状态，本包统一读状态展示，故无返回值。
	upstreamSyncMu sync.Mutex
	upstreamSyncFn func(addr string)

	// OnSave 在 Web 保存配置后被调用，由上层注入。
	// 承担两件事：把配置持久化到磁盘、同步到运行时对象（如动态切换上游地址）。
	// 返回错误时本次修改仍在内存生效，但会记入日志提醒「未能持久化」。
	OnSave func(cfg *Config) error

	// WebAddr 为 Web 界面实际监听地址（如 0.0.0.0:8080），用于界面展示
	// 「本机 / 局域网」访问链接；LAN 表示该地址已经对外（非回环）暴露。
	WebAddr string
	LAN     bool

	// Token 非空时启用访问令牌校验：所有请求需带 ?t=<令牌> 或已种下的 cookie。
	// 局域网暴露时 Web 界面没有账号体系，这是最低限度的保护手段。
	Token string

	mu      sync.Mutex
	logBuf  []string // 环形日志缓冲
	maxLogs int
}

// SetUpstreamSync 注入「立即向上游校时一次」的执行函数。
// 注入的函数需同步阻塞执行，并在返回前把结果写回服务端的上游同步状态
// （server.SetUpstreamStatus），本包随后读取该状态返回给调用方。
func (h *Handler) SetUpstreamSync(fn func(addr string)) {
	h.upstreamSyncMu.Lock()
	h.upstreamSyncFn = fn
	h.upstreamSyncMu.Unlock()
}

// upstreamSyncFunc 取当前注入的上游同步函数（可能为 nil）。
func (h *Handler) upstreamSyncFunc() func(addr string) {
	h.upstreamSyncMu.Lock()
	defer h.upstreamSyncMu.Unlock()
	return h.upstreamSyncFn
}

// SetClientStatus 更新客户端运行时状态（线程安全，供客户端 daemon 周期调用）。
func (h *Handler) SetClientStatus(s ClientStatus) {
	h.clientMu.Lock()
	h.clientStatus = &s
	h.clientMu.Unlock()
}

// ClientStatusSnapshot 返回当前客户端状态快照（可为 nil）。
func (h *Handler) ClientStatusSnapshot() *ClientStatus {
	h.clientMu.Lock()
	defer h.clientMu.Unlock()
	if h.clientStatus == nil {
		return nil
	}
	cp := *h.clientStatus
	return &cp
}

// Config 为界面可读写的最小配置。
type Config struct {
	Listen string `json:"listen"` // 服务端监听地址（重启生效）
	// WebAddr 为 Web 界面的监听地址（重启生效）。访问范围固定 0.0.0.0（只对
	// 局域网开放），端口独立配置：服务端默认 8080、客户端默认 8090，与授时
	// 监听端口无关——改监听端口不该把界面地址一起带走（现场的书签、防火墙
	// 规则都是按 8080/8090 配的）。它落盘到配置文件的 web 字段，下次启动由它
	// 决定监听。
	WebAddr          string `json:"web_addr"`          // Web 界面监听地址（重启生效）
	ServerAddr       string `json:"server"`            // NTP 服务端地址（客户端用）
	Interval         string `json:"interval"`          // 后台校时间隔，如 "15m"
	Step             string `json:"step"`              // 校时阈值，如 "5s"
	Stratum          int    `json:"stratum"`           // 服务端层级
	Upstream         string `json:"upstream"`          // 服务端上游时间源（空=本机硬件时钟）
	UpstreamInterval string `json:"upstream_interval"` // 上游同步间隔，如 "1h"
}

// DefaultConfig 返回默认配置。
func DefaultConfig() *Config {
	return &Config{
		Listen:     "0.0.0.0:123",
		ServerAddr: "127.0.0.1:123",
		Interval:   "15m",
		Step:       "5s",
		Stratum:    int(server.DefaultStratum),
		Upstream:   "",
		// 用中文单位展示（1小时），避免界面上出现 "1h0m0s" 这类机器写法。
		UpstreamInterval: duration.Format(server.DefaultUpstreamInterval),
	}
}

// New 构造 Handler。
func New(s *server.Server, cfg *Config) *Handler {
	if cfg == nil {
		cfg = DefaultConfig()
	}
	return &Handler{Server: s, Config: cfg, maxLogs: 200}
}

// Logf 记录一条日志并写入缓冲（供界面展示）。
func (h *Handler) Logf(format string, args ...interface{}) {
	line := fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), fmt.Sprintf(format, args...))
	h.mu.Lock()
	h.logBuf = append(h.logBuf, line)
	if len(h.logBuf) > h.maxLogs {
		h.logBuf = h.logBuf[len(h.logBuf)-h.maxLogs:]
	}
	h.mu.Unlock()
	log.Print(line)
}

// Routes 注册所有路由到 mux。
func (h *Handler) Routes(mux *http.ServeMux) {
	mux.HandleFunc("/api/status", h.apiStatus)
	mux.HandleFunc("/api/server/time", h.apiServerTime)
	mux.HandleFunc("/api/client/check", h.apiClientCheck)
	mux.HandleFunc("/api/client/sync", h.apiClientSync)
	mux.HandleFunc("/api/config", h.apiConfig)
	mux.HandleFunc("/api/logs", h.apiLogs)
	mux.HandleFunc("/api/clients", h.apiClients)
	mux.HandleFunc("/api/server/upstream/sync", h.apiUpstreamSync)
	// 前端静态资源。
	mux.Handle("/", h.staticHandler())
}

// Guard 在启用访问令牌时给整站加一层校验，否则原样返回。
//
// 局域网开放 Web 界面的风险必须说清楚：本界面没有账号体系，能打开就能改
// 上游时间源——把上游改成恶意地址等于给整个内网喂错时间。界面固定只对
// 局域网开放，所以部署时强烈建议用 -web-token 设一个令牌，Guard 就是这道锁。
func (h *Handler) Guard(next http.Handler) http.Handler {
	tok := strings.TrimSpace(h.Token)
	if tok == "" {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if c, err := r.Cookie(tokenCookie); err == nil && sameToken(c.Value, tok) {
			next.ServeHTTP(w, r)
			return
		}
		if q := r.URL.Query().Get("t"); q != "" && sameToken(q, tok) {
			// 首次带令牌访问：种 cookie 后跳回同地址（去掉 ?t=，避免令牌留在历史记录里）。
			http.SetCookie(w, &http.Cookie{
				Name: tokenCookie, Value: tok, Path: "/",
				HttpOnly: true, SameSite: http.SameSiteLaxMode,
			})
			u := *r.URL
			vals := u.Query()
			vals.Del("t")
			u.RawQuery = vals.Encode()
			http.Redirect(w, r, u.RequestURI(), http.StatusSeeOther)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(tokenPage))
	})
}

const tokenCookie = "ntp_web_token"

const tokenPage = `<!doctype html><html lang="zh-CN"><meta charset="utf-8">
<title>需要访问令牌</title>
<body style="background:#f5f7fa;color:#1e293b;font-family:'Segoe UI','Microsoft YaHei',sans-serif;padding:48px">
<h2 style="margin:0 0 12px">需要访问令牌</h2>
<p style="color:#475569;line-height:1.8">
本机 Web 管理界面启用了访问令牌保护。<br>
请在地址后面追加 <code style="color:#0066ff">?t=你的令牌</code> 再访问一次，<br>
例如 <code style="color:#0066ff">http://192.168.1.10:123/?t=令牌</code>。<br>
令牌由启动参数 <code>-web-token</code> 指定。
</p></body></html>`

// sameToken 常数时间比较，避免通过响应时间反推令牌。
func sameToken(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// faviconVerPlaceholder 由 index.html 里的图标链接携带，服务时替换成内容指纹。
// 写成占位符而不是写死版本号：换图标的人不需要记得改 HTML，指纹自己会变。
const faviconVerPlaceholder = "__FAVICON_VER__"

var (
	faviconVerOnce sync.Once
	faviconVer     string
)

// faviconVersion 返回内嵌 favicon.ico 的内容指纹（sha256 前 8 位十六进制）。
//
// 为什么非要给图标 URL 加指纹：浏览器把 favicon 单独存在历史库（Chrome 的
// Favicons 数据库、Firefox 的 favicons.sqlite）里，按「页面 URL」记，**响应头
// 的 no-store 清不掉已有记录**，按 F5 甚至 Ctrl+F5 也未必换。于是程序里图标早
// 就换了、用户看到的还是上一版，只能靠清浏览器数据或换浏览器自救——这类问题
// 报上来时看起来像「升级没生效」，排查成本极高。把指纹拼进 URL 后，图标一变
// URL 就变，旧记录自然失效，无需用户做任何操作。
func faviconVersion() string {
	faviconVerOnce.Do(func() {
		data, err := staticFS.ReadFile("favicon.ico")
		if err != nil || len(data) == 0 {
			faviconVer = "0"
			return
		}
		sum := sha256.Sum256(data)
		faviconVer = fmt.Sprintf("%x", sum[:4])
	})
	return faviconVer
}

// staticHandler 从内嵌 FS 提供 index.html 与 favicon.ico。
//
// 响应禁止缓存：页面是随程序版本一起内嵌的，升级后若浏览器沿用旧缓存，
// 用户看到的还是上一版的样子（例如本版已删掉的区块又冒出来），只会以为
// 升级没生效、反复重装。页面只有几十 KB，每次现取的代价可以忽略。
//
// 首页由这里自己渲染而不交给 FileServer：需要把 favicon 的内容指纹填进 HTML，
// 而 embed 的文件是只读的、也不该在磁盘上改来改去。
func (h *Handler) staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, ".")
	files := http.FileServer(http.FS(sub))
	var page []byte // 读不到就交给 FileServer，别把占位符原样吐给浏览器
	if raw, err := staticFS.ReadFile("index.html"); err == nil {
		page = []byte(strings.ReplaceAll(string(raw), faviconVerPlaceholder, faviconVersion()))
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate")
		w.Header().Set("Pragma", "no-cache")
		if p := r.URL.Path; page != nil && (p == "/" || p == "/index.html") {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			_, _ = w.Write(page)
			return
		}
		files.ServeHTTP(w, r)
	})
}

// NormalizeListenAddr 把用户填写的 Web 监听地址补全成 host:port。
//
// 命令行与静默安装参数里，人们习惯只写端口（-web 8080、/WEB=8080）。直接交给
// net.Listen 会得到 "missing port in address"，而报错信息完全没提示「要写成
// 127.0.0.1:8080」，现场很难自查。这里统一补上回环地址，让两种写法都能用。
// IPv6 的 [::1]:8080 本身含冒号，原样返回。
func NormalizeListenAddr(addr string) string {
	a := strings.TrimSpace(addr)
	if a == "" {
		return ""
	}
	if !strings.Contains(a, ":") {
		return "127.0.0.1:" + a
	}
	return a
}

// PortOf 取出监听地址里的端口（"0.0.0.0:123" → "123"）；取不到返回空串。
// 只写端口（"123"）也能取到，IPv6 的 [::]:123 交给 net.SplitHostPort 处理。
func PortOf(addr string) string {
	a := NormalizeListenAddr(addr)
	if a == "" {
		return ""
	}
	if _, port, err := net.SplitHostPort(a); err == nil {
		return port
	}
	return ""
}

// PortNumber 取出监听地址里的端口号；取不到或超出 1-65535 时返回 0。
func PortNumber(addr string) int {
	n, err := strconv.Atoi(PortOf(addr))
	if err != nil || n < 1 || n > 65535 {
		return 0
	}
	return n
}

// UniquePorts 去重并丢掉非法端口（0、越界、重复）。
//
// 卸载时要把「配置里的端口」和「历史上用过的默认端口」都撤一遍，
// 两串列表往往有重叠；重复撤销只是多跑一次 netsh/ufw，日志也难看，
// 所以先在这里压平。
func UniquePorts(ports ...int) []int {
	seen := make(map[int]bool, len(ports))
	out := make([]int, 0, len(ports))
	for _, p := range ports {
		if p < 1 || p > 65535 || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

// IsLoopbackAddr 判断监听地址是否只对本机开放（127.0.0.1 / ::1 / localhost）。
//
// 访问范围固定为局域网后，界面上已经没有「仅本机」这个选项，但很早的配置里
// 可能还留着 127.0.0.1:8080。覆盖升级会原样保留它，装完服务在跑、本机能开，
// 别的电脑却怎么都连不上——安装时提示一句，比事后猜防火墙省事得多。
func IsLoopbackAddr(addr string) bool {
	a := NormalizeListenAddr(addr)
	if a == "" {
		return false
	}
	host, _, err := net.SplitHostPort(a)
	if err != nil || host == "" {
		return false
	}
	if strings.EqualFold(host, "localhost") {
		return true
	}
	if ip := net.ParseIP(host); ip != nil {
		return ip.IsLoopback()
	}
	return false
}

// 两端 Web 界面的默认端口。
//
// 同一台机器很可能同时装服务端与客户端，端口必须错开；这两个值也是文档、
// 现场书签与防火墙规则里用惯的地址，所以**不跟授时监听端口联动**：把监听
// 从 123 改成 1123，不该顺手把界面地址也换成 1123。
const (
	// DefaultServerWebPort 服务端 Web 界面默认端口。
	DefaultServerWebPort = 8080
	// DefaultClientWebPort 客户端 Web 界面默认端口。
	DefaultClientWebPort = 8090
)

// ValidateWebAddr 校验 Web 监听地址是否可用（主机:端口，端口 1-65535）。
//
// 这个值会写进配置文件、下次启动直接拿去监听（来源是 `-web`、安装参数或
// 配置文件；界面自 1.11.4 起不再暴露 Web 设置）。若放过一个非法端口，重启后
// Web 起不来、界面也连不上，用户只能手改配置文件自救——所以宁可当场拒绝，
// 也不落一个可能把服务弄瘸的值。
func ValidateWebAddr(addr string) error {
	a := NormalizeListenAddr(addr)
	if a == "" {
		return fmt.Errorf("Web 监听地址不能为空")
	}
	_, port, err := net.SplitHostPort(a)
	if err != nil {
		return fmt.Errorf("Web 监听地址格式应为 主机:端口，例如 127.0.0.1:8080")
	}
	n, err := strconv.Atoi(port)
	if err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("Web 端口必须是 1-65535 之间的数字")
	}
	return nil
}

// ValidateListenAddr 校验授时监听地址（主机:端口，端口 1-65535）。
//
// 与 Web 地址同理：它也会被落盘、下次启动直接拿去做 UDP bind，端口写坏了
// 服务端起不来（现场表现是「客户端全部校不到时」），只能手改配置文件自救。
func ValidateListenAddr(addr string) error {
	if PortNumber(addr) == 0 {
		return fmt.Errorf("监听地址的端口必须是 1-65535 之间的数字，例如 0.0.0.0:123")
	}
	return nil
}

// LANAddr 把回环监听的 Web 地址换成对外可达地址（0.0.0.0:同端口）。
//
// 界面固定只对局域网开放，所以这个函数现在是「规整」而非「放开」：旧配置里
// 残留的 127.0.0.1:端口 会在启动/保存时被改成 0.0.0.0:端口，端口保持不变。
// 早期版本靠它实现 -web-lan，那段语义现在已默认成立。
func LANAddr(addr string) string {
	a := NormalizeListenAddr(addr)
	if a == "" {
		return ""
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return a
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		return a
	}
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" || host == "[::1]" {
		return "0.0.0.0:" + port
	}
	return a
}

// IsLANAddr 判断监听地址是否已对外暴露（非回环）。
func IsLANAddr(addr string) bool {
	a := NormalizeListenAddr(addr)
	if a == "" {
		return false
	}
	host, _, err := net.SplitHostPort(a)
	if err != nil {
		return false
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		return true
	}
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
		return false
	}
	return true
}

// WebURLs 给出该监听地址对应的可访问链接：回环地址给本机链接，
// 通配（0.0.0.0/::）给本机 + 全部网卡 IPv4，用于界面直接告诉用户
// 「别人该填哪个地址」——现场最常见的问题就是不知道填哪个 IP。
func WebURLs(addr string) []string {
	a := NormalizeListenAddr(addr)
	if a == "" {
		return nil
	}
	host, port, err := net.SplitHostPort(a)
	if err != nil {
		return nil
	}
	host = strings.Trim(host, "[]")
	if host == "" || host == "0.0.0.0" || host == "::" {
		out := []string{"http://127.0.0.1:" + port}
		for _, ip := range server.LocalIPs() {
			out = append(out, "http://"+ip+":"+port)
		}
		return out
	}
	if host == "localhost" || strings.HasPrefix(host, "127.") || host == "::1" {
		return []string{"http://127.0.0.1:" + port}
	}
	return []string{"http://" + net.JoinHostPort(host, port)}
}

// displayVersion 把版本号规整成界面上显示的形态。
// 未注入版本（go run 或开发构建）时显示「开发版」，避免界面上出现 dev 这种
// 对现场无意义、又容易被误认为是版本号的字样。
func displayVersion(v string) string {
	v = strings.TrimSpace(v)
	if v == "" || v == "dev" {
		return "开发版"
	}
	if strings.HasPrefix(v, "v") {
		return v
	}
	return "v" + v
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// apiStatus 返回运行状态。
// 服务端模式（Server 非 nil）返回授时状态；客户端模式（Server 为 nil）返回校时状态。
func (h *Handler) apiStatus(w http.ResponseWriter, r *http.Request) {
	resp := map[string]interface{}{
		"server_running": false,
		"platform":       pal.NewPlatform().Name(),
		"elevated":       pal.NewPlatform().IsElevated(),
		"version":        displayVersion(h.Version),
		// Web 界面自身的访问信息：局域网内其它电脑该用哪个地址、是否已对外暴露。
		"web_addr":           h.WebAddr,
		"web_lan":            h.LAN,
		"web_urls":           WebURLs(h.WebAddr),
		"web_token_required": strings.TrimSpace(h.Token) != "",
	}
	if h.Server != nil {
		resp["server_running"] = h.Server.IsRunning()
		resp["addr"] = h.Server.DisplayAddr()
		// 本机可用于对外授时的 IPv4 地址（供界面提示客户端该填哪个地址）。
		resp["local_ips"] = server.LocalIPs()
		resp["stratum"] = h.Server.Stratum()
		resp["uptime"] = duration.Format(h.Server.Uptime().Round(time.Second))
		resp["requests"] = h.Server.Requests()
		resp["last_peer"] = h.Server.LastPeer()
		resp["mode"] = "server"
		// 上游时间源同步状态（配置了上游时返回）。
		resp["upstream"] = h.Server.Upstream()
		if us := h.Server.UpstreamStatusSnapshot(); us != nil {
			resp["upstream_status"] = map[string]interface{}{
				"addr":    us.Addr,
				"offset":  us.Offset,
				"delay":   us.Delay,
				"result":  us.Result,
				"synced":  us.Synced,
				"last_at": us.LastAt.Format("2006-01-02 15:04:05"),
			}
		}
	} else {
		// 客户端模式：返回校时状态。
		resp["mode"] = "client"
		if cs := h.ClientStatusSnapshot(); cs != nil {
			resp["client_server"] = cs.ServerAddr
			resp["interval"] = cs.Interval
			resp["step"] = cs.Step
			resp["last_offset"] = cs.LastOffset
			resp["last_delay"] = cs.LastDelay
			resp["last_result"] = cs.LastResult
			resp["last_at"] = cs.LastAt.Format("15:04:05")
		}
	}
	writeJSON(w, resp)
}

func (h *Handler) apiServerTime(w http.ResponseWriter, r *http.Request) {
	now := time.Now()
	writeJSON(w, map[string]interface{}{
		"utc":   now.UTC().Format("2006-01-02 15:04:05.000"),
		"local": now.Local().Format("2006-01-02 15:04:05.000"),
		"unix":  now.Unix(),
	})
}

// apiClients 返回服务端最近授时的客户端列表（仅服务端模式）。
func (h *Handler) apiClients(w http.ResponseWriter, r *http.Request) {
	if h.Server == nil {
		writeJSON(w, map[string]interface{}{"clients": []interface{}{}})
		return
	}
	list := h.Server.Clients()
	if list == nil {
		list = []server.ClientInfo{}
	}
	if strings.EqualFold(r.URL.Query().Get("format"), "csv") {
		writeClientsCSV(w, list, h.Version)
		return
	}
	writeJSON(w, map[string]interface{}{"clients": list})
}

// writeClientsCSV 把客户端列表导出成 CSV。
//
// 两个细节来自现场反馈：① 加 UTF-8 BOM，否则 Excel 打开中文全是乱码；
// ② 行尾用 CRLF，Excel 才按单元格分行。文件名带时间戳，多次导出不会互相覆盖。
func writeClientsCSV(w http.ResponseWriter, list []server.ClientInfo, version string) {
	name := fmt.Sprintf("ntp-clients-%s.csv", time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	// UTF-8 BOM：Excel 靠它判断编码，少了这 3 字节中文就是乱码。
	_, _ = w.Write([]byte{0xEF, 0xBB, 0xBF})
	fmt.Fprintf(w, "# 局域网 NTP 授时统计  版本=%s  导出时间=%s\r\n",
		displayVersion(version), time.Now().Format("2006-01-02 15:04:05"))
	fmt.Fprint(w, "客户端 IP,授时次数,首次授时,最近授时\r\n")
	for _, c := range list {
		fmt.Fprintf(w, "%s,%d,%s,%s\r\n",
			c.Addr, c.Count,
			c.FirstSee.Format("2006-01-02 15:04:05"),
			c.LastSee.Format("2006-01-02 15:04:05"))
	}
}

// apiUpstreamSync 手动触发一次上游校时（仅服务端模式且已配置上游时间源）。
// 接口为同步阻塞：返回时上游状态已写回服务端，可直接读取展示。
func (h *Handler) apiUpstreamSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	if h.Server == nil {
		writeErr(w, http.StatusBadRequest, "当前为客户端模式，没有上游时间源")
		return
	}
	// 优先用界面上的最新配置，未配置时回退到启动参数里的上游。
	upstream := h.Config.Upstream
	if upstream == "" {
		upstream = h.Server.Upstream()
	}
	if upstream == "" {
		writeErr(w, http.StatusBadRequest, "未配置上游时间源，请先在参数设置中填写并保存")
		return
	}
	fn := h.upstreamSyncFunc()
	if fn == nil {
		writeErr(w, http.StatusServiceUnavailable, "上游同步未启用")
		return
	}
	fn(upstream)

	us := h.Server.UpstreamStatusSnapshot()
	if us == nil {
		writeErr(w, http.StatusInternalServerError, "同步未返回结果")
		return
	}
	if us.Synced {
		h.Logf("手动上游校时成功：%s，%s", upstream, us.Result)
	} else {
		h.Logf("手动上游校时失败：%s，%s", upstream, us.Result)
	}
	writeJSON(w, map[string]interface{}{
		"addr":    us.Addr,
		"offset":  us.Offset,
		"delay":   us.Delay,
		"result":  us.Result,
		"synced":  us.Synced,
		"last_at": us.LastAt.Format("15:04:05"),
	})
}

// offsetHint 用一句中文说明偏差方向。
//
// 只给一个「偏差 252µs」没法判断是本机快了还是慢了（符号容易看漏），
// 运维看日志时往往只需知道谁该往前调。约定与 client.Result.Offset 一致：
// 偏差为正 = 服务端时间更靠前 = 本机偏慢。
func offsetHint(d time.Duration) string {
	switch {
	case d > 0:
		return "本地偏慢"
	case d < 0:
		return "本地偏快"
	default:
		return "本地与服务端一致"
	}
}

// describeClientErr 把底层网络错误翻成一句能照着排查的中文。
//
// 直接打 %v 会得到「client: 请求服务端 1.2.3.4:123 失败: read udp
// 127.0.0.1:53043->1.2.3.4:123: i/o timeout」——地址出现两次、包名前缀
// 和 Go 的 socket 细节混在中文日志里，看的人还得自己翻译。
func describeClientErr(err error) string {
	if err == nil {
		return ""
	}
	var ne net.Error
	if errors.As(err, &ne) && ne.Timeout() {
		return "超时无应答（确认地址正确、服务端已运行、防火墙放行 UDP 123）"
	}
	msg := strings.TrimPrefix(err.Error(), "client: ")
	// 形如「请求服务端 X 失败: <底层原因>」时只保留底层原因，地址由调用方给出。
	if i := strings.Index(msg, "失败: "); i >= 0 {
		msg = msg[i+len("失败: "):]
	}
	switch {
	case strings.Contains(msg, "connection refused"):
		return "连接被拒绝（该地址上没有服务在监听）"
	case strings.Contains(msg, "no such host"):
		return "地址解析失败（主机名不存在或 DNS 不可用）"
	case strings.Contains(msg, "missing port"), strings.Contains(msg, "invalid"):
		return "地址格式不对，应形如 192.168.1.10:123"
	}
	return msg
}

// clientCheckLog 生成「检测偏差」的日志正文。
//
// 措辞与页面按钮「检测偏差」一致，不再混入英文 check/sync——
// 同一个日志面板里中英动词混排，读起来像没做完的半成品。
func clientCheckLog(server string, res *client.Result) string {
	return fmt.Sprintf("客户端检测：服务端 %s，偏差 %s（%s），往返时延 %s",
		server, res.Offset, offsetHint(res.Offset), res.Delay)
}

// clientSyncLog 生成「立即校时」的日志正文。
//
// 三种结果都带服务端地址，方便多客户端环境里对号入座；
// 跳过/无需调整时把原因原样附上（原因本身已说明偏差与阈值），不再重复拼 offset。
func clientSyncLog(server string, res *client.Result) string {
	if res.Applied {
		return fmt.Sprintf("客户端校时：服务端 %s，已设钟，偏差 %s（%s），往返时延 %s",
			server, res.Offset, offsetHint(res.Offset), res.Delay)
	}
	reason := res.Reason
	if reason == "" {
		reason = fmt.Sprintf("偏差 %s，未调整", res.Offset)
	}
	return fmt.Sprintf("客户端校时：服务端 %s，未设钟，%s", server, reason)
}

// apiClientCheck 客户端只测偏差（不改钟）。
func (h *Handler) apiClientCheck(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var body struct {
		Server string `json:"server"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效的请求体")
		return
	}
	if body.Server == "" {
		body.Server = h.Config.ServerAddr
	}
	c, err := client.New(body.Server)
	if err != nil {
		h.Logf("客户端检测失败：服务端 %s，%s", body.Server, describeClientErr(err))
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	res, err := c.Check()
	if err != nil {
		// 失败也要留痕：原来只有成功才写日志，网络不通/地址填错时
		// 日志里一片安静，事后根本查不出「点过检测、没成」。
		h.Logf("客户端检测失败：服务端 %s，%s", body.Server, describeClientErr(err))
		writeErr(w, http.StatusBadGateway, err.Error())
		return
	}
	h.Logf("%s", clientCheckLog(body.Server, res))
	writeJSON(w, map[string]interface{}{
		"server": body.Server,
		"offset": res.Offset.String(),
		"delay":  res.Delay.String(),
		"at":     res.At.Format("15:04:05"),
	})
}

// resolveClientMaxStep 决定「立即校时」这一次用多大跳变阈值。
//
// 取值顺序：请求体显式传入 → Web 已保存的配置 → 返回 0（由 client 包用默认 5 秒）。
// 空串与非法值都跳过，继续看下一级：界面上留空不该把护栏变成 0（那样等于不设防）。
func resolveClientMaxStep(bodyStep string, cfg *Config) time.Duration {
	candidates := []string{bodyStep}
	if cfg != nil {
		candidates = append(candidates, cfg.Step)
	}
	for _, s := range candidates {
		if strings.TrimSpace(s) == "" {
			continue
		}
		if d, err := duration.Parse(s); err == nil && d > 0 {
			return d
		}
	}
	return 0
}

// apiClientSync 客户端校时。
func (h *Handler) apiClientSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 POST")
		return
	}
	var body struct {
		Server string `json:"server"`
		Step   string `json:"step"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, "无效的请求体")
		return
	}
	if body.Server == "" {
		body.Server = h.Config.ServerAddr
	}
	c, err := client.New(body.Server)
	if err != nil {
		h.Logf("客户端校时失败：服务端 %s，%s", body.Server, describeClientErr(err))
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// 阈值优先级：请求显式传入 > Web 已保存的配置 > client 默认值。
	//
	// 这是一处真实踩过的坑：早先只认请求体里的 step，而前端「立即校时」只发了
	// server 没发 step，于是手动校时永远按默认 5 秒护栏走 —— 用户在参数页把阈值
	// 改成 120 秒，保存成功、后台守护也按 120 秒走，唯独点「立即校时」仍报
	// 「超过阈值 5s」，看起来像阈值根本没生效。
	// 现在后端兜一层：没传 step 就用已保存配置，避免前端漏传/旧页面缓存导致不一致。
	if d := resolveClientMaxStep(body.Step, h.Config); d > 0 {
		c.MaxStep = d
	}
	res, err := c.Sync()
	if err != nil {
		h.Logf("客户端校时失败：服务端 %s，%s", body.Server, describeClientErr(err))
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	h.Logf("%s", clientSyncLog(body.Server, res))
	writeJSON(w, map[string]interface{}{
		"offset":  res.Offset.String(),
		"delay":   res.Delay.String(),
		"applied": res.Applied,
		"skipped": res.Skipped,
		"reason":  res.Reason,
	})
}

// apiConfig 读写配置。
func (h *Handler) apiConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		// web_addr 回显「下次启动会用的地址」；当前真正在听的地址由状态接口的
		// web_addr 给出。界面自 1.11.4 起不再展示这个字段，保留在接口里供脚本读取。
		cfg := *h.Config
		if strings.TrimSpace(cfg.WebAddr) == "" {
			cfg.WebAddr = h.WebAddr
		}
		writeJSON(w, cfg)
	case http.MethodPost:
		// 解码请求体并逐项落盘。
		var body struct {
			Listen           string `json:"listen"`
			WebAddr          string `json:"web_addr"`
			ServerAddr       string `json:"server"`
			Interval         string `json:"interval"`
			Step             string `json:"step"`
			Stratum          int    `json:"stratum"`
			Upstream         string `json:"upstream"`
			UpstreamInterval string `json:"upstream_interval"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "无效的请求体")
			return
		}
		// 先算后写：所有会落盘的值都校验完再写内存。若先把 body.Listen 写进
		// 内存再校验后面的项，非法值已经被内存接受、只是没落盘，界面回显就会
		// 与磁盘上的真实配置不一致——比直接拒绝更难排查。
		listen := h.Config.Listen
		if v := strings.TrimSpace(body.Listen); v != "" {
			if err := ValidateListenAddr(v); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			listen = v
		}
		// Web 监听地址（重启生效）：访问范围固定 0.0.0.0（只对局域网开放）。
		// 界面自 1.11.4 起不再提交这个字段（端口改由 -web / 安装参数 / 配置文件
		// 决定），但接口保留兼容：提交了校验端口、主机一律规整成 0.0.0.0——
		// 访问范围是产品决定，不给外部留改的余地；端口写坏了重启后 Web 起不来、
		// 界面也连不上。
		webAddr := strings.TrimSpace(h.Config.WebAddr)
		if webAddr == "" {
			webAddr = h.WebAddr
		}
		if v := strings.TrimSpace(body.WebAddr); v != "" {
			if err := ValidateWebAddr(v); err != nil {
				writeErr(w, http.StatusBadRequest, err.Error())
				return
			}
			webAddr = LANAddr(NormalizeListenAddr(v))
		} else if webAddr != "" {
			// 没提交就不改端口，只把残留的回环地址规整成 0.0.0.0。
			webAddr = LANAddr(webAddr)
		}
		h.Config.Listen = listen
		if webAddr != "" {
			h.Config.WebAddr = webAddr
		}
		if body.ServerAddr != "" {
			h.Config.ServerAddr = body.ServerAddr
		}
		if body.Interval != "" {
			h.Config.Interval = body.Interval
		}
		if body.Step != "" {
			h.Config.Step = body.Step
		}
		if body.Stratum > 0 {
			h.Config.Stratum = body.Stratum
		}
		// 上游时间源（服务端用）：允许清空（空字符串表示回退本机硬件时钟）。
		if body.Upstream != h.Config.Upstream {
			h.Config.Upstream = body.Upstream
		}
		if body.UpstreamInterval != "" {
			// 统一用中文单位回显（输入 "90m" 也回显为 "1小时30分钟"），
			// 便于用户直观核对；解析失败则保留原始输入，由上层按默认值兜底。
			if d, e := duration.Parse(body.UpstreamInterval); e == nil && d > 0 {
				h.Config.UpstreamInterval = duration.Format(d)
			} else {
				h.Config.UpstreamInterval = body.UpstreamInterval
			}
		}
		h.Logf("配置已更新：%s", h.configSummary(h.Config))
		// 落盘并同步到运行时。失败不阻断本次生效，但要留下明确日志：
		// 否则用户会以为「保存成功」，重启后发现配置回来的是旧值。
		if h.OnSave != nil {
			if err := h.OnSave(h.Config); err != nil {
				h.Logf("⚠ 配置未能持久化，重启后将丢失：%v", err)
			}
		}
		writeJSON(w, h.Config)
	default:
		writeErr(w, http.StatusMethodNotAllowed, "仅支持 GET/POST")
	}
}

// configSummary 生成「配置已更新」日志的正文：只列出当前运行模式下真正
// 有意义的字段，值为空的整项省略。
//
// 服务端与客户端共用同一份页面与同一个 Handler，但各自只用到一半配置项：
// 服务端没有校时目标/校时间隔/校时阈值，客户端没有监听地址/层级/上游。
// 早先无条件打印全部字段，日志里就会出现 `server= interval= step=`
// 一串空值，看日志时反而要先分辨哪些才是生效的。
func (h *Handler) configSummary(c *Config) string {
	parts := make([]string, 0, 4)
	add := func(label, v string) {
		if v = strings.TrimSpace(v); v != "" {
			parts = append(parts, label+"="+v)
		}
	}
	if h.Server != nil {
		add("监听", c.Listen)
		if c.Stratum > 0 {
			add("层级", strconv.Itoa(c.Stratum))
		}
		if strings.TrimSpace(c.Upstream) == "" {
			// 上游留空是「用本机硬件时钟」的合法配置，明确写出来，
			// 免得日志里少一项让人以为没保存上。
			parts = append(parts, "上游=本机硬件时钟")
		} else {
			add("上游", c.Upstream)
			add("上游间隔", c.UpstreamInterval)
		}
	} else {
		add("校时目标", c.ServerAddr)
		add("校时间隔", c.Interval)
		add("校时阈值", c.Step)
	}
	if len(parts) == 0 {
		return "无变更字段"
	}
	return strings.Join(parts, " ")
}

// apiLogs 返回最近日志。
func (h *Handler) apiLogs(w http.ResponseWriter, r *http.Request) {
	h.mu.Lock()
	logs := make([]string, len(h.logBuf))
	copy(logs, h.logBuf)
	h.mu.Unlock()
	// 现场排障常要把日志发给别人看，提供纯文本下载（带时间戳文件名）。
	if strings.EqualFold(r.URL.Query().Get("format"), "txt") {
		name := fmt.Sprintf("ntp-logs-%s.txt", time.Now().Format("20060102-150405"))
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
		for _, l := range logs {
			fmt.Fprintln(w, l)
		}
		return
	}
	writeJSON(w, map[string]interface{}{"logs": logs})
}
