package server

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"
)

// 授时统计的持久化。
//
// 为什么需要：按 IP 聚合的「授时次数」原先只在内存里，服务一重启就清零。
// 现场要拿它做两件事——判断某台设备到底有没有在按时校时（次数是否在涨），
// 以及出问题后回溯最后一次授时的时刻。这两件事都跨得过一次服务重启。
//
// 设计取舍：不做「每次授时都写盘」。UDP 123 上随时可能有成百上千次请求，
// 每次都写一次文件等于把 IO 压力绑到授时路径上，本末倒置。改为由调用方
// 定时落盘 + 退出前落盘，最坏情况是丢掉最后一个周期内的计数。

// clientStatsFile 是统计文件的落盘格式。
// 单独定义结构体而不是直接存 map：map 的序列化顺序不稳定，diff/排错时难看，
// 也避免以后加字段时破坏兼容。
type clientStatsFile struct {
	SavedAt time.Time    `json:"saved_at"`
	Clients []ClientInfo `json:"clients"`
}

// SaveClients 把当前客户端统计写入 path。
// 采用「临时文件 + 重命名」的原子写法：写到一半崩了不会留下半个坏文件，
// 下次启动读到的仍是上一份完整的。
func (s *Server) SaveClients(path string) error {
	if path == "" {
		return nil
	}
	list := s.Clients()
	sort.Slice(list, func(i, j int) bool { return list[i].LastSee.After(list[j].LastSee) })
	payload := clientStatsFile{SavedAt: time.Now(), Clients: list}

	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("server: 创建统计目录失败: %w", err)
	}
	b, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return fmt.Errorf("server: 序列化统计失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("server: 写入统计失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("server: 替换统计文件失败: %w", err)
	}
	return nil
}

// LoadClients 从 path 恢复统计。文件不存在（首次安装）不算错误。
// 读出来的是一个坏文件时也只记错误不丢弃服务——统计数据丢得起，授时不能停。
func (s *Server) LoadClients(path string) error {
	if path == "" {
		return nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("server: 读取统计文件失败: %w", err)
	}
	var payload clientStatsFile
	if err := json.Unmarshal(b, &payload); err != nil {
		return fmt.Errorf("server: 解析统计文件失败: %w", err)
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clients == nil {
		s.clients = make(map[string]*ClientInfo)
	}
	// 旧数据按「最近授时」倒序灌入：超出上限被淘汰的应该是最久没来的，
	// 而不是恢复顺序里排在后面的。
	sort.Slice(payload.Clients, func(i, j int) bool {
		return payload.Clients[i].LastSee.After(payload.Clients[j].LastSee)
	})
	for _, c := range payload.Clients {
		if c.Addr == "" {
			continue
		}
		if _, exists := s.clients[c.Addr]; exists {
			continue
		}
		if len(s.order) >= maxClients {
			break
		}
		cp := c
		s.clients[c.Addr] = &cp
		s.order = append(s.order, c.Addr)
	}
	return nil
}
