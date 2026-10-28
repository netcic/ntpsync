// Package client 实现 NTP 客户端：向服务端请求时间，计算时钟偏差，
// 在护栏（大偏差阈值）允许的范围内通过 PAL 修改本机系统时钟。
//
// 支持两种模式：
//   - 单次校时（Sync）：请求一次并落地；
//   - 只测不改（Check）：只返回偏差/时延，不修改系统时钟；
//   - 后台周期校时（由上层 cmd/ntpsync 的 daemon 子命令驱动）。
package client

import (
	"fmt"
	"net"
	"strings"
	"time"

	"ntpsync/internal/ntp"
	"ntpsync/internal/pal"
)

// DefaultMaxStep 为单次校时允许的最大跳变幅度（超过则视为异常，需人工确认）。
// 5 秒对绝大多数局域网场景足够宽松，又能挡住"服务端钟错导致全 LAN 被污染"。
const DefaultMaxStep = 5 * time.Second

// MaxStepUnlimited 表示不设跳变阈值：偏差多大都直接设钟。
//
// 仅用于「服务端向上游校时」这一条链路。护栏的初衷是保护下游客户端不被
// 一个错误的服务端拖偏；而服务端自己面对上游时没有更高的权威可依，它就是
// 这台机器的最终时间来源。若这里也套 5 秒护栏，本机钟一旦偏出 5 秒（如首次
// 部署、CMOS 电池没电、虚拟机休眠），同步会永久停在"偏差超阈值"而永不修正——
// 护栏拦住的不是错误，而是它本该修掉的那个偏差。
const MaxStepUnlimited = -1 * time.Nanosecond

// DefaultInterval 为后台守护模式的默认校时间隔。
const DefaultInterval = 15 * time.Minute

// Result 是一次校时的结果。
type Result struct {
	Offset  time.Duration // 客户端相对服务端的时钟偏差（>0 表示本地慢）
	Delay   time.Duration // 往返时延
	Applied bool          // 是否真的修改了系统时钟
	Skipped bool          // 是否因护栏/阈值而跳过改钟
	Reason  string        // 跳过/失败原因说明（可空）
	At      time.Time     // 校时时刻
}

// Client 为 NTP 客户端。
type Client struct {
	ServerAddr string        // 服务端地址，如 "192.168.1.10:123"
	Timeout    time.Duration // 单次请求超时
	// MaxStep 为最大允许跳变：>0 用该阈值；0 用 DefaultMaxStep；
	// MaxStepUnlimited（负数）表示不设阈值，偏差多大都设钟。
	MaxStep  time.Duration
	platform pal.Platform
	// query 为「向服务端测一次偏差/时延」的底层实现，默认 ntp.Query。
	// 抽成字段仅为可测性：让测试能注入确定的偏差值，从而验证护栏逻辑
	// 而无需真的改动本机系统时钟。
	query func(addr string, timeout time.Duration) (offset, delay time.Duration, err error)
}

// New 构造客户端；serverAddr 为空时返回错误。
// serverAddr 支持带端口（"192.168.1.10:123"）或纯主机（"ntp.ntsc.ac.cn"、"192.168.1.10"），
// 纯主机自动补默认 NTP 端口 123。
func New(serverAddr string) (*Client, error) {
	serverAddr = normalizeAddr(serverAddr)
	if serverAddr == "" {
		return nil, fmt.Errorf("client: 服务端地址不能为空")
	}
	return &Client{
		ServerAddr: serverAddr,
		Timeout:    3 * time.Second,
		MaxStep:    DefaultMaxStep,
		platform:   pal.NewPlatform(),
		query:      ntp.Query,
	}, nil
}

// normalizeAddr 把地址归一化为 "host:port"。纯域名/IP（无端口）补默认 NTP 端口 123。
// 带方括号的 IPv6（"[::1]）或已有端口的形式保持不变。
func normalizeAddr(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	// 已有端口（最后一个冒号后是数字），直接返回。
	if host, port, err := net.SplitHostPort(addr); err == nil {
		if port != "" {
			return net.JoinHostPort(host, port)
		}
	}
	// 纯 IPv6 不带端口（如 "::1"）：SplitHostPort 会报错，需手动补。
	if strings.Count(addr, ":") > 0 && !strings.HasPrefix(addr, "[") {
		// 可能是裸 IPv6。用 net.ParseIP 判定。
		if ip := net.ParseIP(addr); ip != nil && strings.Contains(addr, ":") {
			return net.JoinHostPort(addr, "123")
		}
	}
	// 其余情况（域名、IPv4）补默认端口。
	return net.JoinHostPort(addr, "123")
}

// maxStep 返回生效的最大跳变阈值。第二个返回值为 false 表示不设阈值
// （MaxStep 为 MaxStepUnlimited），此时调用方不应做任何拦截。
func (c *Client) maxStep() (time.Duration, bool) {
	if c.MaxStep < 0 {
		return 0, false
	}
	if c.MaxStep > 0 {
		return c.MaxStep, true
	}
	return DefaultMaxStep, true
}

// queryFn 返回实际使用的测量函数（未注入时回落 ntp.Query）。
func (c *Client) queryFn() func(addr string, timeout time.Duration) (time.Duration, time.Duration, error) {
	if c.query != nil {
		return c.query
	}
	return ntp.Query
}

// Check 只测量偏差，不修改系统时钟（安全，无需提权）。
func (c *Client) Check() (*Result, error) {
	offset, delay, err := c.queryFn()(c.ServerAddr, c.Timeout)
	if err != nil {
		return nil, fmt.Errorf("client: 请求服务端 %s 失败: %w", c.ServerAddr, err)
	}
	return &Result{
		Offset: offset,
		Delay:  delay,
		At:     time.Now(),
	}, nil
}

// Sync 执行一次校时：测偏差 → 护栏判断 → 必要时设钟。
// 设钟需要管理员/root 权限；未提权或超阈值时会返回带原因的 Result（不报硬错误）。
func (c *Client) Sync() (*Result, error) {
	res, err := c.Check()
	if err != nil {
		return nil, err
	}

	// 护栏：偏差绝对值超过阈值时拒绝盲跳，交由上层人工确认。
	// MaxStep 为 MaxStepUnlimited 时不做任何拦截（服务端向上游校时即如此）。
	if limit, guarded := c.maxStep(); guarded && abs(res.Offset) > limit {
		res.Skipped = true
		res.Reason = fmt.Sprintf("偏差 %v 超过阈值 %v，已拒绝自动设钟（防止时间源异常污染本机）",
			res.Offset, limit)
		return res, nil
	}

	// 偏差极小（<1ms）无需动钟，避免无谓提权操作。
	if abs(res.Offset) < time.Millisecond {
		res.Applied = false
		res.Skipped = true
		res.Reason = "偏差 <1ms，无需修改"
		return res, nil
	}

	if !c.platform.IsElevated() {
		res.Skipped = true
		res.Reason = "修改系统时钟需要管理员(Windows)/root(Linux)权限"
		return res, nil
	}

	now, err := c.platform.ReadSystemClock()
	if err != nil {
		return nil, fmt.Errorf("client: 读取系统时钟失败: %w", err)
	}
	newTime := now.Add(res.Offset)
	if err := c.platform.SetSystemTime(newTime); err != nil {
		return nil, fmt.Errorf("client: 设置系统时钟失败: %w", err)
	}
	res.Applied = true
	return res, nil
}

// abs 返回 duration 的绝对值。
func abs(d time.Duration) time.Duration {
	if d < 0 {
		return -d
	}
	return d
}
