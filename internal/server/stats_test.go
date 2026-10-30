package server

import (
	"net"
	"path/filepath"
	"testing"
	"time"
)

// TestSaveLoadClientsRoundTrip 验证「落盘 → 重启 → 恢复」这条链：
// 次数、首次/最近授时时刻都要原样回来，否则统计持久化就没意义了。
func TestSaveLoadClientsRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")

	now := time.Now().Truncate(time.Second)
	s := New(Config{Listen: "127.0.0.1:0"})
	s.noteClient(&net.UDPAddr{IP: net.ParseIP("192.168.1.10")}, now)
	s.noteClient(&net.UDPAddr{IP: net.ParseIP("192.168.1.10")}, now.Add(2*time.Second))
	s.noteClient(&net.UDPAddr{IP: net.ParseIP("192.168.1.20")}, now.Add(time.Second))

	if err := s.SaveClients(path); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	fresh := New(Config{Listen: "127.0.0.1:0"})
	if err := fresh.LoadClients(path); err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	got := fresh.Clients()
	if len(got) != 2 {
		t.Fatalf("恢复出 %d 条，期望 2 条：%+v", len(got), got)
	}
	byAddr := map[string]ClientInfo{}
	for _, c := range got {
		byAddr[c.Addr] = c
	}
	if c := byAddr["192.168.1.10"]; c.Count != 2 {
		t.Errorf("192.168.1.10 次数 = %d，期望 2（同一 IP 两次授时应累加）", c.Count)
	}
	if c := byAddr["192.168.1.10"]; !c.LastSee.Equal(now.Add(2 * time.Second)) {
		t.Errorf("最近授时时刻 = %v，期望 %v", c.LastSee, now.Add(2*time.Second))
	}
	if c := byAddr["192.168.1.20"]; c.Count != 1 {
		t.Errorf("192.168.1.20 次数 = %d，期望 1", c.Count)
	}
}

// TestLoadClientsMissingFileIsNotError 首次安装时统计文件不存在，绝不能因此起不来。
func TestLoadClientsMissingFileIsNotError(t *testing.T) {
	s := New(Config{Listen: "127.0.0.1:0"})
	if err := s.LoadClients(filepath.Join(t.TempDir(), "nope.json")); err != nil {
		t.Fatalf("文件不存在应当安静通过，实际报错: %v", err)
	}
	if err := s.LoadClients(""); err != nil {
		t.Fatalf("空路径应当直接跳过，实际报错: %v", err)
	}
	if err := s.SaveClients(""); err != nil {
		t.Fatalf("空路径保存应当直接跳过，实际报错: %v", err)
	}
}

// TestLoadClientsRespectsMax 恢复时同样受 maxClients 约束：
// 不能因为磁盘里存得多就把内存撑爆。
func TestLoadClientsRespectsMax(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "clients.json")
	now := time.Now()

	s := New(Config{Listen: "127.0.0.1:0"})
	for i := 0; i < maxClients+20; i++ {
		ip := net.IPv4(10, 0, byte(i/256), byte(i%256)).String()
		s.noteClient(&net.UDPAddr{IP: net.ParseIP(ip)}, now.Add(time.Duration(i)*time.Second))
	}
	if err := s.SaveClients(path); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	// 注意：noteClient 本身就会淘汰，先确认落盘条数没超过上限
	saved := New(Config{Listen: "127.0.0.1:0"})
	if err := saved.LoadClients(path); err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if n := len(saved.Clients()); n > maxClients {
		t.Fatalf("恢复出 %d 条，超过上限 %d", n, maxClients)
	}
}

// TestSaveClientsKeepsOnlyRecent 落盘顺序按最近授时倒序，
// 便于运维直接打开 JSON 就能看出「谁还在校时」。
func TestSaveClientsKeepsOnlyRecent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "clients.json")
	now := time.Now()
	s := New(Config{Listen: "127.0.0.1:0"})
	s.noteClient(&net.UDPAddr{IP: net.ParseIP("10.0.0.1")}, now)
	s.noteClient(&net.UDPAddr{IP: net.ParseIP("10.0.0.2")}, now.Add(time.Minute))
	if err := s.SaveClients(path); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	fresh := New(Config{Listen: "127.0.0.1:0"})
	if err := fresh.LoadClients(path); err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	list := fresh.Clients()
	if len(list) != 2 {
		t.Fatalf("期望 2 条，实际 %d", len(list))
	}
	if list[0].Addr != "10.0.0.2" {
		t.Fatalf("第一条应为最近授时的 10.0.0.2，实际 %s", list[0].Addr)
	}
}
