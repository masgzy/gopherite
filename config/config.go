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
		}
		if err != nil {
			return nil, fmt.Errorf("line %d: key %s: %w", i+1, k, err)
		}
	}
	if cfg.ServerPort < 1 || cfg.ServerPort > 65535 {
		return nil, fmt.Errorf("server-port %d out of range", cfg.ServerPort)
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
