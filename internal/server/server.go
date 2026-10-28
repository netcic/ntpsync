// Package server 实现 NTP 服务端守护：在指定 UDP 端口常驻监听，
// 用本机硬件时钟作为权威时间源，向标准 NTP/SNTP 客户端（含交换机等设备）
// 返回 RFC5905 合法报文。可被上层（命令行 / 系统服务）驱动，也可独立运行。
package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"ntpsync/internal/ntp"
)

// DefaultPort 为 NTP 标准端口。
const DefaultPort = 123

// DefaultStratum 为服务端层级：本机硬件时钟作源，通常取 2（次主钟）。
const DefaultStratum uint8 = 2

// Server 表示一个 NTP 服务端守护实例。
type Server struct {
	addr    string
	stratum uint8

	mu       sync.Mutex
	conn     *net.UDPConn
	started  time.Time
	lastPeer string // 最近一个客户端的地址
	clients  map[string]*ClientInfo
	order    []string // 按首次授时顺序，用于稳定展示

	requests atomic.Int64 // 已成功应答的请求数

	// 上游时间源同步（可选）：配置后周期性向上游 NTP 校时，把本机时钟锚定到上游。
	upstream         string        // 上游 NTP 地址（空表示用本机硬件时钟）
	upstreamInterval time.Duration // 上游同步间隔
	upstreamMu       sync.Mutex
	upstreamStatus   *UpstreamStatus // 最近一次上游同步结果
	upstreamCancel   context.CancelFunc
	// upstreamWake 用于在配置变更时立即唤醒同步循环，
	// 避免用户改完地址还要等一个轮询周期才生效。
	upstreamWake chan struct{}
}

// UpstreamStatus 描述最近一次向上游同步的状态。
type UpstreamStatus struct {
	Addr   string    `json:"addr"`    // 上游地址
	Offset string    `json:"offset"`  // 时钟偏差
	Delay  string    `json:"delay"`   // 往返时延
	Result string    `json:"result"`  // 结果描述（已同步/失败原因）
	LastAt time.Time `json:"last_at"` // 最近一次同步时刻
	Synced bool      `json:"synced"`  // 最近一次是否成功
}

// maxClients 为客户端列表保留上限（按 IP 计，超出时淘汰最早接入的）。
const maxClients = 50

// ClientInfo 描述一个授时客户端的统计信息。
//
// 统计粒度是 IP，不是 ip:port：客户端每次校时都会换一个源端口
// （Go 客户端每轮新建 socket、Windows 客户端每次 daemon 循环亦然），
// 按 ip:port 记账会让同一个客户端在列表里占 N 行、每行次数永远是 1。
type ClientInfo struct {
	Addr     string    `json:"addr"`      // 客户端 IP（不含端口）
	FirstSee time.Time `json:"first_see"` // 首次授时时刻
	LastSee  time.Time `json:"last_see"`  // 最近一次授时时刻
	Count    int64     `json:"count"`     // 累计授时次数（同一 IP 累加）
}

// noteClient 记录一次成功应答的客户端，按 IP 聚合成一行。
//
// 抽成独立方法是为了能单测聚合逻辑：真正收发包要走真实 UDP 端口，
// 而「同一 IP 不同源端口必须合并、次数必须累加」正是这个列表的核心契约。
func (s *Server) noteClient(addr *net.UDPAddr, now time.Time) {
	if addr == nil {
		return
	}
	key := addr.IP.String()
	s.mu.Lock()
	defer s.mu.Unlock()
	// 最近对端保留端口，排查「到底是谁连过来的」时有用；
	// 列表行本身只展示 IP。
	s.lastPeer = addr.String()
	if s.clients == nil {
		s.clients = make(map[string]*ClientInfo)
	}
	if ci, ok := s.clients[key]; ok {
		ci.LastSee = now
		ci.Count++
		return
	}
	s.clients[key] = &ClientInfo{Addr: key, FirstSee: now, LastSee: now, Count: 1}
	s.order = append(s.order, key)
	// 超上限时淘汰最早记录的客户端（按首次授时先后）。
	if len(s.order) > maxClients {
		old := s.order[0]
		s.order = s.order[1:]
		delete(s.clients, old)
	}
}

// Clients 返回客户端列表快照（按最近授时时间倒序）。
func (s *Server) Clients() []ClientInfo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ClientInfo, 0, len(s.clients))
	for _, ci := range s.clients {
		out = append(out, *ci)
	}
	// 最近授时的排前面。
	for i := 0; i < len(out); i++ {
		for j := i + 1; j < len(out); j++ {
			if out[j].LastSee.After(out[i].LastSee) {
				out[i], out[j] = out[j], out[i]
			}
		}
	}
	return out
}

// Config 为服务端配置。
type Config struct {
	// Listen 监听地址，如 "0.0.0.0:123"；留空默认 "0.0.0.0:123"。
	Listen string
	// Stratum 层级；0 表示用默认值（2）。
	Stratum uint8
	// Upstream 上游 NTP 时间源地址（如 "ntp.ntsc.ac.cn" 或 "192.168.1.1:123"）；
	// 留空表示以本机硬件时钟为权威源（不向上游同步）。
	Upstream string
	// UpstreamInterval 上游同步间隔；0 表示用默认值（1 小时）。
	UpstreamInterval time.Duration
}

// DefaultUpstreamInterval 为上游同步的默认间隔。
const DefaultUpstreamInterval = time.Hour

// New 构造一个服务端（尚未监听）。
func New(cfg Config) *Server {
	listen := cfg.Listen
	if listen == "" {
		listen = fmt.Sprintf("0.0.0.0:%d", DefaultPort)
	}
	stratum := cfg.Stratum
	if stratum == 0 {
		stratum = DefaultStratum
	}
	upstream := cfg.Upstream
	interval := cfg.UpstreamInterval
	if interval <= 0 {
		interval = DefaultUpstreamInterval
	}
	return &Server{
		addr:             listen,
		stratum:          stratum,
		clients:          make(map[string]*ClientInfo),
		upstream:         upstream,
		upstreamInterval: interval,
		upstreamWake:     make(chan struct{}, 1),
	}
}

// Addr 返回监听地址。
func (s *Server) Addr() string { return s.addr }

// LocalAddr 返回实际绑定的地址（端口为 0 时用其拿到系统分配的真实端口）。
// 未启动时返回空串。
//
// 注意：这是"可连接"的真实地址，可能形如 "[::]:123"（IPv6 双栈通配）。
// 仅用于展示请改用 DisplayAddr。
func (s *Server) LocalAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return ""
	}
	return s.conn.LocalAddr().String()
}

// DisplayAddr 返回适合展示给用户的监听地址（已做通配地址归一化）。
// 未启动时返回空串。
//
// 为什么需要归一化：以 0.0.0.0 通配监听时，Go 在 Windows/Linux 上会创建
// IPv6 双栈 socket，LocalAddr() 返回 "[::]:123"。这个值虽然真实，但对用户
// 而言形如"仅监听 IPv6"，会误导人以为 IPv4 客户端连不上（实际双栈可达）。
// 故通配地址统一展示为 "0.0.0.0:123"，与配置里的写法保持一致。
func (s *Server) DisplayAddr() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return ""
	}
	return normalizeListenAddr(s.conn.LocalAddr())
}

// normalizeListenAddr 把 net.Addr 归一化为用户友好的 "host:port"。
// 通配地址（0.0.0.0 / ::）统一显示为 0.0.0.0，避免出现 "[::]:123" 这类误导写法；
// 绑定到具体 IP 时原样返回。
func normalizeListenAddr(a net.Addr) string {
	if a == nil {
		return ""
	}
	ua, ok := a.(*net.UDPAddr)
	if !ok {
		return a.String()
	}
	if ua.IP == nil || ua.IP.IsUnspecified() {
		return net.JoinHostPort("0.0.0.0", strconv.Itoa(ua.Port))
	}
	return net.JoinHostPort(ua.IP.String(), strconv.Itoa(ua.Port))
}

// LocalIPs 返回本机可用于对外提供授时服务的 IPv4 地址列表。
// 排除回环（127.0.0.0/8）与链路本地（169.254.0.0/16，Windows 上多为未连接的
// 虚拟网卡），结果去重并升序排列。用于在界面上提示"客户端该填哪个地址"。
func LocalIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	seen := make(map[string]struct{})
	var out []string
	for _, ifc := range ifaces {
		// 跳过已关闭的网卡与回环网卡。
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, ad := range addrs {
			ipn, ok := ad.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || ip4.IsLoopback() || ip4.IsLinkLocalUnicast() {
				continue
			}
			s := ip4.String()
			if _, dup := seen[s]; dup {
				continue
			}
			seen[s] = struct{}{}
			out = append(out, s)
		}
	}
	sort.Strings(out)
	return out
}

// Stratum 返回服务端层级。
func (s *Server) Stratum() uint8 { return s.stratum }

// Start 绑定 UDP 端口并在后台开始处理请求（非阻塞）。
func (s *Server) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn != nil {
		return errors.New("server: 已在运行")
	}
	udpAddr, err := net.ResolveUDPAddr("udp", s.addr)
	if err != nil {
		return fmt.Errorf("server: 解析地址失败: %w", err)
	}
	conn, err := net.ListenUDP("udp", udpAddr)
	if err != nil {
		return fmt.Errorf("server: 监听 %s 失败: %w", s.addr, err)
	}
	s.conn = conn
	s.started = time.Now()
	// 后台跑收发循环；RunForeground 在 conn 关闭时返回，并负责统计与记录最近客户端。
	go func() { _ = s.RunForeground(conn) }()
	return nil
}

// Stop 关闭监听，使后台循环退出。
func (s *Server) Stop() error {
	s.stopUpstreamSync()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conn == nil {
		return nil
	}
	err := s.conn.Close()
	s.conn = nil
	return err
}

// Upstream 返回上游时间源地址（空表示用本机硬件时钟）。
func (s *Server) Upstream() string {
	s.upstreamMu.Lock()
	defer s.upstreamMu.Unlock()
	return s.upstream
}

// SetUpstream 运行时切换上游时间源（空表示停用，改用本机硬件时钟）。
//
// 为什么需要它：Web 参数页允许运维改上游地址。若只能靠重启服务生效，
// 界面上「保存成功」就会变成一句空话——改动既不落到后台循环，重启后还会丢。
func (s *Server) SetUpstream(addr string) {
	s.upstreamMu.Lock()
	changed := s.upstream != addr
	s.upstream = addr
	s.upstreamMu.Unlock()
	if changed {
		s.wakeUpstream()
	}
}

// wakeUpstream 非阻塞地通知同步循环「配置变了，立即重新评估」。
// 同时有多处变更时只保留一次唤醒即可，故用容量 1 的缓冲通道。
func (s *Server) wakeUpstream() {
	select {
	case s.upstreamWake <- struct{}{}:
	default:
	}
}

// UpstreamInterval 返回上游同步间隔。
func (s *Server) UpstreamInterval() time.Duration {
	s.upstreamMu.Lock()
	defer s.upstreamMu.Unlock()
	if s.upstreamInterval <= 0 {
		return DefaultUpstreamInterval
	}
	return s.upstreamInterval
}

// SetUpstreamInterval 运行时调整上游同步间隔（<=0 时回落默认值）。
func (s *Server) SetUpstreamInterval(d time.Duration) {
	if d <= 0 {
		d = DefaultUpstreamInterval
	}
	s.upstreamMu.Lock()
	s.upstreamInterval = d
	s.upstreamMu.Unlock()
	// 间隔变了，轮询粒度也要重算，立即唤醒一次。
	s.wakeUpstream()
}

// UpstreamStatusSnapshot 返回最近一次上游同步状态（可能为 nil）。
func (s *Server) UpstreamStatusSnapshot() *UpstreamStatus {
	s.upstreamMu.Lock()
	defer s.upstreamMu.Unlock()
	if s.upstreamStatus == nil {
		return nil
	}
	cp := *s.upstreamStatus
	return &cp
}

// SetUpstreamStatus 由上层（执行同步的调用方）写入最近一次上游同步状态。
func (s *Server) SetUpstreamStatus(st *UpstreamStatus) {
	if st == nil {
		return
	}
	s.upstreamMu.Lock()
	s.upstreamStatus = st
	s.upstreamMu.Unlock()
}

// StartUpstreamSync 启动上游同步后台循环。
// syncFn 为每次同步要执行的回调，由上层注入（避免 server 依赖 client 包）。
//
// 注意：即便启动时未配置上游也会启动循环。因为 Web 参数页允许在运行期补配
// 上游，循环每轮重新读取当前配置，配置为空时静默跳过——否则「先起服务、
// 后填上游」这一常见操作将永远不生效。
func (s *Server) StartUpstreamSync(syncFn func()) {
	if syncFn == nil {
		return
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.upstreamMu.Lock()
	s.upstreamCancel = cancel
	s.upstreamMu.Unlock()
	go s.upstreamLoop(ctx, syncFn)
}

// stopUpstreamSync 停止上游同步循环。
func (s *Server) stopUpstreamSync() {
	s.upstreamMu.Lock()
	cancel := s.upstreamCancel
	s.upstreamCancel = nil
	s.upstreamMu.Unlock()
	if cancel != nil {
		cancel()
	}
}

// upstreamLoop 周期性调用 syncFn 向上游校时。
//
// 每一轮都重新读取上游地址与间隔，因此运行期修改配置无需重启即可生效；
// 地址发生变化时立即触发一次同步，避免运维改完还要干等一个周期。
func (s *Server) upstreamLoop(ctx context.Context, syncFn func()) {
	var lastSync time.Time
	var lastAddr string

	for {
		s.upstreamMu.Lock()
		addr := s.upstream
		interval := s.upstreamInterval
		s.upstreamMu.Unlock()
		if interval <= 0 {
			interval = DefaultUpstreamInterval
		}

		// 首次、换地址、或到达间隔时同步一次。
		due := lastSync.IsZero() || addr != lastAddr || time.Since(lastSync) >= interval
		if addr != "" && due {
			syncFn()
			lastSync = time.Now()
			lastAddr = addr
		}

		// 轮询粒度取间隔的 1/10，限制在 [1s, 30s]：既能及时发现配置变更，
		// 又不会因空转产生可观 CPU 开销。配置变更另有 wake 通道立即打断等待。
		tick := interval / 10
		if tick < time.Second {
			tick = time.Second
		}
		if tick > 30*time.Second {
			tick = 30 * time.Second
		}

		select {
		case <-time.After(tick):
		case <-s.upstreamWake:
			// 配置变更，立即进入下一轮重新读取。
		case <-ctx.Done():
			return
		}
	}
}

// IsRunning 报告服务端是否在监听。
func (s *Server) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.conn != nil
}

// Uptime 返回自启动以来的运行时长（未启动则为 0）。
func (s *Server) Uptime() time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.started.IsZero() {
		return 0
	}
	return time.Since(s.started)
}

// Requests 返回已成功应答的请求总数。
func (s *Server) Requests() int64 { return s.requests.Load() }

// RunForeground 在传入的连接上同步跑收发循环（阻塞），
// 供已由调用方建好的连接复用（主要用于测试与 systemd 直接接管场景）。
// 每成功应答一次，统计计数 +1 并记录最近客户端。
func (s *Server) RunForeground(conn *net.UDPConn) error {
	buf := make([]byte, 1024)
	for {
		n, addr, err := conn.ReadFromUDP(buf)
		if err != nil {
			return err
		}
		if n < 48 {
			continue
		}
		resp, err := ntp.HandleRequest(buf[:n], time.Now(), s.stratum)
		if err != nil {
			continue
		}
		if _, err := conn.WriteToUDP(resp, addr); err != nil {
			continue
		}
		s.requests.Add(1)
		s.noteClient(addr, time.Now())
	}
}

// LastPeer 返回最近一个客户端的地址（无则为空串）。
func (s *Server) LastPeer() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.lastPeer
}
