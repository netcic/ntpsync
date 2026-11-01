package pal_test

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"ntpsync/internal/config"
	"ntpsync/internal/pal"
	"ntpsync/internal/web"
)

// 本文件的职责：把「程序运行期到底写了什么」与「卸载脚本要删什么」钉在一起。
//
// 卸载残留这类问题不会在编译期暴露，只能靠断言：程序新增一个数据文件、
// 脚本忘了删，测试必须红。所以这里不测「脚本长什么样」，而是把脚本里的
// 声明取出来，和 internal/config、internal/pal 的常量逐项比对。
//
// 这些都是静态断言：不装服务、不写注册表，任何机器上都能跑。实机卸载后的
// 核查由 scripts/verify-uninstall.ps1（Windows）/ verify-uninstall.sh（Linux）完成。

// repoFile 读取仓库内的打包脚本。
func repoFile(t *testing.T, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "..", filepath.FromSlash(rel)))
	if err != nil {
		t.Fatalf("读取 %s 失败: %v", rel, err)
	}
	return string(b)
}

// defineValues 取出脚本里所有 `!define <name> "value"` 的 value。
func defineValues(src, name string) []string {
	re := regexp.MustCompile(`(?m)^\s*!define\s+` + regexp.QuoteMeta(name) + `\s+"([^"]*)"`)
	var out []string
	for _, m := range re.FindAllStringSubmatch(src, -1) {
		out = append(out, m[1])
	}
	return out
}

// codeLines 去掉整行注释与行尾注释，只留真正会执行的代码。
// 注释里出现「对方产品的文件名」是正常的（正是用来解释为什么不能删），
// 不能当成「会删对方数据」的证据；只有这样断言才落在行为上。
func codeLines(src string) string {
	var out []string
	for _, line := range strings.Split(src, "\n") {
		if i := strings.Index(line, "#"); i >= 0 {
			line = line[:i]
		}
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// sameSet 比较两个字符串集合（忽略顺序与重复）。
func sameSet(t *testing.T, what string, got, want []string) {
	t.Helper()
	seen := map[string]bool{}
	for _, g := range got {
		seen[g] = true
	}
	for _, w := range want {
		if !seen[w] {
			t.Errorf("%s 缺少 %q（实际 %v）", what, w, got)
		}
		delete(seen, w)
	}
	for rest := range seen {
		t.Errorf("%s 多出 %q（实际 %v）", what, rest, got)
	}
}

// TestInstallerUninstallPathsMatchRuntime 断言 Windows 卸载器删的正是程序写的那些文件。
func TestInstallerUninstallPathsMatchRuntime(t *testing.T) {
	nsi := repoFile(t, "packaging/win/ntp-installer.nsi")

	// 配置文件名：两端各一份，必须与 config 的默认路径一致。
	// 写错一端的名字，卸载就会删掉同机另一个产品的配置。
	sameSet(t, "CFG_FILE",
		defineValues(nsi, "CFG_FILE"),
		[]string{filepath.Base(config.DefaultPath()), filepath.Base(config.DefaultClientPath())})

	// 数据目录名：%ProgramData%\ntpsync（Linux 是 /etc/ntpsync），两端共用。
	sameSet(t, "DATA_DIR_NAME",
		defineValues(nsi, "DATA_DIR_NAME"),
		[]string{filepath.Base(filepath.Dir(config.DefaultPath()))})

	// 授时统计文件（只有服务端有）。
	sameSet(t, "STATS_FILE",
		defineValues(nsi, "STATS_FILE"),
		[]string{config.ServerStatsFileName})

	// 原子写入会留下 .tmp，卸载必须一起删。
	for _, tok := range []string{"${CFG_FILE}.tmp", "${STATS_FILE}.tmp"} {
		if !strings.Contains(nsi, tok) {
			t.Errorf("卸载段应删除 %s（程序写盘中断会留下它）", tok)
		}
	}

	// 目录只能「空了才删」：两端共用一个数据目录，用 RMDir /r 会把对方数据一起带走。
	if !strings.Contains(nsi, `RMDir "$R9\${DATA_DIR_NAME}"`) {
		t.Error("卸载段应以非递归 RMDir 删除数据目录（空了才删）")
	}
	if strings.Contains(nsi, `RMDir /r "$R9`) {
		t.Error("卸载段不能递归删除共用数据目录，会误删同机另一个产品的配置")
	}
}

// TestInstallerUninstallServicesAndRegistry 断言服务名、旧版服务名与注册表项都在清理清单里。
func TestInstallerUninstallServicesAndRegistry(t *testing.T) {
	nsi := repoFile(t, "packaging/win/ntp-installer.nsi")

	sameSet(t, "SERVICE_NAME", defineValues(nsi, "SERVICE_NAME"),
		[]string{pal.DefaultServiceName, pal.ClientServiceName})
	sameSet(t, "LEGACY_SERVICE", defineValues(nsi, "LEGACY_SERVICE"),
		[]string{pal.LegacyServerServiceName, pal.LegacyClientServiceName})
	sameSet(t, "LEGACY_PRODUCT_EN", defineValues(nsi, "LEGACY_PRODUCT_EN"),
		[]string{"NTPTimeServer", "NTPTimeClient"})
	// 1.0.x 靠 Run 项拉起进程，卸载要把这两个值清掉（各角色各清自己那条）
	sameSet(t, "LEGACY_RUN_VALUE", defineValues(nsi, "LEGACY_RUN_VALUE"),
		[]string{"NTP授时服务", "NTP校时客户端"})

	// 本产品自己的两个键（安装目录 / 控制面板卸载入口）都要删。
	for _, key := range []string{
		`DeleteRegKey HKLM "Software\${PRODUCT_NAME_EN}"`,
		`DeleteRegKey HKLM "Software\Microsoft\Windows\CurrentVersion\Uninstall\${PRODUCT_NAME_EN}"`,
	} {
		if !strings.Contains(nsi, key) {
			t.Errorf("卸载段缺少 %s", key)
		}
	}
	// 32/64 两个注册表视图都要清：历史版本用过 SetRegView 64，
	// 只清一个视图会在另一处留下「控制面板里看得到、点不动」的条目。
	if !strings.Contains(nsi, "SetRegView 64") || !strings.Contains(nsi, "SetRegView 32") {
		t.Error("卸载段应同时清理 32 位与 64 位注册表视图")
	}
	// 1.0.x 的产品键只在旧程序目录已不存在时才删（否则旧版卸载入口会失效）
	if !strings.Contains(nsi, `DeleteRegKey HKLM "Software\${LEGACY_PRODUCT_EN}"`) ||
		!strings.Contains(nsi, `${AndIfNot} ${FileExists} `) {
		t.Error("卸载段应在「旧程序目录已不存在」的前提下才清理 1.0.x 产品键")
	}
}

// TestInstallerUninstallFirewallNamesMatchPal 断言卸载器按 pal 建过的规则名删除。
// 名字对不上 = 规则留着，现场表现是「程序都卸了，端口还开着」。
func TestInstallerUninstallFirewallNamesMatchPal(t *testing.T) {
	nsi := repoFile(t, "packaging/win/ntp-installer.nsi")

	// 默认端口要与 web 包的常量一致：卸载器按默认端口兜底清规则，
	// 常量漂了就会去删一个从来没建过的名字。
	sameSet(t, "DEFAULT_WEB", defineValues(nsi, "DEFAULT_WEB"),
		[]string{fmt.Sprint(web.DefaultServerWebPort), fmt.Sprint(web.DefaultClientWebPort)})

	for _, want := range []string{
		fmt.Sprintf(pal.FirewallRuleUDP, 123),   // 授时放行（默认端口兜底）
		fmt.Sprintf(pal.FirewallRuleUDPv1, 123), // 1.0.x 的授时规则名
		strings.Replace(pal.FirewallRuleWeb, "%d", "${DEFAULT_WEB}", 1),
		strings.Replace(pal.FirewallRuleWebLegacy, "%d", "${DEFAULT_WEB}", 1),
		fmt.Sprintf(pal.FirewallRuleWeb, 123), // 1.11.2 让 Web 端口跟随授时端口
		fmt.Sprintf(pal.FirewallRuleWebLegacy, 123),
	} {
		if !strings.Contains(nsi, want) {
			t.Errorf("卸载段缺少防火墙规则 %q", want)
		}
	}
}

// TestLinuxUninstallScriptsCleanOwnConfig 断言两个 shell 卸载脚本只删自己的配置。
func TestLinuxUninstallScriptsCleanOwnConfig(t *testing.T) {
	server := codeLines(repoFile(t, "scripts/uninstall-server.sh"))
	client := codeLines(repoFile(t, "scripts/uninstall-client.sh"))

	serverCfg := filepath.Base(config.DefaultPath())
	clientCfg := filepath.Base(config.DefaultClientPath())

	if !strings.Contains(server, "/etc/ntpsync") || !strings.Contains(server, serverCfg) {
		t.Errorf("服务端卸载脚本应删除 /etc/ntpsync/%s", serverCfg)
	}
	if !strings.Contains(server, config.ServerStatsFileName) {
		t.Errorf("服务端卸载脚本应删除授时统计 %s", config.ServerStatsFileName)
	}
	if strings.Contains(server, clientCfg) {
		t.Errorf("服务端卸载脚本不应出现客户端配置名 %s（会误删另一个产品的数据）", clientCfg)
	}

	if !strings.Contains(client, "/etc/ntpsync") || !strings.Contains(client, clientCfg) {
		t.Errorf("客户端卸载脚本应删除 /etc/ntpsync/%s", clientCfg)
	}
	if strings.Contains(client, serverCfg) || strings.Contains(client, config.ServerStatsFileName) {
		t.Error("客户端卸载脚本不应出现服务端的配置/统计文件名")
	}

	// 共用目录只能 rmdir（空了才删），不能 rm -rf
	for name, src := range map[string]string{"uninstall-server.sh": server, "uninstall-client.sh": client} {
		if strings.Contains(src, "rm -rf /etc/ntpsync") {
			t.Errorf("%s 不能递归删除 /etc/ntpsync（两端共用）", name)
		}
		if !strings.Contains(src, "rmdir \"$CFG_DIR\"") && !strings.Contains(src, "rmdir /etc/ntpsync") {
			t.Errorf("%s 应在目录空了才删除 /etc/ntpsync", name)
		}
		if !strings.Contains(src, `.tmp`) {
			t.Errorf("%s 应连 .tmp 临时文件一起删", name)
		}
	}
}

// TestDebRpmCleanupCoversConfig 断言 deb postrm 与 rpm %postun 都清了配置。
// 不打这两个包时它们最容易被忘掉——装完 apt remove 一看 /etc/ntpsync 还在。
func TestDebRpmCleanupCoversConfig(t *testing.T) {
	pkg := repoFile(t, "scripts/package.sh")

	for _, want := range []string{
		"DEBIAN/postrm",
		"%postun",
		"/etc/ntpsync/" + filepath.Base(config.DefaultPath()),
		"/etc/ntpsync/" + filepath.Base(config.DefaultClientPath()),
		config.ServerStatsFileName,
		"rmdir /etc/ntpsync",
	} {
		if !strings.Contains(pkg, want) {
			t.Errorf("scripts/package.sh 缺少 %q", want)
		}
	}
	if strings.Contains(pkg, "rm -rf /etc/ntpsync") {
		t.Error("打包脚本不能递归删除 /etc/ntpsync（服务端与客户端共用）")
	}
}
