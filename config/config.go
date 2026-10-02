// Package config loads and persists the server.properties-style
// configuration. The default document is embedded in the binary: a fresh
// install runs with zero external files until persistence demands one.
package config

import (
	_ "embed"
	"fmt"
	"os"
	"strconv"
	"strings"
)

//go:embed default.properties
var defaultConfig string

// Config holds every operator-tunable setting. Numeric/bool fields carry
// zero values until Load fills them, so always construct via Load.
type Config struct {
	ServerPort           int
	ServerIP             string
	MOTD                 string
	MaxPlayers           int
	OnlineMode           bool
	CompressionThreshold int // -1 disables; enforced from M2
	IconPath             string
	ReadTimeout          int
	LevelName            string // world directory, vanilla level-name
	ViewDistance         int    // server-side chunk radius cap

	// 智能 GC 调控（Gopherite 扩展键）。
	GCTuning         bool  // 闭环动态 GOGC 调控开关
	GCTargetPauseMS  int   // P99 暂停目标（毫秒）
	GCMemLimitMiB    int64 // 内存天花板 MiB；0 = 自动探测（cgroup/系统内存 × 90%）
	GCMinGOGC        int   // 调控下限档
	GCMaxGOGC        int   // 调控上限档
	GCBaseGOGC       int   // 启动基线
	GCSampleInterval int   // 采样周期（秒）
}

// Path is the conventional configuration file name.
const Path = "server.properties"

// Load reads path. When the file is missing it is created from the
// embedded defaults (first-run behaviour mirrors vanilla: generate, then
// proceed) and the default values are returned.
func Load(path string) (*Config, bool, error) {
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		cfg, perr := parse(defaultConfig)
		if perr != nil {
			return nil, false, fmt.Errorf("config: embedded defaults broken: %w", perr)
		}
		if werr := os.WriteFile(path, []byte(defaultConfig), 0o644); werr != nil {
			// Read-only working directory: defaults still apply.
			return cfg, false, nil
		}
		return cfg, true, nil
	}
	if err != nil {
		return nil, false, fmt.Errorf("config: read %s: %w", path, err)
	}
	cfg, err := parse(string(raw))
	if err != nil {
		return nil, false, fmt.Errorf("config: parse %s: %w", path, err)
	}
	return cfg, false, nil
}

// parse understands the vanilla properties subset: key=value lines,
// '#' comments and blank lines. Unknown keys are ignored so configs can
// keep vanilla-only entries without breaking Gopherite.
func parse(doc string) (*Config, error) {
	cfg := &Config{
		ServerPort:           25565,
		MOTD:                 "A Gopherite Server",
		MaxPlayers:           20,
		OnlineMode:           true,
		CompressionThreshold: 256,
		ReadTimeout:          30,
		LevelName:            "world",
		ViewDistance:         8,
		GCTuning:             true,
		GCTargetPauseMS:      2,
		GCMemLimitMiB:        0,
		GCMinGOGC:            20,
		GCMaxGOGC:            300,
		GCBaseGOGC:           100,
		GCSampleInterval:     1,
	}
	for i, line := range strings.Split(doc, "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return nil, fmt.Errorf("line %d: expected key=value", i+1)
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		var err error
		switch k {
		case "server-port":
			cfg.ServerPort, err = strconv.Atoi(v)
		case "server-ip":
			cfg.ServerIP = v
		case "motd":
			cfg.MOTD = v
		case "max-players":
			cfg.MaxPlayers, err = strconv.Atoi(v)
		case "online-mode":
			cfg.OnlineMode = v == "true"
		case "network-compression-threshold":
			cfg.CompressionThreshold, err = strconv.Atoi(v)
		case "server-icon":
			cfg.IconPath = v
		case "read-timeout":
			cfg.ReadTimeout, err = strconv.Atoi(v)
		case "level-name":
			cfg.LevelName = v
		case "view-distance":
			cfg.ViewDistance, err = strconv.Atoi(v)
		case "gc-tuning":
			cfg.GCTuning = v == "true"
		case "gc-target-pause-ms":
			cfg.GCTargetPauseMS, err = strconv.Atoi(v)
		case "gc-mem-limit-mib":
			cfg.GCMemLimitMiB, err = strconv.ParseInt(v, 10, 64)
		case "gc-min-gogc":
			cfg.GCMinGOGC, err = strconv.Atoi(v)
		case "gc-max-gogc":
			cfg.GCMaxGOGC, err = strconv.Atoi(v)
		case "gc-base-gogc":
			cfg.GCBaseGOGC, err = strconv.Atoi(v)
		case "gc-sample-interval":
			cfg.GCSampleInterval, err = strconv.Atoi(v)
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: key %s: %w", i+1, k, err)
		}
	}
	if cfg.ServerPort < 1 || cfg.ServerPort > 65535 {
		return nil, fmt.Errorf("server-port %d out of range", cfg.ServerPort)
	}
	if cfg.LevelName == "" || cfg.LevelName == "." || cfg.LevelName == ".." || strings.ContainsRune(cfg.LevelName, '/') || strings.ContainsRune(cfg.LevelName, '\\') {
		return nil, fmt.Errorf("level-name %q invalid", cfg.LevelName)
	}
	if cfg.ViewDistance < 2 {
		cfg.ViewDistance = 2
	}
	if cfg.ViewDistance > 32 {
		cfg.ViewDistance = 32
	}
	if cfg.GCTargetPauseMS < 1 {
		cfg.GCTargetPauseMS = 1
	}
	if cfg.GCMemLimitMiB < 0 {
		cfg.GCMemLimitMiB = 0
	}
	if cfg.GCSampleInterval < 1 {
		cfg.GCSampleInterval = 1
	}
	if cfg.GCMinGOGC < 1 {
		cfg.GCMinGOGC = 20
	}
	if cfg.GCMaxGOGC < cfg.GCMinGOGC {
		cfg.GCMaxGOGC = 300
	}
	if cfg.GCBaseGOGC < 1 {
		cfg.GCBaseGOGC = 100
	}
	return cfg, nil
}

// ListenAddr renders the bind address.
func (c *Config) ListenAddr() string {
	if c.ServerIP == "" {
		return fmt.Sprintf(":%d", c.ServerPort)
	}
	return fmt.Sprintf("%s:%d", c.ServerIP, c.ServerPort)
}
