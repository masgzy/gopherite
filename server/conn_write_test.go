package server

import (
	"bufio"
	"errors"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// 回归（M12 后测试发现的隐患）：慢客户端停止读取时，TCP 发送缓冲被写满
// 会让 WriteFramed 的底层 write 无限阻塞。broadcastSoundLocked 等
// *Locked 广播是在持有 Server.mu 的前提下逐个 sendPacket 的——单个卡死
// 连接就能冻结整个服务端 tick。sendPacket 必须带写截止时间，超时后毒化
// 连接并立即返回。

func newWriteTestConn(t *testing.T, nc net.Conn, writeTimeout int) (*Server, *conn) {
	t.Helper()
	s := &Server{opts: Options{WriteTimeoutSeconds: writeTimeout}, players: make(map[*conn]*player)}
	c := &conn{
		s:  s,
		nc: nc,
		br: bufio.NewReader(nc),
		bw: bufio.NewWriterSize(nc, 4096),
		st: statePlay,
		wr: protocol.NewWriter(),
	}
	return s, c
}

// 写超时必须准时打断阻塞中的写入，并把连接标记为 dead、关闭底层套接字。
func TestSendPacketWriteTimeout(t *testing.T) {
	srvSide, cliSide := net.Pipe() // net.Pipe 无内核缓冲：对端不读，写入必然阻塞
	defer cliSide.Close()

	_, c := newWriteTestConn(t, srvSide, 1)

	done := make(chan error, 1)
	go func() { done <- c.sendPacket([]byte{0x01, 0x02, 0x03}) }()

	select {
	case err := <-done:
		if !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("期望写超时错误，得到 %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("sendPacket 未在写超时内返回——慢客户端阻塞隐患复现")
	}

	if !c.dead.Load() {
		t.Fatal("写超时后连接应被标记为 dead")
	}
	// 毒化后底层连接必须已关闭：对端应立刻读到 EOF，而不是继续等待。
	cliSide.SetReadDeadline(time.Now().Add(2 * time.Second))
	if _, err := cliSide.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("毒化后对端应读到 EOF，得到 %v", err)
	}
}

// 毒化后的连接必须立即拒绝后续写入，不得让排在 writeMu 后面的调用方
// 再等一轮完整超时（keep-alive、实体 tick、广播可能同时打向同一条连接）。
func TestSendPacketAfterDeadShortCircuits(t *testing.T) {
	srvSide, cliSide := net.Pipe()
	defer cliSide.Close()

	_, c := newWriteTestConn(t, srvSide, 1)
	c.dead.Store(true)

	start := time.Now()
	if err := c.sendPacket([]byte{0x01}); !errors.Is(err, errConnDead) {
		t.Fatalf("期望 errConnDead，得到 %v", err)
	}
	if d := time.Since(start); d > 100*time.Millisecond {
		t.Fatalf("dead 短路应立即返回，实际耗时 %v", d)
	}
}

// 持锁广播路径（broadcastSoundLocked）在存在卡死客户端时必须在写超时内
// 返回，不得无限持有 Server.mu。
func TestBroadcastUnblocksOnStalledClient(t *testing.T) {
	srvSide, cliSide := net.Pipe()
	defer cliSide.Close()

	s, stalled := newWriteTestConn(t, srvSide, 1)
	s.players[stalled] = &player{conn: stalled, x: 0, y: 64, z: 0}

	done := make(chan struct{})
	go func() {
		s.broadcastSoundLocked("minecraft:block.note_block.pling",
			v776.SoundSourceBlocks, 0, 64, 0, 1, 1)
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("持锁广播被慢客户端无限阻塞——隐患复现")
	}
	if !stalled.dead.Load() {
		t.Fatal("广播超时后慢客户端连接应被毒化")
	}
}
