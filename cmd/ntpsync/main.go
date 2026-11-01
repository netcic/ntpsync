// Command ntpsync 是局域网 NTP 授时系统的客户端可执行程序。
//
// 交付的执行文件叫 SntpClient（目录名 ntpsync 是 Go 包路径，不随商品名改）。
//
// 它向服务端请求时间，计算时钟偏差，在护栏允许范围内修改本机系统时钟。
// 支持三种行为：
//   - check：只测偏差/时延，不改钟（安全，无需提权）；
//   - sync：单次校时（需提权才能真实改钟）；
//   - daemon：常驻后台按间隔周期校时（需提权）。
//
// 用法：
//
//	SntpClient check  -s 192.168.1.10:123
//	SntpClient sync   -s 192.168.1.10:123 [-step 5s]
//	SntpClient daemon -s 192.168.1.10:123 [-interval 15m] [-step 5s]
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"ntpsync/internal/client"
	"ntpsync/internal/config"
	"ntpsync/internal/duration"
	"ntpsync/internal/pal"
	"ntpsync/internal/service"
	"ntpsync/internal/web"
)

// version 为本程序的版本号，构建时由 build.sh 用
// -ldflags "-X main.version=<VERSION 文件内容>" 注入。
// 保持默认值 dev：go run 或手工 go build 时界面显示「开发版」。
var version = "dev"

// uninstallClient 卸载客户端服务，并清掉本程序写入的防火墙放行与配置文件。
//
// 客户端只放行 Web 管理端口的 TCP（授时不监听端口），所以这里不碰 UDP 123：
// 同机装了服务端时，那条放行是服务端的、必须留着。
//
// 端口以配置文件为准（运维可能在界面里改过），读不到就按默认 8090 再撤一遍。
func uninstallClient(p pal.Platform, cfgPath string, keepConfig bool) error {
	removed, err := removeClientService(p)
	if err != nil {
		// 与服务端同理：服务还在就不动配置，避免它下次启动退回默认参数。
		fmt.Fprintf(os.Stderr, "警告：服务 %s 未能卸载，已跳过防火墙与配置清理——请以管理员/root 身份重试。\n", pal.ClientServiceName)
		return err
	}
	if removed {
		fmt.Printf("已卸载服务 %s\n", pal.ClientServiceName)
	} else {
		fmt.Printf("服务 %s 未安装，跳过\n", pal.ClientServiceName)
	}

	webPort := web.DefaultClientWebPort
	if c, err := config.LoadClient(cfgPath); err == nil && c != nil {
		if n := web.PortNumber(c.Web); n > 0 {
			webPort = n
		}
	}
	for _, port := range web.UniquePorts(webPort, web.DefaultClientWebPort) {
		if err := p.RemoveFirewallTCP(port); err != nil && !errors.Is(err, pal.ErrUnsupported) {
			fmt.Printf("警告：撤销 TCP %d 的防火墙放行失败：%v\n", port, err)
		}
	}

	if keepConfig {
		fmt.Printf("已保留配置 %s（-keep-config）\n", cfgPath)
		return nil
	}
	gone, err := config.RemoveDataFiles(cfgPath)
	if err != nil {
		return err
	}
	if len(gone) == 0 {
		fmt.Println("没有需要删除的配置文件")
	}
	for _, f := range gone {
		fmt.Printf("已删除 %s\n", f)
	}
	return nil
}

// displayVersion 供日志与界面复用：未注入版本时显示「开发版」。
func displayVersion() string {
	if version == "" || version == "dev" {
		return "开发版"
	}
	return "v" + strings.TrimPrefix(version, "v")
}

func main() {
	log.SetFlags(log.LstdFlags)

	check := flag.NewFlagSet("check", flag.ExitOnError)
	checkServer := check.String("s", "", "服务端地址，如 192.168.1.10:123")

	sync := flag.NewFlagSet("sync", flag.ExitOnError)
	syncServer := sync.String("s", "", "服务端地址，如 127.0.0.1:123")
	syncStep := sync.String("step", "5秒", "校时阈值：单次允许的最大跳变，超过则拒绝自动设钟，如 5秒/30秒")

	daemon := flag.NewFlagSet("daemon", flag.ExitOnError)
	daemonServer := daemon.String("s", "", "服务端地址，如 127.0.0.1:123")
	daemonInterval := daemon.String("interval", "15分", "校时间隔（默认按分），如 15分/1小时/30秒")
	daemonStep := daemon.String("step", "5秒", "校时阈值：单次允许的最大跳变，如 5秒/30秒")
	daemonWeb := daemon.String("web", "", "同时启动 Web 管理界面（只对局域网开放，默认 8090，如 -web 8090）")
	daemonWebLAN := daemon.Bool("web-lan", false, "兼容保留：Web 界面已默认局域网开放，无需再加")
	daemonWebToken := daemon.String("web-token", "", "Web 访问令牌：非空时访问需带 ?t=令牌")

	install := flag.NewFlagSet("install", flag.ExitOnError)
	instServer := install.String("s", "", "服务端地址，如 127.0.0.1:123（必填）")
	instInterval := install.String("interval", "15分", "校时间隔（默认按分），如 15分/1小时/30秒")
	instStep := install.String("step", "5秒", "校时阈值：单次允许的最大跳变，如 5秒/30秒")
	instWeb := install.String("web", "", "同时启动 Web 管理界面（只对局域网开放，默认 8090，如 -web 8090）")
	instWebLAN := install.Bool("web-lan", false, "兼容保留：Web 界面已默认局域网开放，无需再加")
	instWebToken := install.String("web-token", "", "Web 访问令牌：非空时访问需带 ?t=令牌")
	instManual := install.Bool("manual", false, "仅安装服务不自动启动/开机自启")
	instPreserve := install.Bool("preserve", false, "配置文件已存在时保留其中参数（升级场景），只重新注册服务")

	uninstall := flag.NewFlagSet("uninstall", flag.ExitOnError)
	uninstConfig := uninstall.String("config", "", "配置文件路径（默认使用系统标准位置），便于多实例与测试隔离")
	uninstKeepConfig := uninstall.Bool("keep-config", false, "只卸载服务与防火墙放行，保留配置文件")

	// 无参数启动：双击（无参数）直接以常驻校时运行，
	// 服务端地址优先读同目录配置文件 SntpClient.conf（旧名 ntpsync.conf 仍兼容），
	// 缺省 127.0.0.1:123。
	if len(os.Args) < 2 {
		server := loadPortableConfig()
		log.Printf("无参数启动：常驻校时 服务端=%s", server)
		runDaemon(context.Background(), server, client.DefaultInterval, client.DefaultMaxStep, webOptions{})
		return
	}

	switch os.Args[1] {
	case "check":
		_ = check.Parse(os.Args[2:])
		runCheck(*checkServer)
	case "sync":
		_ = sync.Parse(os.Args[2:])
		step, err := parseDurationArg("step", *syncStep, client.DefaultMaxStep)
		if err != nil {
			log.Fatalf("%v", err)
		}
		runSync(*syncServer, step)
	case "daemon":
		_ = daemon.Parse(os.Args[2:])
		interval, err := parseDurationArg("interval", *daemonInterval, client.DefaultInterval)
		if err != nil {
			log.Fatalf("%v", err)
		}
		step, err := parseDurationArg("step", *daemonStep, client.DefaultMaxStep)
		if err != nil {
			log.Fatalf("%v", err)
		}
		// 磁盘上 Web 保存过的设置作为第二配置源（命令行显式参数优先）。
		m := mergeSavedClient(explicitFlags(daemon), *daemonServer, interval, step)
		server, interval, step := m.Server, m.Interval, m.Step
		if server == "" {
			log.Fatalf("请用 -s 指定服务端地址，如 127.0.0.1:123")
		}
		// Web 监听地址：配置文件里的值优先。
		//
		// 这是「界面改的端口能生效」的前提：旧版安装把 -web 写进了服务启动参数，
		// 若还按「命令行 > 配置文件」处理，用户在界面上看到/改的端口会被老参数
		// 顶回去，界面等于白改。旧配置里的 127.0.0.1 也不用管——守护进程启动时
		// 会统一把主机规整成 0.0.0.0（Web 界面只对局域网开放）。
		webAddr, webLAN := *daemonWeb, *daemonWebLAN
		if m.Web != "" {
			if webAddr != "" && webAddr != m.Web {
				log.Printf("Web 监听地址以配置文件为准：%s（命令行 -web %s 已忽略）", m.Web, webAddr)
			}
			webAddr, webLAN = m.Web, false
		}
		if err := runDaemonService(server, interval, step, webOptions{
			Addr:  webAddr,
			LAN:   webLAN,
			Token: pickToken(*daemonWebToken, explicitFlags(daemon)["web-token"]),
		}); err != nil {
			log.Fatalf("后台校时退出: %v", err)
		}
	case "install":
		_ = install.Parse(os.Args[2:])
		interval, err := parseDurationArg("interval", *instInterval, client.DefaultInterval)
		if err != nil {
			log.Fatalf("%v", err)
		}
		step, err := parseDurationArg("step", *instStep, client.DefaultMaxStep)
		if err != nil {
			log.Fatalf("%v", err)
		}
		if err := installClientService(*instServer, interval, step, *instWeb, *instWebLAN, *instWebToken, *instManual, *instPreserve); err != nil {
			log.Fatalf("安装服务失败: %v", err)
		}
	case "uninstall":
		_ = uninstall.Parse(os.Args[2:])
		cfgPath := *uninstConfig
		if cfgPath == "" {
			cfgPath = config.DefaultClientPath()
		}
		if err := uninstallClient(pal.NewPlatform(), cfgPath, *uninstKeepConfig); err != nil {
			log.Fatalf("卸载失败: %v", err)
		}
	case "start":
		if err := pal.NewPlatform().StartService(pal.ClientServiceName); err != nil {
			log.Fatalf("启动服务失败: %v", err)
		}
		fmt.Println("服务已启动")
	case "stop":
		if err := pal.NewPlatform().StopService(pal.ClientServiceName); err != nil {
			log.Fatalf("停止服务失败: %v", err)
		}
		fmt.Println("服务已停止")
	case "status":
		st, err := pal.NewPlatform().ServiceStatus(pal.ClientServiceName)
		if err != nil {
			log.Fatalf("查询状态失败: %v", err)
		}
		fmt.Printf("服务 %s 状态: %s\n", pal.ClientServiceName, st)
	default:
		usage()
		os.Exit(2)
	}
}

// removeClientService 卸载客户端自己的系统服务。
//
// 服务名固定取 pal.ClientServiceName，不接受任何「默认值兜底」：
// 同一台机器上很可能同时装着服务端 SntpServer，一旦删错就是把业主的授时源干掉。
// 这正是本函数抽出来单独测试的原因——卸载路径要提权，没法在单测里跑真流程。
//
// removed 区分「真删掉了」和「本来就没装」：后者不是错误，重复卸载或先跑过
// 安装器卸载都应该安静退出，而不是报一个失败吓用户。
func removeClientService(p pal.Platform) (removed bool, err error) {
	if err := p.UninstallService(pal.ClientServiceName); err != nil {
		if errors.Is(err, pal.ErrServiceNotInstalled) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

func usage() {
	fmt.Fprint(os.Stderr, `SntpClient - 局域网 NTP 授时客户端

用法:
  SntpClient                                        双击/无参数：常驻校时（服务端读同目录 SntpClient.conf）
  SntpClient check     -s <server>                  只测偏差，不改钟
  SntpClient sync      -s <server> [-step 5秒]      单次校时（需管理员/root）
  SntpClient daemon    -s <server> [-interval 15分] [-web 0.0.0.0:8090]   常驻后台周期校时（需管理员/root）
  SntpClient install   -s <server> [-interval 15分] [-web 0.0.0.0:8090]   注册为系统服务开机自启（需管理员/root）
  SntpClient uninstall [-keep-config]              卸载服务，并清除防火墙放行与配置文件（需提权）
  SntpClient start / stop / status                  启动/停止/查看服务状态

参数:
  -s        服务端地址（必填），如 127.0.0.1:123
  -step     校时阈值：单次允许的最大跳变，默认 5秒，超过则拒绝自动设钟（支持 5秒/30秒/1分）
  -interval 后台校时间隔，默认 15分（支持 15分/30秒/1小时；纯数字按分）
  -web      Web 管理界面端口，默认 8090（只对局域网开放；不写则不启用），如 -web 8090
  -web-lan  允许局域网访问 Web 界面（监听改为 0.0.0.0:同端口）
  -web-token Web 访问令牌：非空时访问需带 ?t=令牌（局域网暴露时建议设置）

配置优先级: 命令行显式参数 > 配置文件 > 内置默认值
`)
}

// parseDurationArg 解析时长参数：支持中文单位（分/秒/小时/天）与标准格式（15m/5s），
// 纯数字按「分」解析。解析失败返回错误。
func parseDurationArg(name, val string, def time.Duration) (time.Duration, error) {
	if val == "" {
		return def, nil
	}
	d, err := duration.Parse(val)
	if err != nil {
		return 0, fmt.Errorf("参数 -%s 的值 %q 无法解析: %v", name, val, err)
	}
	return d, nil
}

// clientConfigPath 返回客户端配置文件路径。
// 抽成变量只为可测性：测试可临时替换为 t.TempDir()，避免读写真实 %ProgramData%。
var clientConfigPath = config.DefaultClientPath

// explicitFlags 返回命令行里「用户真正写出来」的参数名集合。
// 用于实现配置优先级：命令行显式参数 > 配置文件 > 内置默认值。
func explicitFlags(fs *flag.FlagSet) map[string]bool {
	set := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { set[f.Name] = true })
	return set
}

// mergedClient 是 mergeSavedClient 的返回：把磁盘上保存的客户端设置
// 合并命令行显式参数后的一次性结果。
type mergedClient struct {
	Server   string
	Interval time.Duration
	Step     time.Duration
	// Web 为 Web 界面的监听地址，空=未启用/未配置。主机部分不再有意义：
	// 守护进程启动时会统一规整成 0.0.0.0（只对局域网开放），只保留端口。
	Web string
}

// mergeSavedClient 把磁盘上保存的客户端设置合并进来，只覆盖未显式指定的参数。
//
// 读取失败只记日志、按命令行参数继续跑：配置文件坏掉不该拦住宿主校时。
func mergeSavedClient(explicit map[string]bool, server string, interval, step time.Duration) mergedClient {
	out := mergedClient{Server: server, Interval: interval, Step: step}
	c, err := config.LoadClient(clientConfigPath())
	if err != nil {
		log.Printf("⚠ 读取客户端配置失败，按命令行参数运行：%v", err)
		return out
	}
	if !explicit["s"] && c.Server != "" {
		out.Server = c.Server
	}
	if !explicit["interval"] && c.Interval != "" {
		if d, e := duration.Parse(c.Interval); e == nil && d > 0 {
			out.Interval = d
		}
	}
	if !explicit["step"] && c.Step != "" {
		if d, e := duration.Parse(c.Step); e == nil && d > 0 {
			out.Step = d
		}
	}
	// Web 监听地址：命令行没显式给就用配置里的（界面改「访问范围」就是改它）。
	if !explicit["web"] && c.Web != "" {
		out.Web = c.Web
	}
	return out
}

// newClient 构造客户端，缺地址时报错退出。
func newClient(server string, step time.Duration) *client.Client {
	c, err := client.New(server)
	if err != nil {
		log.Fatalf("%v（请用 -s 指定服务端地址）", err)
	}
	if step > 0 {
		c.MaxStep = step
	}
	return c
}

// runCheck 只测偏差，不改钟。
func runCheck(server string) {
	c := newClient(server, 0)
	res, err := c.Check()
	if err != nil {
		log.Fatalf("校时失败: %v", err)
	}
	fmt.Printf("服务端 %s\n  时钟偏差: %s\n  往返时延: %s\n  本地%s\n",
		c.ServerAddr, res.Offset, res.Delay, offsetHint(res.Offset))
}

// runSync 单次校时。
func runSync(server string, step time.Duration) {
	c := newClient(server, step)
	res, err := c.Sync()
	if err != nil {
		log.Fatalf("校时失败: %v", err)
	}
	switch {
	case res.Applied:
		fmt.Printf("已校时：本地时钟调整 %s（服务端 %s）\n", res.Offset, c.ServerAddr)
	case res.Skipped:
		fmt.Printf("跳过校时：%s\n", res.Reason)
	default:
		fmt.Printf("无需校时（偏差 %s）\n", res.Offset)
	}
}

// webOptions 描述 Web 管理界面的监听与访问控制。
//
// 单列一个结构体而不是再加两个参数：runDaemon / runDaemonService /
// installClientService 都已经参数不少，再散着加会把调用点写错。
type webOptions struct {
	Addr  string // 监听地址（允许只写端口；主机部分会被规整成 0.0.0.0）
	LAN   bool   // 兼容保留：Web 界面已固定局域网开放，置真与否结果相同
	Token string // 访问令牌（空=不校验）
}

// pickToken 决定最终使用的访问令牌：命令行显式给出（含显式置空）优先，
// 否则沿用配置文件里的——服务重启或覆盖升级都不该把令牌弄丢，
// 否则「昨天加了令牌，今天重启后又裸奔了」。
func pickToken(flagVal string, explicit bool) string {
	v := strings.TrimSpace(flagVal)
	if v != "" || explicit {
		return v
	}
	if c, err := config.LoadClient(clientConfigPath()); err == nil {
		return strings.TrimSpace(c.WebToken)
	}
	return ""
}

// runDaemonService 是 daemon 子命令的入口：以「服务身份」包装常驻校时。
// 在 Windows 下若被 SCM 拉起则走 svc 接管（可被 sc start/stop/query 正确管理），
// 否则退化为前台运行（监听 Ctrl+C）。非 Windows 直接运行。
func runDaemonService(server string, interval, step time.Duration, wo webOptions) error {
	return service.Run(pal.ClientServiceName, func(ctx context.Context) error {
		runDaemon(ctx, server, interval, step, wo)
		return nil
	})
}

// clientSettings 是后台校时的一组参数。运行中可被 Web 参数页改写，
// 因此收在一个结构里传递，而不是散落成 runDaemon 的局部变量。
type clientSettings struct {
	Server   string        // 校时目标
	Interval time.Duration // 校时间隔
	Step     time.Duration // 校时阈值（单次允许的最大跳变）
}

// daemonSettings 保存后台校时的当前参数，支持运行中修改。
//
// 为什么需要它：Web 参数页允许运维改校时目标/间隔/阈值。这些值早先是
// runDaemon 的局部变量，保存后只更新了 web.Config——守护循环仍按启动时的
// 旧值走，状态总览也一直是旧值，表现就是「日志说保存成功，但页面没变」。
type daemonSettings struct {
	mu  sync.Mutex
	cur clientSettings
	// webToken 为 Web 访问令牌，保存配置时原样写回（见 applyFromWeb）。
	webToken string
}

func (s *daemonSettings) get() clientSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.cur
}

// apply 用 Web 保存的配置更新参数，返回更新后真正生效的值。
// 空值/非法值一律保持原值：界面留空不该把正在跑的间隔改成 0。
func (s *daemonSettings) apply(cfg *web.Config) clientSettings {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v := strings.TrimSpace(cfg.ServerAddr); v != "" {
		s.cur.Server = v
	}
	if d, err := duration.Parse(cfg.Interval); err == nil && d > 0 {
		s.cur.Interval = d
	}
	if d, err := duration.Parse(cfg.Step); err == nil && d > 0 {
		s.cur.Step = d
	}
	return s.cur
}

// status 组装上报给界面的状态：参数用当前生效值，最近校时结果沿用 prev。
// 保存配置后推送一次，状态总览就能立刻显示新参数，不必等下一次校时才变。
func (cs clientSettings) status(prev *web.ClientStatus) web.ClientStatus {
	st := web.ClientStatus{
		ServerAddr: cs.Server,
		Interval:   duration.Format(cs.Interval),
		Step:       duration.Format(cs.Step),
	}
	if prev != nil {
		st.LastOffset = prev.LastOffset
		st.LastDelay = prev.LastDelay
		st.LastResult = prev.LastResult
		st.LastAt = prev.LastAt
	}
	return st
}

// applyFromWeb 处理 Web 保存：更新运行参数 → 推一次界面状态 → 落盘。
//
// 三步缺一不可，也正好对应三类典型的「保存没生效」：
//   - 只改内存不改守护循环 → 下一次校时仍按旧间隔/旧阈值走；
//   - 不推状态 → 状态总览要等下一次校时才变，看着就是「日志正常但页面没变」；
//   - 不落盘 → 服务重启后又回到启动参数。
//
// 返回值只表示落盘是否成功：运行参数此时已经生效，界面照实反映。
func (s *daemonSettings) applyFromWeb(cfg *web.Config, h *web.Handler) error {
	next := s.apply(cfg)
	if h != nil {
		h.SetClientStatus(next.status(h.ClientStatusSnapshot()))
	}
	// Web 监听地址（访问范围）：界面改的是「下次启动用哪个地址」，当前进程
	// 已经绑在旧地址上，所以只落盘、不热切换。h 为 nil 时按空处理。
	webAddr := ""
	if h != nil {
		webAddr = h.WebAddr
	}
	if v := strings.TrimSpace(cfg.WebAddr); v != "" {
		webAddr = web.NormalizeListenAddr(v)
	}
	return config.SaveClient(clientConfigPath(), &config.ClientConfig{
		Server:   next.Server,
		Interval: duration.Format(next.Interval),
		Step:     duration.Format(next.Step),
		// 令牌随配置写回：界面保存一次配置就丢掉令牌的话，等于自己把锁拆了。
		WebToken: s.webToken,
		Web:      webAddr,
	})
}

// runDaemon 常驻后台周期校时。ctx 取消（收到停止信号 / 服务停止请求）时优雅退出。
// wo.Addr 非空时启动本地 Web 管理界面，并把校时状态同步给界面。
func runDaemon(ctx context.Context, server string, interval time.Duration, step time.Duration, wo webOptions) {
	// 允许只写端口（-web 8090），统一补全成地址后再把主机规整成 0.0.0.0：
	// Web 界面只对局域网开放，端口沿用安装时 -web 给的值（默认 8090）。
	webAddr := web.LANAddr(web.NormalizeListenAddr(wo.Addr))
	st := &daemonSettings{cur: clientSettings{Server: server, Interval: interval, Step: step}, webToken: wo.Token}
	cur := st.get()
	log.Printf("后台校时启动：服务端=%s 间隔=%s 校时阈值=%s",
		cur.Server, duration.Format(cur.Interval), duration.Format(cur.Step))

	// wake 用于「参数刚被 Web 改过」时打断当前等待，按新间隔重新计时
	// （缓冲 1，丢信号无害：最多多等一次旧间隔）。
	wake := make(chan struct{}, 1)

	// 本地 Web 管理界面（可选）。
	var h *web.Handler
	var webSrv *http.Server
	if webAddr != "" {
		h = web.New(nil, &web.Config{
			ServerAddr: cur.Server,
			Interval:   duration.Format(cur.Interval),
			Step:       duration.Format(cur.Step),
			WebAddr:    webAddr,
		})
		h.Version = version
		h.WebAddr = webAddr
		h.LAN = web.IsLANAddr(webAddr)
		h.Token = wo.Token
		h.Logf("校时客户端已启动 版本=%s 服务端=%s", displayVersion(), cur.Server)
		// 保存配置要做三件事，缺一不可：
		//   ① 改运行参数——只改内存不改循环，保存等于没生效；
		//   ② 推一次状态——否则状态总览要等下一次校时才变（本次修复的症状）；
		//   ③ 落盘——否则重启后配置回到启动参数，用户会以为保存没成功。
		h.OnSave = func(cfg *web.Config) error {
			err := st.applyFromWeb(cfg, h)
			select {
			case wake <- struct{}{}:
			default: // 已有待处理的唤醒信号，无需重复
			}
			return err
		}
		mux := http.NewServeMux()
		h.Routes(mux)
		// 首次校时可能还在路上（最长 3 秒超时），先把参数推上去，
		// 免得刚打开页面时状态总览一片「—」看着像没配置。
		h.SetClientStatus(cur.status(nil))
		webSrv = &http.Server{Addr: webAddr, Handler: h.Guard(mux)}
		go func() {
			// 先绑定再宣布「已启动」：端口被占用时只会留下一句异常，若先打「已启动」，
			// 现场看到的是「成功 + 异常」并存，容易误判成地址填错或者界面在跑。
			ln, err := net.Listen("tcp", webAddr)
			if err != nil {
				log.Printf("客户端 Web 管理界面启动失败：无法监听 %s（%v）。端口可能已被其他程序占用——"+
					"换端口（-web 0.0.0.0:8090）或结束占用该端口的程序后重启服务；校时功能不受影响。", webAddr, err)
				return
			}
			log.Printf("客户端 Web 管理界面已启动 地址=%s", strings.Join(web.WebURLs(webAddr), "  "))
			if h.Token != "" {
				log.Printf("客户端 Web 已启用访问令牌：首次访问请在地址后加 ?t=<令牌>")
			}
			if h.LAN && h.Token == "" {
				log.Printf("⚠ 客户端 Web 只对局域网开放且未设令牌：同网段任何机器都能改校时参数，" +
					"建议加 -web-token（首次访问 http://IP:端口/?t=令牌）")
			}
			if err := webSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
				log.Printf("客户端 Web 服务异常: %v", err)
			}
		}()
	}

	// 启动先校一次。
	doSync(h, st.get())

	for {
		// 每轮现建定时器：参数改动后按新间隔无缝重排，不必处理 Stop/Reset 的竞态。
		timer := time.NewTimer(st.get().Interval)
		select {
		case <-timer.C:
			doSync(h, st.get())
		case <-wake:
			timer.Stop() // 参数变更，丢弃本轮等待
		case <-ctx.Done():
			timer.Stop()
			log.Println("收到停止信号，后台校时停止")
			if webSrv != nil {
				shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
				defer cancel()
				_ = webSrv.Shutdown(shutdownCtx)
			}
			return
		}
	}
}

// doSync 执行一次校时，并把结果同步给 Web Handler（h 非 nil 时）。
func doSync(h *web.Handler, cs clientSettings) {
	// 这里不用 newClient：地址已被 apply 拦过空值，但万一校验失败，
	// 不能让常驻循环 fatal 退出——只记一次失败、等下一轮。
	c, err := client.New(cs.Server)
	if err != nil {
		log.Printf("校时失败: %v", err)
		if h != nil {
			st := cs.status(h.ClientStatusSnapshot())
			st.LastResult = "校时失败"
			st.LastAt = time.Now()
			h.SetClientStatus(st)
		}
		return
	}
	if cs.Step > 0 {
		c.MaxStep = cs.Step
	}

	res, err := c.Sync()
	if err != nil {
		log.Printf("校时失败: %v", err)
		if h != nil {
			st := cs.status(h.ClientStatusSnapshot())
			st.LastResult = "校时失败"
			st.LastAt = time.Now()
			h.SetClientStatus(st)
		}
		return
	}
	var line string
	switch {
	case res.Applied:
		log.Printf("已校时：调整 %s", res.Offset)
		line = fmt.Sprintf("NTP 校时 · 已调整 %s", res.Offset)
	case res.Skipped:
		log.Printf("跳过：%s", res.Reason)
		line = "NTP 校时 · 已跳过"
	default:
		log.Printf("偏差 %s，无需调整", res.Offset)
		line = fmt.Sprintf("NTP 校时 · 偏差 %s", res.Offset)
	}
	if h != nil {
		st := cs.status(h.ClientStatusSnapshot())
		st.LastOffset = res.Offset.String()
		st.LastDelay = res.Delay.String()
		st.LastResult = line
		st.LastAt = res.At
		h.SetClientStatus(st)
	}
}

// offsetHint 根据偏差方向给出提示（本地慢/快）。
func offsetHint(d time.Duration) string {
	if d > 0 {
		return "时钟偏慢（落后于服务端）"
	}
	if d < 0 {
		return "时钟偏快（领先于服务端）"
	}
	return "时钟与服务端一致"
}

// clientInstallPlan 描述一次客户端安装要落地的配置。
// 纯函数，便于测试「升级时保留旧配置」这段分支——installClientService 要提权。
type clientInstallPlan struct {
	Keep bool                // true = 沿用已有配置文件，不按本次参数重写
	Eff  config.ClientConfig // 最终生效的配置（Keep=true 时是沿用值）
	// Save 非 nil 时需要落盘。全新安装写完整配置；覆盖升级一般不写，唯独
	// 旧配置缺 web 时要把补齐 web 的副本写回去（其余字段仍是用户原值）。
	Save *config.ClientConfig
}

// planClientInstall 决定本次安装是否沿用已有配置。
//
// 三个条件缺一不可：安装器要求保留、配置文件存在、配置能正常读出。
// 沿用是逐字段的：旧文件缺某个字段（例如老版本没写 step）时用本次参数补上，
// 不用空值去覆盖，否则服务会按空值兜底、行为和用户预期不一致。
func planClientInstall(want config.ClientConfig, preserve, exists bool,
	old *config.ClientConfig, loadErr error) clientInstallPlan {

	if !preserve || !exists || loadErr != nil || old == nil {
		return clientInstallPlan{Keep: false, Eff: want, Save: &want}
	}
	eff := want
	if old.Server != "" {
		eff.Server = old.Server
	}
	if old.Interval != "" {
		eff.Interval = old.Interval
	}
	if old.Step != "" {
		eff.Step = old.Step
	}
	// Web 监听地址（访问范围）同理保留：它是用户在界面上特意选的，
	// 覆盖升级时被重置回「仅本机」会让人以为设置丢了。
	if old.Web != "" {
		eff.Web = old.Web
	}
	// 旧配置里根本没有 web 字段（1.0.x / 1.10.x 装出来的配置就是这样）：
	// 必须把本次安装给的 -web 补进去并落盘。否则覆盖升级后守护进程拿不到
	// Web 地址，界面根本不会启动——而 1.11.4 起页面里已经没有改端口的地方，
	// 用户只能手改配置文件自救。补写只动 web（连同旧令牌），业务参数仍是原值。
	var save *config.ClientConfig
	if old.Web == "" && want.Web != "" {
		patched := *old
		patched.Web = want.Web
		if patched.WebToken == "" {
			patched.WebToken = want.WebToken
		}
		eff.Web = patched.Web
		eff.WebToken = patched.WebToken
		save = &patched
	}
	return clientInstallPlan{Keep: true, Eff: eff, Save: save}
}

// installClientService 用 PAL 把自身注册为系统服务，开机自启常驻校时。
// webAddr 非空时落盘到配置文件，让守护进程同时提供 Web 管理界面：
// 访问范围固定局域网（存成 0.0.0.0:端口），端口沿用 -web 给的值。
//
// preserve 为真且配置文件已存在时保留原配置（覆盖升级场景）：校时目标/间隔/
// 阈值都是装机后花时间配的，不该因为重装一次就回到默认 127.0.0.1:123。
func installClientService(server string, interval, step time.Duration, webAddr string, webLAN bool, webToken string, manual bool, preserve bool) error {
	if server == "" {
		return fmt.Errorf("请用 -s 指定服务端地址，如 127.0.0.1:123")
	}
	// Web 界面只对局域网开放：安装期就把地址存成 0.0.0.0:端口（端口沿用 -web），
	// 服务重启后仍按同一地址监听，不依赖启动参数。webLAN 形参已无意义，保留只为不改调用点。
	if strings.TrimSpace(webAddr) != "" {
		webAddr = web.LANAddr(webAddr)
	}
	webToken = strings.TrimSpace(webToken)
	p := pal.NewPlatform()
	if !p.IsElevated() {
		return fmt.Errorf("%w：请以管理员(Windows)/root(Linux)运行 `SntpClient install`", pal.ErrNotElevated)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取自身路径失败: %w", err)
	}
	startType := "auto"
	if manual {
		startType = "manual"
	}
	// 从 v1.0.x 升级：清掉旧服务 ntpsync，避免与新服务并存。
	if removed, err := pal.CleanupLegacyService(p, pal.LegacyClientServiceName); err != nil {
		log.Printf("清理旧版服务 %s 失败（不影响本次安装）：%v", pal.LegacyClientServiceName, err)
	} else if removed {
		log.Printf("已清理旧版服务 %s（v1.0.x 遗留）", pal.LegacyClientServiceName)
	}

	// 校时目标/间隔/阈值写进配置文件，而不是固化在服务启动参数里。
	// 否则 Web 参数页改完、服务一重启就被启动参数覆盖回去——「保存成功」
	// 成了一句空话。与服务端的处理方式保持一致。
	cfgPath := clientConfigPath()
	exists := config.Exists(cfgPath)
	var oldCfg *config.ClientConfig
	var loadErr error
	if preserve && exists {
		oldCfg, loadErr = config.LoadClient(cfgPath)
	}
	want := config.ClientConfig{
		Server: server, Interval: duration.Format(interval), Step: duration.Format(step),
		// 令牌随配置落盘，否则服务重启后界面又变成无鉴权。
		WebToken: webToken,
		// Web 监听地址（主机部分即访问范围）同样落盘：界面不再暴露 Web 设置
		// （1.11.4 起），端口只能靠安装参数/配置文件定；只放启动参数的话
		// 重启后会被安装时的旧值顶回去。
		Web: webAddr,
	}
	plan := planClientInstall(want, preserve, exists, oldCfg, loadErr)
	if plan.Keep {
		fmt.Printf("检测到已有配置 %s，保留其中参数（校时目标=%s）\n", cfgPath, plan.Eff.Server)
	} else if loadErr != nil {
		fmt.Printf("读取已有配置 %s 失败（%v），改为按本次参数写入\n", cfgPath, loadErr)
	}
	// 配置写成功时，Web 地址与令牌只留在配置文件里，不再塞进服务启动参数：
	// 启动参数是「显式指定」，会盖掉界面上后来改的值，让设置形同虚设。
	// 写失败才退回把参数塞进启动命令，保证服务仍能按预期参数跑起来。
	cfgSaved := false
	switch {
	case plan.Save != nil:
		if err := config.SaveClient(cfgPath, plan.Save); err != nil {
			fmt.Printf("警告：写入配置 %s 失败（%v），改为写入服务启动参数\n", cfgPath, err)
		} else {
			cfgSaved = true
			if plan.Keep {
				fmt.Printf("已补写 Web 管理界面地址 %s 到 %s（其余参数保持原值）\n", plan.Eff.Web, cfgPath)
			}
		}
	case plan.Keep:
		// 沿用旧配置且无需补写：文件里已经有完整的 web 地址。
		cfgSaved = true
	}
	args := []string{"daemon"}
	if !cfgSaved {
		if plan.Eff.Web != "" {
			args = append(args, "-web", plan.Eff.Web)
		}
		if plan.Eff.WebToken != "" {
			args = append(args, "-web-token", plan.Eff.WebToken)
		}
	}
	// 只对本机开放的旧配置会让「局域网打不开界面」：服务在跑、本机能开，别的
	// 电脑连不上。覆盖升级会原样保留它，这里提示一句，不擅自改用户的值。
	if web.IsLoopbackAddr(plan.Eff.Web) {
		fmt.Printf("提示：配置里的 Web 地址是 %s，只允许本机访问，局域网其他电脑打不开界面。\n"+
			"      需要局域网访问请执行 install -preserve -web 0.0.0.0:%s，或直接改配置文件里的 web 字段后重启服务。\n",
			plan.Eff.Web, web.PortOf(plan.Eff.Web))
	}
	cfg := pal.ServiceConfig{
		Name:        pal.ClientServiceName,
		DisplayName: pal.ClientServiceName,
		Description: "开机自启，周期性向局域网 NTP 服务端校准本机时钟",
		BinPath:     exe,
		Args:        args,
		StartType:   startType,
	}
	if err := p.InstallService(cfg); err != nil {
		return err
	}
	fmt.Printf("客户端服务已安装（%s，开机%s），服务端=%s 间隔=%s 校时阈值=%s（配置文件 %s）\n",
		cfg.DisplayName, startType, plan.Eff.Server, plan.Eff.Interval, plan.Eff.Step, cfgPath)

	// 放行 Web 端口的 TCP 入站：界面只对局域网开放，同网段的电脑要能打开它。
	// 失败只提示不影响校时——校时走 UDP 出站，不受这条规则影响。
	if wp := web.PortNumber(plan.Eff.Web); wp > 0 {
		if err := p.ConfigureFirewallTCP(wp); err != nil {
			fmt.Printf("警告：Web 端口 TCP %d 放行失败（局域网里可能打不开界面）: %v\n", wp, err)
		} else {
			fmt.Printf("防火墙已放行 TCP %d 入站（Web 管理界面）\n", wp)
		}
	}

	if !manual {
		if err := p.StartService(pal.ClientServiceName); err != nil {
			return fmt.Errorf("服务已安装但启动失败: %w", err)
		}
		fmt.Println("客户端服务已启动")
	}
	return nil
}

// loadPortableConfig 便携模式读取服务端地址。
//
// 依次尝试 exe 同目录的 SntpClient.conf、旧版遗留的 ntpsync.conf
// （内容为一行地址，如 127.0.0.1:123）；都没配到就用默认 127.0.0.1:123。
// 留着旧文件名是为了升级不断配：1.10.28 之前便携部署配的是 ntpsync.conf。
func loadPortableConfig() string {
	const def = "127.0.0.1:123"
	exe, err := os.Executable()
	if err != nil {
		return def
	}
	return portableServer(filepath.Dir(exe))
}

// portableServer 在给定目录里找服务端地址（按 portableConfNames 顺序取第一个有内容的）。
// 单独成函数是为了能单测「新名优先、旧名兜底」，不必依赖测试进程自身所在目录。
func portableServer(dir string) string {
	const def = "127.0.0.1:123"
	for _, name := range portableConfNames {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			continue
		}
		if s := cleanConfLine(string(b)); s != "" {
			return s
		}
	}
	return def
}

// portableConfNames 便携配置文件候选名，按顺序取第一个读得到且有内容的。
var portableConfNames = []string{"SntpClient.conf", "ntpsync.conf"}

// cleanConfLine 去掉首尾空白与 # 之后的注释，返回空串表示这一份没配置。
func cleanConfLine(raw string) string {
	s := strings.TrimSpace(raw)
	if i := strings.IndexByte(s, '#'); i >= 0 {
		s = strings.TrimSpace(s[:i])
	}
	return s
}
