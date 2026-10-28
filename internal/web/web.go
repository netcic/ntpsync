// Package web 提供内嵌的 Web 管理界面与 REST API。
//
// 前端为单文件 Vue3（Motrix 风格，深色/卡片式），通过 go:embed 内嵌，
// 本地 HTTP 服务加载，离线可用、兼容 Win7 等旧浏览器（不依赖 WebView/Electron）。
//
// REST API（均返回 JSON）：
//
//	GET  /api/status       服务端运行状态（running/stratum/uptime/requests/lastPeer/addr）
//	GET  /api/server/time  服务端当前时间（用于时间核对）
//	POST /api/client/check 客户端只测偏差（body: {server}）
//	POST /api/client/sync  客户端校时（body: {server, step?}）
//	GET  /api/config       读取配置
//	POST /api/config       保存配置（body: {server, interval, step}）
//	GET  /api/logs         最近日志
package web

import (
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
	Listen           string `json:"listen"`            // 服务端监听地址（重启生效）
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

// staticHandler 从内嵌 FS 提供 index.html 与 favicon.ico。
func (h *Handler) staticHandler() http.Handler {
	sub, _ := fs.Sub(staticFS, ".")
	return http.FileServer(http.FS(sub))
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
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

// apiServerTime 返回服务端当前时间（UTC + 本地），用于人工核对。
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
	writeJSON(w, map[string]interface{}{"clients": list})
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
	if body.Step != "" {
		if d, e := duration.Parse(body.Step); e == nil {
			c.MaxStep = d
		}
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
		writeJSON(w, h.Config)
	case http.MethodPost:
		var body Config
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			writeErr(w, http.StatusBadRequest, "无效的请求体")
			return
		}
		if body.Listen != "" {
			h.Config.Listen = body.Listen
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
	writeJSON(w, map[string]interface{}{"logs": logs})
}
