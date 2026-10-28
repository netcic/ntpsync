package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLoad_NotExist_ReturnsEmpty(t *testing.T) {
	dir := t.TempDir()
	c, err := Load(filepath.Join(dir, "nope.json"))
	if err != nil {
		t.Fatalf("文件不存在不应报错，实际: %v", err)
	}
	if c == nil {
		t.Fatal("应返回空配置而非 nil")
	}
	if c.Listen != "" || c.Upstream != "" {
		t.Errorf("空配置应全为零值，实际: %+v", c)
	}
}

func TestSaveThenLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "ntpsyncd.json")
	want := &ServerConfig{
		Listen:           "0.0.0.0:123",
		Stratum:          3,
		Web:              "127.0.0.1:8080",
		Upstream:         "ntp.aliyun.com",
		UpstreamInterval: "1小时",
	}
	if err := Save(path, want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Listen != want.Listen || got.Stratum != want.Stratum ||
		got.Web != want.Web || got.Upstream != want.Upstream ||
		got.UpstreamInterval != want.UpstreamInterval {
		t.Errorf("往返不一致:\n want=%+v\n  got=%+v", want, got)
	}
}

// TestSave_EmptyUpstreamPersists 校验「清空上游」这个语义能被持久化。
// 若 Upstream 字段加了 omitempty，清空后就写不进文件，重启会复活旧值。
func TestSave_EmptyUpstreamPersists(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := Save(path, &ServerConfig{Upstream: "ntp.aliyun.com"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	// 再保存一次，把上游清空。
	if err := Save(path, &ServerConfig{Upstream: ""}); err != nil {
		t.Fatalf("清空保存失败: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Upstream != "" {
		t.Errorf("上游应被清空，实际仍为 %q", got.Upstream)
	}
}

func TestLoad_CorruptFile_ReportsError(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("损坏的配置应报错，便于上层记录日志")
	}
	if !strings.Contains(err.Error(), "格式非法") {
		t.Errorf("错误信息应说明格式问题，实际: %v", err)
	}
}

// TestSave_NoTempFileLeftBehind 校验原子写不会残留 .tmp 临时文件。
func TestSave_NoTempFileLeftBehind(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := Save(path, &ServerConfig{Listen: "0.0.0.0:123"}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("不应残留临时文件 %s.tmp", path)
	}
}

func TestDefaultPath(t *testing.T) {
	got := DefaultPath()
	if !strings.HasSuffix(got, "ntpsyncd.json") {
		t.Errorf("路径应以 ntpsyncd.json 结尾，实际 %q", got)
	}
	if runtime.GOOS == "windows" {
		if !strings.Contains(strings.ToLower(got), "ntpsync") {
			t.Errorf("Windows 路径应位于 ntpsync 目录下，实际 %q", got)
		}
	}
	t.Logf("默认配置路径: %s", got)
}

// TestSaveClientThenLoad_RoundTrip 客户端配置的往返读写。
func TestSaveClientThenLoad_RoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sub", "ntpsync.json")
	want := &ClientConfig{Server: "192.168.1.10:123", Interval: "15分钟", Step: "5秒"}
	if err := SaveClient(path, want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	got, err := LoadClient(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Server != want.Server || got.Interval != want.Interval || got.Step != want.Step {
		t.Errorf("往返不一致:\n want=%+v\n  got=%+v", want, got)
	}
}

// TestLoadClient_NotExist_ReturnsEmpty 没装过客户端时不该报错。
func TestLoadClient_NotExist_ReturnsEmpty(t *testing.T) {
	c, err := LoadClient(filepath.Join(t.TempDir(), "nope.json"))
	if err != nil {
		t.Fatalf("文件不存在不应报错，实际: %v", err)
	}
	if c == nil {
		t.Fatal("应返回空配置而非 nil")
	}
	if c.Server != "" || c.Interval != "" || c.Step != "" {
		t.Errorf("空配置应全为零值，实际: %+v", c)
	}
}

// TestDefaultClientPath 客户端与服务端必须用不同文件，否则同机双端互相覆盖。
func TestDefaultClientPath(t *testing.T) {
	got := DefaultClientPath()
	if !strings.HasSuffix(got, "ntpsync.json") {
		t.Errorf("路径应以 ntpsync.json 结尾，实际 %q", got)
	}
	if got == DefaultPath() {
		t.Errorf("客户端配置不应与服务端共用同一个文件: %q", got)
	}
	t.Logf("客户端默认配置路径: %s", got)
}

// TestExists 安装器靠它区分「没配过」与「配过但值为空」：
// 前者写默认值，后者必须保留，否则升级会抹掉用户在 Web 里配的参数。
func TestExists(t *testing.T) {
	dir := t.TempDir()
	if Exists(dir) {
		t.Error("目录不应被当作配置文件")
	}
	p := filepath.Join(dir, "x.json")
	if Exists(p) {
		t.Error("文件不存在时应返回 false")
	}
	if err := Save(p, &ServerConfig{Listen: "0.0.0.0:123"}); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if !Exists(p) {
		t.Error("文件存在时应返回 true")
	}
}
