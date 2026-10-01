// Command gopherite is the single-binary entry point of the Gopherite
// Minecraft server. All resources are embedded: first launch only writes
// files the operator must edit (eula.txt, server.properties).
package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/masgzy/gopherite/config"
	"github.com/masgzy/gopherite/eula"
	"github.com/masgzy/gopherite/internal/ui"
	"github.com/masgzy/gopherite/protocol/java/v776"
	"github.com/masgzy/gopherite/server"
)

// build info, overridable via -ldflags at release time.
var (
	version = "dev"
	commit  = "unknown"
)

const banner = `
   ____ __  __  ____  __ _   __ __ _  ___  ____
  / ___|  \/  |/ ___|/ _| | / _| | |/ _ \|  _ \
 | |  _| |\/| | |  _| |_| || |_| | | |_| | |_) |
 | |_| | |  | | |_| |  _|  \  _| | |  _| |  _ <
  \____|_|  |_|\____|_| |_(_)_|_|_|\____/|_| \_\
`

func main() {
	cfgPath := flag.String("config", config.Path, "配置文件路径")
	dir := flag.String("dir", ".", "eula.txt 与世界数据的运行目录")
	acceptEULA := flag.Bool("accept-eula", false, "接受 Minecraft EULA 并启动")
	port := flag.Int("port", 0, "覆盖配置中的 server-port")
	showVersion := flag.Bool("version", false, "打印版本信息并退出")
	flag.Parse()

	log.SetFlags(log.LstdFlags)
	fmt.Print(ui.Title(banner))
	if *showVersion {
		fmt.Printf("Gopherite %s（commit %s，协议 %d）\n", version, commit, v776.ProtocolNumber)
		return
	}

	// EULA 门禁，与原版行为一致。
	if *acceptEULA {
		if err := eula.Accept(*dir); err != nil {
			log.Fatalf("eula: %v", err)
		}
	}
	st, err := eula.Ensure(*dir)
	if err != nil {
		log.Fatal(err)
	}
	_ = st

	cfg, generated, err := config.Load(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	if generated {
		log.Printf("已生成默认配置 %s，编辑后重启即可生效", ui.Path(*cfgPath))
	}
	if *port != 0 {
		cfg.ServerPort = *port
	}

	srv, err := server.New(server.Options{
		ListenAddr:         cfg.ListenAddr(),
		MOTD:               cfg.MOTD,
		MaxPlayers:         cfg.MaxPlayers,
		OnlineMode:         cfg.OnlineMode,
		VersionName:        v776.Name,
		ProtocolNumber:     v776.ProtocolNumber,
		FaviconPath:        cfg.IconPath,
		MaxPacketLen:       1 << 21,
		ReadTimeoutSeconds: cfg.ReadTimeout,
		LevelName:          cfg.LevelName,
		ViewDistance:       cfg.ViewDistance,
	})
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		sig := make(chan os.Signal, 1)
		signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
		<-sig
		log.Println(ui.Warn("! 正在关停") + ui.Dim("（正在保存世界）..."))
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		_ = srv.Shutdown(ctx)
		os.Exit(0)
	}()

	log.Printf("Gopherite %s 正在启动，监听 %s（协议 %d，正版验证 %s）",
		version, ui.Path(cfg.ListenAddr()), v776.ProtocolNumber,
		ui.Number(onOff(cfg.OnlineMode)))

	start := time.Now()
	if err := srv.Listen(); err != nil {
		log.Fatal(err)
	}
	// 原版同款启动完成提示：Done (<耗时>)!
	log.Printf("%s! 输入 %s 查看帮助，按 Ctrl+C 停止服务器",
		ui.Success("Done ("+fmt.Sprintf("%.3fs", time.Since(start).Seconds())+")"),
		ui.Keyword("help"))
	if err := srv.Serve(); err != nil {
		log.Fatal(err)
	}
}

// onOff renders a bool as 开启/关闭.
func onOff(v bool) string {
	if v {
		return "开启"
	}
	return "关闭"
}
