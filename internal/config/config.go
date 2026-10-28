// Package config 负责把运行期配置持久化到磁盘。
//
// 为什么需要它：Web 参数页允许运维修改监听地址、上游时间源等设置。若这些
// 改动只存在内存里，重启服务即丢失，界面上「保存成功」就成了一句空话。
// 本包提供 JSON 文件读写，供启动时加载、保存时落盘。
//
// 设计取向：配置文件是「可选的第二配置源」。文件不存在时一切照旧（完全按
// 命令行参数运行），存在时作为基础值，命令行显式给出的参数优先覆盖。
// 这样既能支持 Web 改配置，也不会破坏既有的静默部署脚本。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// ServerConfig 为服务端需要持久化的设置。
// 字段与 Web 参数页一一对应，改这里时记得同步 web.Config。
type ServerConfig struct {
	Listen           string `json:"listen,omitempty"`
	Stratum          int    `json:"stratum,omitempty"`
	Web              string `json:"web,omitempty"`
	Upstream         string `json:"upstream"` // 不加 omitempty：允许显式保存「清空上游」
	UpstreamInterval string `json:"upstream_interval,omitempty"`
}

// ClientConfig 为客户端需要持久化的设置（Web 参数页保存）。
// 字段与客户端参数页一一对应，改这里时记得同步 web.Config。
type ClientConfig struct {
	Server   string `json:"server,omitempty"`   // 校时目标，如 192.168.1.10:123
	Interval string `json:"interval,omitempty"` // 校时间隔，如 "15分钟"
	Step     string `json:"step,omitempty"`     // 校时阈值，如 "5秒"
}

// DefaultPath 返回默认配置文件路径。
//
// Windows 用 %ProgramData%\ntpsync\：该目录对 LocalSystem 可写，且不像
// Program Files 那样受 UAC 保护（Web 保存由服务进程执行，权限足够）。
// 非 Windows 用 /etc/ntpsync/。
func DefaultPath() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "ntpsync", "ntpsyncd.json")
		}
	}
	return filepath.Join("/etc", "ntpsync", "ntpsyncd.json")
}

// DefaultClientPath 返回客户端配置文件路径。
// 与服务端分文件保存：同一台机器上可能两端都装，混用会互相覆盖。
func DefaultClientPath() string {
	if runtime.GOOS == "windows" {
		if pd := os.Getenv("ProgramData"); pd != "" {
			return filepath.Join(pd, "ntpsync", "ntpsync.json")
		}
	}
	return filepath.Join("/etc", "ntpsync", "ntpsync.json")
}

// Load 读取配置文件。文件不存在时返回空配置且不报错（视为「未配置」）。
func Load(path string) (*ServerConfig, error) {
	var c ServerConfig
	if err := loadJSON(path, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// LoadClient 读取客户端配置文件。文件不存在时返回空配置且不报错。
func LoadClient(path string) (*ClientConfig, error) {
	var c ClientConfig
	if err := loadJSON(path, &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// Exists 报告配置文件是否已存在。
//
// Load/LoadClient 对「文件不存在」返回空配置而非错误，安装器却需要区分
// 「没配过」与「配过但值为空」——升级时必须保留用户已经配好的参数，
// 只有真正首次安装才写入默认值。
func Exists(path string) bool {
	st, err := os.Stat(path)
	return err == nil && !st.IsDir()
}

// loadJSON 把 path 上的 JSON 解到 v；文件不存在视为「未配置」而非错误。
func loadJSON(path string, v interface{}) error {
	b, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("config: 读取 %s 失败: %w", path, err)
	}
	if err := json.Unmarshal(b, v); err != nil {
		return fmt.Errorf("config: %s 格式非法: %w", path, err)
	}
	return nil
}

// Save 原子地写入配置文件：先写临时文件再改名，
// 避免写入过程中断电/崩溃留下半截 JSON 导致下次启动读不出来。
func Save(path string, c *ServerConfig) error {
	if c == nil {
		return errors.New("config: 配置不能为空")
	}
	return saveJSON(path, c)
}

// SaveClient 原子地写入客户端配置（与服务端同一个写入实现）。
func SaveClient(path string, c *ClientConfig) error {
	if c == nil {
		return errors.New("config: 配置不能为空")
	}
	return saveJSON(path, c)
}

// saveJSON 原子写：临时文件 + rename，失败时清理临时文件。
func saveJSON(path string, v interface{}) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("config: 序列化失败: %w", err)
	}
	b = append(b, '\n')

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("config: 创建目录 %s 失败: %w", dir, err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return fmt.Errorf("config: 写入 %s 失败: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("config: 替换 %s 失败: %w", path, err)
	}
	return nil
}
