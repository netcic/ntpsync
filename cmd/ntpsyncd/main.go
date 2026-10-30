// Command ntpsyncd 是局域网 NTP 授时系统的服务端可执行程序。
//
// 交付的执行文件叫 Sntpserver（目录名 ntpsyncd 是 Go 包路径，不随商品名改）。
//
// 它把本机硬件时钟作为权威时间源，在 UDP 123 端口常驻监听，
// 向标准 NTP/SNTP 客户端（含交换机等设备）返回 RFC5905 合法报文。
//
// 用法：
//
//	Sntpserver run [-l 0.0.0.0:123] [-s 2]      # 前台运行（调试/守护托管）
//	Sntpserver install [-l 0.0.0.0:123] [-s 2]  # 注册为系统服务并自启（需提权）
//	Sntpserver uninstall                        # 卸载系统服务（需提权）
//	Sntpserver start                            # 启动系统服务
//	Sntpserver stop                             # 停止系统服务
//	Sntpserver status                           # 查看服务状态
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
	"strconv"
	"strings"
	"time"

	"ntpsync/internal/client"
	"ntpsync/internal/config"
	"ntpsync/internal/duration"
	"ntpsync/internal/pal"
	"ntpsync/internal/server"
	"ntpsync/internal/service"
	"ntpsync/internal/web"
)

// version 为本程序的版本号，构建时由 build.sh 用
// -ldflags "-X main.version=<VERSION 文件内容>" 注入。
// 保持默认值 dev：go run 或手工 go build 时界面显示「开发版」，
// 不会把一个假版本号当成真版本显示给现场。
var version = "dev"

// serverOptions 汇总 run 子命令的运行参数。
// 取值优先级：命令行显式参数 > 配置文件 > 内置默认值。
type serverOptions struct {
	Listen           string
	Stratum          uint8
	Web              string
	Upstream         string
	UpstreamInterval time.Duration
	ConfigPath       string // 持久化文件路径
}

func main() {
	log.SetFlags(log.LstdFlags)

	run := flag.NewFlagSet("run", flag.ExitOnError)
	runListen := run.String("l", "0.0.0.0:123", "监听地址")
	runStratum := run.Int("s", int(server.DefaultStratum), "NTP 层级(stratum)")
	runWeb := run.String("web", "", "同时启动 Web 管理界面，格式如 127.0.0.1:8080")
	runUpstream := run.String("upstream", "", "上游 NTP 时间源地址（如 ntp.ntsc.ac.cn），留空则用本机硬件时钟")
	runUpstreamInterval := run.String("upstream-interval", "1小时", "向上游同步的间隔（默认 1小时）")
	runConfigPath := run.String("config", "", "配置文件路径（默认使用系统标准位置），便于多实例与测试隔离")

	install := flag.NewFlagSet("install", flag.ExitOnError)
	instListen := install.String("l", "0.0.0.0:123", "监听地址")
	instStratum := install.Int("s", int(server.DefaultStratum), "NTP 层级(stratum)")
	instManual := install.Bool("manual", false, "仅安装服务不自动启动/开机自启")
	instWeb := install.String("web", "", "同时启动 Web 管理界面，格式如 127.0.0.1:8080")
	instNoFirewall := install.Bool("no-firewall", false, "跳过防火墙放行")
	instUpstream := install.String("upstream", "", "上游 NTP 时间源地址，留空则用本机硬件时钟")
	instUpstreamInterval := install.String("upstream-interval", "1小时", "向上游同步的间隔（默认 1小时）")
	instPreserve := install.Bool("preserve", false, "配置文件已存在时保留其中参数（升级场景），只重新注册服务")

	// 无参数启动：双击（无参数）直接前台运行 + Web 界面，
	// 让普通用户无需命令行即可使用。仍支持 run/install 等显式子命令。
	if len(os.Args) < 2 {
		log.Println("无参数启动：自动以前台 + Web 界面运行（Ctrl+C 退出）")
		opts := serverOptions{
			Listen:           "0.0.0.0:123",
			Stratum:          server.DefaultStratum,
			Web:              "127.0.0.1:8080",
			UpstreamInterval: server.DefaultUpstreamInterval,
			ConfigPath:       config.DefaultPath(),
		}
		opts = mergeSavedConfig(opts, nil)
		_ = runServer(context.Background(), opts)
		return
	}

	switch os.Args[1] {
	case "run":
		_ = run.Parse(os.Args[2:])
		interval, err := parseDurArg("upstream-interval", *runUpstreamInterval, server.DefaultUpstreamInterval)
		if err != nil {
			log.Fatalf("%v", err)
		}
		// 记录哪些参数由命令行显式给出——只有这些才允许覆盖配置文件。
		explicit := map[string]bool{}
		run.Visit(func(f *flag.Flag) { explicit[f.Name] = true })

		cfgPath := *runConfigPath
		if cfgPath == "" {
			cfgPath = config.DefaultPath()
		}

		opts := mergeSavedConfig(serverOptions{
			Listen:           *runListen,
			Stratum:          uint8(*runStratum),
			Web:              *runWeb,
			Upstream:         *runUpstream,
			UpstreamInterval: interval,
			ConfigPath:       cfgPath,
		}, explicit)
		if err := runServerService(opts); err != nil {
			log.Fatalf("服务端退出: %v", err)
		}
	case "install":
		_ = install.Parse(os.Args[2:])
		interval, err := parseDurArg("upstream-interval", *instUpstreamInterval, server.DefaultUpstreamInterval)
		if err != nil {
			log.Fatalf("%v", err)
		}
		if err := installService(*instListen, uint8(*instStratum), *instManual, *instWeb, *instNoFirewall, *instUpstream, interval, *instPreserve); err != nil {
			log.Fatalf("安装服务失败: %v", err)
		}
	case "uninstall":
		// 服务名必须显式传 pal.DefaultServiceName：本机可能同时装了客户端
		// （SntpClient），让 PAL 去猜「默认服务名」就会顺手把别人的服务删掉。
		removed, err := removeServerService(pal.NewPlatform())
		if err != nil {
			log.Fatalf("卸载服务失败: %v", err)
		}
		if removed {
			fmt.Printf("已卸载服务 %s\n", pal.DefaultServiceName)
		} else {
			fmt.Printf("服务 %s 未安装，无需卸载\n", pal.DefaultServiceName)
		}
	case "start":
		if err := pal.NewPlatform().StartService(pal.DefaultServiceName); err != nil {
			log.Fatalf("启动服务失败: %v", err)
		}
		fmt.Println("服务已启动")
	case "stop":
		if err := pal.NewPlatform().StopService(pal.DefaultServiceName); err != nil {
			log.Fatalf("停止服务失败: %v", err)
		}
		fmt.Println("服务已停止")
	case "status":
		st, err := pal.NewPlatform().ServiceStatus(pal.DefaultServiceName)
		if err != nil {
			log.Fatalf("查询状态失败: %v", err)
		}
		fmt.Printf("服务 %s 状态: %s\n", pal.DefaultServiceName, st)
	default:
		usage()
		os.Exit(2)
	}
}

// removeServerService 卸载服务端自己的系统服务。
//
// 服务名固定为 pal.DefaultServiceName，绝不走「默认值兜底」——兜底恰恰是
// 早期 bug 的来源：客户端命令在全新进程里拿到空名字，回落成服务端的服务名，
// 把服务端的服务删了个干净。
//
// removed 区分「真删掉了」与「本来就没装」：后者不是错误，重复卸载应安静退出。
func removeServerService(p pal.Platform) (removed bool, err error) {
	if err := p.UninstallService(pal.DefaultServiceName); err != nil {
		if errors.Is(err, pal.ErrServiceNotInstalled) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// displayVersion 供日志与界面复用：未注入版本时显示「开发版」。
func displayVersion() string {
	if version == "" || version == "dev" {
		return "开发版"
	}
	return "v" + strings.TrimPrefix(version, "v")
}

// clientStatsPath 授时统计文件的路径：与配置文件同目录，便于一起备份/迁移。
// 文件名带产品名前缀，与客户端（若同机）区分开。
func clientStatsPath(cfgPath string) string {
	dir := filepath.Dir(cfgPath)
	if dir == "" || dir == "." {
		dir = "."
	}
	return filepath.Join(dir, "Sntpserver-clients.json")
}

func usage() {
	fmt.Fprint(os.Stderr, `Sntpserver - 局域网 NTP 授时服务端

用法:
  Sntpserver                                       双击/无参数：前台 + Web 界面
  Sntpserver run      [-l 0.0.0.0:123] [-s 2] [-web 127.0.0.1:8080] [-upstream ntp.ntsc.ac.cn]   前台运行
  Sntpserver install  [-l 0.0.0.0:123] [-s 2] [-web 127.0.0.1:8080] [-upstream ntp.ntsc.ac.cn]   注册为系统服务（需管理员/root）
  Sntpserver uninstall                             卸载服务（需提权）
  Sntpserver start                                 启动服务
  Sntpserver stop                                  停止服务
  Sntpserver status                                查看服务状态

参数:
  -l                  监听地址，默认 0.0.0.0:123
  -s                  NTP 层级(stratum)，默认 2
  -web                本地 Web 界面地址，如 127.0.0.1:8080（默认不启用）
  -upstream           上游 NTP 时间源（如 ntp.ntsc.ac.cn），留空则用本机硬件时钟
  -upstream-interval  向上游同步间隔，默认 1小时（支持 30分/1小时/2小时）
`)
}

// mergeSavedConfig 把持久化配置合并进运行参数。
//
// 优先级：命令行显式参数 > 配置文件 > 传入的默认值。
// explicit 记录哪些 flag 被显式指定；传 nil 表示没有命令行（双击启动）。
// 配置文件读不出来时只记日志并继续，绝不让一个坏文件把服务拦住。
func mergeSavedConfig(opts serverOptions, explicit map[string]bool) serverOptions {
	saved, err := config.Load(opts.ConfigPath)
	if err != nil {
		log.Printf("读取配置失败（改为按命令行/默认值运行）：%v", err)
		return opts
	}
	if !explicit["l"] && saved.Listen != "" {
		opts.Listen = saved.Listen
	}
	if !explicit["s"] && saved.Stratum > 0 {
		opts.Stratum = uint8(saved.Stratum)
	}
	if !explicit["web"] && saved.Web != "" {
		opts.Web = saved.Web
	}
	if !explicit["upstream"] {
		opts.Upstream = saved.Upstream
	}
	if !explicit["upstream-interval"] && saved.UpstreamInterval != "" {
		if d, e := parseDurArg("upstream-interval", saved.UpstreamInterval, server.DefaultUpstreamInterval); e == nil {
			opts.UpstreamInterval = d
		} else {
			log.Printf("配置中的上游同步间隔非法，改用默认值：%v", e)
		}
	}
	return opts
}

// runServerService 是 run 子命令的入口：以「服务身份」包装授时服务。
// Windows 下被 SCM 拉起时走 svc 接管（可被 sc start/stop/query 正确管理），
// 否则退化为前台运行（监听 Ctrl+C）。非 Windows 直接运行。
func runServerService(opts serverOptions) error {
	return service.Run(pal.DefaultServiceName, func(ctx context.Context) error {
		return runServer(ctx, opts)
	})
}

// runServer 运行服务端。ctx 取消（收到停止信号 / 服务停止请求）时优雅退出。
// opts.Web 非空时同时启动内嵌 Web 管理界面；opts.Upstream 非空时周期性向上游同步。
func runServer(ctx context.Context, opts serverOptions) error {
	s := server.New(server.Config{
		Listen:           opts.Listen,
		Stratum:          opts.Stratum,
		Upstream:         opts.Upstream,
		UpstreamInterval: opts.UpstreamInterval,
	})
	// 授时统计落盘：服务重启不清零，现场才能看出某台设备是否在按时校时。
	// 恢复失败只记日志——统计数据丢得起，授时本身不能因此起不来。
	statsPath := clientStatsPath(opts.ConfigPath)
	if err := s.LoadClients(statsPath); err != nil {
		log.Printf("读取授时统计失败（本次从零开始累计）：%v", err)
	}
	// 定时保存：写盘频率与授时频率解耦，避免把 IO 压力压到 NTP 应答路径上。
	statsDone := make(chan struct{})
	go func() {
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		defer close(statsDone)
		for {
			select {
			case <-ctx.Done():
				_ = s.SaveClients(statsPath)
				return
			case <-ticker.C:
				if err := s.SaveClients(statsPath); err != nil {
					log.Printf("保存授时统计失败：%v", err)
				}
			}
		}
	}()
	if err := s.Start(); err != nil {
		return err
	}
	defer func() {
		s.Stop()
		// 退出前再存一次，把最后半分钟内的计数也留住。
		if err := s.SaveClients(statsPath); err != nil {
			log.Printf("退出前保存授时统计失败：%v", err)
		}
		<-statsDone
	}()
	// 上游同步循环无条件启动：Web 参数页可在运行期补配上游，循环每轮重读配置。
	// 同步地址用闭包动态取 s.Upstream()，保证界面改完立即按新地址校时。
	s.StartUpstreamSync(func() {
		syncUpstreamOnce(s, s.Upstream())
	})
	if opts.Upstream != "" {
		log.Printf("NTP 服务端已启动 监听=%s stratum=%d 上游=%s 同步间隔=%s",
			s.Addr(), s.Stratum(), opts.Upstream, duration.Format(opts.UpstreamInterval))
	} else {
		log.Printf("NTP 服务端已启动 监听=%s stratum=%d（本机硬件时钟）", s.Addr(), s.Stratum())
	}

	// 可选：启动 Web 管理界面。
	var h *web.Handler
	var webSrv *http.Server
	opts.Web = web.NormalizeListenAddr(opts.Web)
	if opts.Web != "" {
		webCfg := &web.Config{
			Listen:  opts.Listen,
			Stratum: int(opts.Stratum),
			// 用中文单位回显（1小时），避免界面出现 "1h0m0s"。
			Upstream:         opts.Upstream,
			UpstreamInterval: duration.Format(opts.UpstreamInterval),
		}
		h = web.New(s, webCfg)
		h.Version = version
		// 界面日志缓冲只收 Logf 的内容，补一条启动记录，避免「下载日志」下到空文件。
		h.Logf("NTP 服务端已启动 版本=%s 监听=%s stratum=%d", displayVersion(), s.Addr(), s.Stratum())
		// 注入「立即同步」执行函数：供 Web 参数页的手动校时按钮调用。
		// 地址由 Web 侧传入（可能是刚改完还没重启的新地址），无返回值——
		// 成功/失败详情统一写入服务端上游状态，由接口读取后回显。
		h.SetUpstreamSync(func(addr string) {
			syncUpstreamOnce(s, addr)
		})
		// 保存回调：落盘 + 同步到运行时。
		// 上游与间隔可即时生效；监听地址/层级需重启（界面已标注）。
		h.OnSave = func(cfg *web.Config) error {
			s.SetUpstream(cfg.Upstream)
			if d, e := parseDurArg("upstream-interval", cfg.UpstreamInterval, server.DefaultUpstreamInterval); e == nil {
				s.SetUpstreamInterval(d)
			}
			return config.Save(opts.ConfigPath, &config.ServerConfig{
				Listen:           cfg.Listen,
				Stratum:          cfg.Stratum,
				Web:              opts.Web,
				Upstream:         cfg.Upstream,
				UpstreamInterval: cfg.UpstreamInterval,
			})
		}
		mux := http.NewServeMux()
		h.Routes(mux)
		webSrv = &http.Server{Addr: opts.Web, Handler: mux}
		go func() {
			log.Printf("Web 管理界面已启动 地址=http://%s", opts.Web)
			if err := webSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
				log.Printf("Web 服务异常: %v", err)
			}
		}()
	}

	<-ctx.Done()

	log.Println("收到停止信号，正在关闭…")
	if webSrv != nil {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = webSrv.Shutdown(shutdownCtx)
	}
	return s.Stop()
}

// serverInstallPlan 描述一次服务安装要落地的内容。
//
// 抽成纯函数是为了可测：installService 需要提权，测试环境里跑不起来，
// 而「要不要保留旧配置」这段分支恰恰是覆盖升级时最容易出错的地方，
// 必须能单独断言，不能只靠人眼审代码。
type serverInstallPlan struct {
	Keep   bool                 // true = 沿用已有配置文件，不写文件、不塞启动参数
	Listen string               // 最终生效的监听地址（防火墙按它放行）
	Saved  *config.ServerConfig // Keep=false 时要写入的配置文件内容
}

// planServerInstall 决定本次安装是否沿用已有配置。
//
// 三个条件缺一不可：安装器要求保留、配置文件确实存在、配置能正常读出。
//   - 任一不满足就按本次参数重建配置——半截或损坏的 JSON 留着只会让服务起不来；
//   - 保留时监听地址取旧值，否则防火墙会放行默认的 123，而服务实际监听的是
//     用户在 Web 里改过的端口。
func planServerInstall(listen string, stratum uint8, webAddr, upstream string,
	upstreamInterval time.Duration, preserve, exists bool,
	old *config.ServerConfig, loadErr error) serverInstallPlan {

	built := &config.ServerConfig{
		Listen:           listen,
		Stratum:          int(stratum),
		Web:              webAddr,
		Upstream:         upstream,
		UpstreamInterval: duration.Format(upstreamInterval),
	}
	if !preserve || !exists || loadErr != nil || old == nil {
		return serverInstallPlan{Keep: false, Listen: listen, Saved: built}
	}
	effListen := old.Listen
	if effListen == "" {
		effListen = listen
	}
	return serverInstallPlan{Keep: true, Listen: effListen, Saved: old}
}

// fallbackText 返回值本身；为空时返回 def。仅用于日志可读性。
func fallbackText(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// installService 用 PAL 把自身注册为系统服务（含启动参数透传），并放行防火墙。
//
// preserve 为真且配置文件已存在时进入「保留模式」：不重写配置、不塞启动参数，
// 服务完全按现有配置文件运行，监听端口也取自该文件——否则防火墙会放行默认的
// 123，而服务实际监听的是用户改过的端口。安装器在覆盖升级时传这个开关，
// 避免用户辛苦在 Web 里配好的参数被一次重装抹掉。
func installService(listen string, stratum uint8, manual bool, webAddr string, noFirewall bool, upstream string, upstreamInterval time.Duration, preserve bool) error {
	p := pal.NewPlatform()
	if !p.IsElevated() {
		return fmt.Errorf("%w：请以管理员(Windows)/root(Linux)运行 `Sntpserver install`", pal.ErrNotElevated)
	}
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("获取自身路径失败: %w", err)
	}
	startType := "auto"
	if manual {
		startType = "manual"
	}
	// 从 v1.0.x 升级：旧服务 ntpsyncd 若还挂着，会同新服务抢 UDP 123。
	// 清不掉也不该中断安装——记一条日志，装新的照样装。
	if removed, err := pal.CleanupLegacyService(p, pal.LegacyServerServiceName); err != nil {
		log.Printf("清理旧版服务 %s 失败（不影响本次安装）：%v", pal.LegacyServerServiceName, err)
	} else if removed {
		log.Printf("已清理旧版服务 %s（v1.0.x 遗留）", pal.LegacyServerServiceName)
	}
	cfgPath := config.DefaultPath()

	// 保留模式判定：只有「要求保留 + 文件确实存在 + 能读出来」三者同时成立才保留。
	// 读失败时宁可覆盖本次参数，也不能留一个半截配置害服务起不来。
	exists := config.Exists(cfgPath)
	var oldCfg *config.ServerConfig
	var loadErr error
	if preserve && exists {
		oldCfg, loadErr = config.Load(cfgPath)
	}
	plan := planServerInstall(listen, stratum, webAddr, upstream, upstreamInterval,
		preserve, exists, oldCfg, loadErr)
	listen = plan.Listen
	if plan.Keep {
		fmt.Printf("检测到已有配置 %s，保留其中参数（监听=%s 上游=%s）\n",
			cfgPath, plan.Listen, fallbackText(plan.Saved.Upstream, "本机硬件时钟"))
	} else if loadErr != nil {
		fmt.Printf("读取已有配置 %s 失败（%v），改为按本次参数写入\n", cfgPath, loadErr)
	}

	// 配置统一写入配置文件，服务启动参数只保留 "run"。
	//
	// 这样做是为了消除「两个配置来源打架」：若安装参数塞进服务启动命令，
	// 用户在 Web 参数页改完、重启后会被安装时的旧参数覆盖，界面就形同虚设。
	// 写文件失败时回退为把参数塞进启动命令（保证服务仍能按预期参数运行）。
	args := []string{"run"}
	if !plan.Keep {
		if err := config.Save(cfgPath, plan.Saved); err != nil {
			fmt.Printf("警告：写入配置 %s 失败（%v），改为写入服务启动参数\n", cfgPath, err)
			args = append(args, "-l", listen, "-s", fmt.Sprintf("%d", stratum))
			if webAddr != "" {
				args = append(args, "-web", webAddr)
			}
			if upstream != "" {
				args = append(args, "-upstream", upstream, "-upstream-interval", upstreamInterval.String())
			}
		} else {
			fmt.Printf("配置已写入 %s\n", cfgPath)
		}
	} else {
		fmt.Printf("服务启动参数保持为 run，全部配置来自 %s\n", cfgPath)
	}
	cfg := pal.ServiceConfig{
		Name:        pal.DefaultServiceName,
		DisplayName: pal.DefaultServiceName,
		Description: "把本机硬件时钟作为权威时间源，向局域网设备提供 NTP 授时",
		BinPath:     exe,
		Args:        args,
		StartType:   startType,
	}
	if err := p.InstallService(cfg); err != nil {
		return err
	}
	fmt.Printf("服务已安装（%s，开机%s）\n", cfg.DisplayName, startTypeText(startType))

	// 放行防火墙（UDP 监听端口）。
	if !noFirewall {
		port := udpPortFromAddr(listen)
		if err := p.ConfigureFirewall(port); err != nil {
			fmt.Printf("警告：防火墙放行失败（服务仍可用，但局域网设备可能无法访问）: %v\n", err)
		} else {
			fmt.Printf("防火墙已放行 UDP %d 入站\n", port)
		}
	}

	if !manual {
		if err := p.StartService(pal.DefaultServiceName); err != nil {
			return fmt.Errorf("服务已安装但启动失败: %w", err)
		}
		fmt.Println("服务已启动")
	}
	return nil
}

// parseDurArg 解析时长参数：支持中文单位（分/秒/小时/天）与标准格式，纯数字按「分」。
func parseDurArg(name, val string, def time.Duration) (time.Duration, error) {
	if val == "" {
		return def, nil
	}
	d, err := duration.Parse(val)
	if err != nil {
		return 0, fmt.Errorf("参数 -%s 的值 %q 无法解析: %v", name, val, err)
	}
	return d, nil
}

// newUpstreamClient 构造「服务端向上游校时」专用的客户端。
//
// 与普通客户端的唯一区别是不设跳变阈值：本机就是下游的权威时间源，面对上游
// 没有更高的权威可依，偏差再大也必须修正。若沿用默认 5 秒护栏，首次部署、
// CMOS 电池失效或虚拟机长时间休眠导致的十几秒偏差会被永久挡住——护栏拦下的
// 不是异常，而是它本该修掉的那个错误。
func newUpstreamClient(upstream string) (*client.Client, error) {
	c, err := client.New(upstream)
	if err != nil {
		return nil, err
	}
	c.MaxStep = client.MaxStepUnlimited
	return c, nil
}

// syncUpstreamOnce 向上游 NTP 执行一次校时，并把结果写回服务端状态（供 Web 展示）。
func syncUpstreamOnce(s *server.Server, upstream string) {
	status := &server.UpstreamStatus{Addr: upstream, LastAt: time.Now()}

	c, err := newUpstreamClient(upstream)
	if err != nil {
		status.Result = "地址无效: " + err.Error()
		s.SetUpstreamStatus(status)
		return
	}
	res, err := c.Sync()
	if err != nil {
		status.Result = "同步失败: " + err.Error()
		s.SetUpstreamStatus(status)
		return
	}
	status.Offset = res.Offset.String()
	status.Delay = res.Delay.String()
	status.Synced, status.Result = upstreamResultText(res)
	s.SetUpstreamStatus(status)
}

// upstreamResultText 把一次上游校时结果映射为「通信是否成功」与展示文案。
//
// 语义刻意分开：Synced 只表示与上游通信成功，并不等于时钟已改。早先两者混写，
// 会出现「已同步（偏差 17s 超过阈值 5s，已拒绝自动设钟）」这种自相矛盾的提示。
func upstreamResultText(res *client.Result) (synced bool, text string) {
	switch {
	case res.Applied:
		return true, fmt.Sprintf("已设钟（调整 %s）", res.Offset)
	case res.Skipped:
		return true, "未设钟：" + res.Reason
	default:
		return true, fmt.Sprintf("已同步（偏差 %s）", res.Offset)
	}
}

// udpPortFromAddr 从 "0.0.0.0:123" 形式的地址解析出端口号。
func udpPortFromAddr(addr string) int {
	if i := strings.LastIndexByte(addr, ':'); i >= 0 && i < len(addr)-1 {
		if p, err := strconv.Atoi(addr[i+1:]); err == nil {
			return p
		}
	}
	return 123
}

func startTypeText(s string) string {
	switch s {
	case "auto":
		return "自启"
	case "manual":
		return "手动"
	default:
		return s
	}
}
