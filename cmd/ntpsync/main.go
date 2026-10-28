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
	daemonWeb := daemon.String("web", "", "同时启动本地 Web 管理界面，格式如 127.0.0.1:8090")

	install := flag.NewFlagSet("install", flag.ExitOnError)
	instServer := install.String("s", "", "服务端地址，如 127.0.0.1:123（必填）")
	instInterval := install.String("interval", "15分", "校时间隔（默认按分），如 15分/1小时/30秒")
	instStep := install.String("step", "5秒", "校时阈值：单次允许的最大跳变，如 5秒/30秒")
	instWeb := install.String("web", "", "同时启动本地 Web 管理界面，格式如 127.0.0.1:8090")
	instManual := install.Bool("manual", false, "仅安装服务不自动启动/开机自启")
	instPreserve := install.Bool("preserve", false, "配置文件已存在时保留其中参数（升级场景），只重新注册服务")

	// 无参数启动：双击（无参数）直接以常驻校时运行，
	// 服务端地址优先读同目录配置文件 SntpClient.conf（旧名 ntpsync.conf 仍兼容），
	// 缺省 127.0.0.1:123。
	if len(os.Args) < 2 {
		server := loadPortableConfig()
		log.Printf("无参数启动：常驻校时 服务端=%s", server)
		runDaemon(context.Background(), server, client.DefaultInterval, client.DefaultMaxStep, "")
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
		server, interval, step := mergeSavedClient(explicitFlags(daemon), *daemonServer, interval, step)
		if server == "" {
			log.Fatalf("请用 -s 指定服务端地址，如 127.0.0.1:123")
		}
		if err := runDaemonService(server, interval, step, *daemonWeb); err != nil {
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
		if err := installClientService(*instServer, interval, step, *instWeb, *instManual, *instPreserve); err != nil {
			log.Fatalf("安装服务失败: %v", err)
		}
	case "uninstall":
		removed, err := removeClientService(pal.NewPlatform())
		if err != nil {
			log.Fatalf("卸载服务失败: %v", err)
		}
		if removed {
			fmt.Printf("已卸载服务 %s\n", pal.ClientServiceName)
		} else {
			fmt.Printf("服务 %s 未安装，无需卸载\n", pal.ClientServiceName)
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
// 同一台机器上很可能同时装着服务端 Sntpserver，一旦删错就是把业主的授时源干掉。
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
  SntpClient daemon    -s <server> [-interval 15分] [-web 127.0.0.1:8090]   常驻后台周期校时（需管理员/root）
  SntpClient install   -s <server> [-interval 15分] [-web 127.0.0.1:8090]   注册为系统服务开机自启（需管理员/root）
  SntpClient uninstall                              卸载系统服务（需提权）
  SntpClient start / stop / status                  启动/停止/查看服务状态

参数:
  -s        服务端地址（必填），如 127.0.0.1:123
  -step     校时阈值：单次允许的最大跳变，默认 5秒，超过则拒绝自动设钟（支持 5秒/30秒/1分）
  -interval 后台校时间隔，默认 15分（支持 15分/30秒/1小时；纯数字按分）
  -web      本地 Web 管理界面地址，如 127.0.0.1:8090（默认不启用）

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

// mergeSavedClient 把磁盘上保存的客户端设置合并进来，只覆盖未显式指定的参数。
//
// 读取失败只记日志、按命令行参数继续跑：配置文件坏掉不该拦住宿主校时。
func mergeSavedClient(explicit map[string]bool, server string, interval, step time.Duration) (string, time.Duration, time.Duration) {
	c, err := config.LoadClient(clientConfigPath())
	if err != nil {
		log.Printf("⚠ 读取客户端配置失败，按命令行参数运行：%v", err)
		return server, interval, step
	}
	if !explicit["s"] && c.Server != "" {
		server = c.Server
	}
	if !explicit["interval"] && c.Interval != "" {
		if d, e := duration.Parse(c.Interval); e == nil && d > 0 {
			interval = d
		}
	}
	if !explicit["step"] && c.Step != "" {
		if d, e := duration.Parse(c.Step); e == nil && d > 0 {
			step = d
		}
	}
	return server, interval, step
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

// runDaemonService 是 daemon 子命令的入口：以「服务身份」包装常驻校时。
// 在 Windows 下若被 SCM 拉起则走 svc 接管（可被 sc start/stop/query 正确管理），
// 否则退化为前台运行（监听 Ctrl+C）。非 Windows 直接运行。
func runDaemonService(server string, interval, step time.Duration, webAddr string) error {
	return service.Run(pal.ClientServiceName, func(ctx context.Context) error {
		runDaemon(ctx, server, interval, step, webAddr)
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
	return config.SaveClient(clientConfigPath(), &config.ClientConfig{
		Server:   next.Server,
		Interval: duration.Format(next.Interval),
		Step:     duration.Format(next.Step),
	})
}

// runDaemon 常驻后台周期校时。ctx 取消（收到停止信号 / 服务停止请求）时优雅退出。
// webAddr 非空时启动本地 Web 管理界面，并把校时状态同步给界面。
func runDaemon(ctx context.Context, server string, interval time.Duration, step time.Duration, webAddr string) {
	st := &daemonSettings{cur: clientSettings{Server: server, Interval: interval, Step: step}}
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
		})
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
		webSrv = &http.Server{Addr: webAddr, Handler: mux}
		go func() {
			log.Printf("客户端 Web 管理界面已启动 地址=http://%s", webAddr)
			if err := webSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
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
	Keep bool                // true = 沿用已有配置文件，不重写
	Eff  config.ClientConfig // 最终生效的配置（Keep=true 时是沿用值）
}

// planClientInstall 决定本次安装是否沿用已有配置。
//
// 三个条件缺一不可：安装器要求保留、配置文件存在、配置能正常读出。
// 沿用是逐字段的：旧文件缺某个字段（例如老版本没写 step）时用本次参数补上，
// 不用空值去覆盖，否则服务会按空值兜底、行为和用户预期不一致。
func planClientInstall(want config.ClientConfig, preserve, exists bool,
	old *config.ClientConfig, loadErr error) clientInstallPlan {

	if !preserve || !exists || loadErr != nil || old == nil {
		return clientInstallPlan{Keep: false, Eff: want}
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
	return clientInstallPlan{Keep: true, Eff: eff}
}

// installClientService 用 PAL 把自身注册为系统服务，开机自启常驻校时。
// webAddr 非空时服务启动参数会带上 -web，让守护进程同时提供本地 Web 界面。
//
// preserve 为真且配置文件已存在时保留原配置（覆盖升级场景）：校时目标/间隔/
// 阈值都是装机后花时间配的，不该因为重装一次就回到默认 127.0.0.1:123。
func installClientService(server string, interval, step time.Duration, webAddr string, manual bool, preserve bool) error {
	if server == "" {
		return fmt.Errorf("请用 -s 指定服务端地址，如 127.0.0.1:123")
	}
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
	want := config.ClientConfig{Server: server, Interval: duration.Format(interval), Step: duration.Format(step)}
	plan := planClientInstall(want, preserve, exists, oldCfg, loadErr)
	if plan.Keep {
		fmt.Printf("检测到已有配置 %s，保留其中参数（校时目标=%s）\n", cfgPath, plan.Eff.Server)
	} else if loadErr != nil {
		fmt.Printf("读取已有配置 %s 失败（%v），改为按本次参数写入\n", cfgPath, loadErr)
	}
	if !plan.Keep {
		if err := config.SaveClient(cfgPath, &plan.Eff); err != nil {
			return fmt.Errorf("写入配置文件失败: %w", err)
		}
	}
	args := []string{"daemon"}
	if webAddr != "" {
		args = append(args, "-web", webAddr)
	}
	cfg := pal.ServiceConfig{
		Name:        pal.ClientServiceName,
		DisplayName: "SntpClient",
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
