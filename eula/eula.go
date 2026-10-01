// Package eula implements the vanilla-compatible EULA gate: the server
// refuses to start until the operator has accepted the Minecraft EULA,
// either via eula.txt or the --accept-eula flag.
package eula

import (
	_ "embed"
	"fmt"
	"os"
	"strings"
)

// Path is the conventional EULA file name, matching vanilla so existing
// tooling (panels, docker images, scripts) keeps working unchanged.
const Path = "eula.txt"

// embedded template mirrors vanilla's generated file (comments localized
// to Chinese; the eula key line stays vanilla-compatible).
const template = `#将下面的设置改为 TRUE 即表示你同意 Minecraft EULA（https://aka.ms/MinecraftEULA）。
#也可以在启动命令行传入 --accept-eula 来接受 EULA。
#本文件由 Gopherite 生成。
eula=false
`

// State describes the current acceptance status.
type State int

const (
	// Missing means no eula.txt exists yet; Generate must run first.
	Missing State = iota
	// Rejected means eula.txt exists with eula=false.
	Rejected
	// Accepted means the operator agreed to the EULA.
	Accepted
)

// Check inspects the EULA state without modifying anything.
func Check(dir string) (State, error) {
	raw, err := os.ReadFile(join(dir, Path))
	if os.IsNotExist(err) {
		return Missing, nil
	}
	if err != nil {
		return Rejected, err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "eula=") {
			if strings.TrimSpace(strings.TrimPrefix(line, "eula=")) == "true" {
				return Accepted, nil
			}
			return Rejected, nil
		}
	}
	return Rejected, nil
}

// Generate writes the default eula.txt (eula=false) into dir.
func Generate(dir string) error {
	return os.WriteFile(join(dir, Path), []byte(template), 0o644)
}

// Accept flips eula.txt to accepted, creating it if needed.
func Accept(dir string) error {
	if err := Generate(dir); err != nil {
		return err
	}
	return os.WriteFile(join(dir, Path), []byte(strings.Replace(template, "eula=false", "eula=true", 1)), 0o644)
}

// Ensure runs the vanilla gate: returns Accepted, or generates the file
// and returns a descriptive error telling the operator what to do.
func Ensure(dir string) (State, error) {
	st, err := Check(dir)
	if err != nil {
		return st, err
	}
	switch st {
	case Accepted:
		return Accepted, nil
	case Missing:
		if gerr := Generate(dir); gerr != nil {
			return Missing, gerr
		}
		return Missing, fmt.Errorf("eula: 已生成 %s；阅读 https://aka.ms/MinecraftEULA 后将其中的 eula 改为 true（或用 --accept-eula 启动）", Path)
	default:
		return Rejected, fmt.Errorf("eula: %s 中 eula=false；必须同意 https://aka.ms/MinecraftEULA 才能运行服务器", Path)
	}
}

func join(dir, name string) string {
	if dir == "" {
		return name
	}
	return dir + "/" + name
}
