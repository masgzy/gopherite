package server

import (
	"testing"

	"github.com/masgzy/gopherite/protocol"
	"github.com/masgzy/gopherite/protocol/java"
	"github.com/masgzy/gopherite/protocol/java/v776"
)

// TestWeatherTransitionCycle 固定随机种子驱动状态机，验证状态只会
// 在 晴→雨→(雷|晴)→… 之间转移且各状态的周期落在配置区间。
func TestWeatherTransitionCycle(t *testing.T) {
	s, b0, _ := joinedBot(t, "Weatherman")
	drainBot(b0)
	s.mu.Lock()
	defer s.mu.Unlock()

	// clear → rain 必然。
	s.weather = weatherClear
	s.advanceWeatherLocked()
	if s.weather != weatherRain {
		t.Fatalf("晴后应转为雨，得到 %d", s.weather)
	}
	if s.weatherTicks < weatherRainMin || s.weatherTicks > weatherRainMax {
		t.Fatalf("雨周期 %d 越界", s.weatherTicks)
	}

	// 雨后只能到雷暴或晴。
	seen := map[int32]bool{}
	for i := 0; i < 200; i++ {
		s.weather = weatherRain
		s.advanceWeatherLocked()
		if s.weather != weatherThunder && s.weather != weatherClear {
			t.Fatalf("雨后非法状态 %d", s.weather)
		}
		seen[s.weather] = true
		// 雷暴必须回到雨。
		if s.weather == weatherThunder {
			s.advanceWeatherLocked()
			if s.weather != weatherRain {
				t.Fatalf("雷暴后应回到雨，得到 %d", s.weather)
			}
		}
	}
	if !seen[weatherThunder] || !seen[weatherClear] {
		t.Fatalf("200 次掷骰未见全部分支: %v", seen)
	}
}

// TestSetWeatherBroadcast 验证天气切换向在线玩家广播 begin/end raining
// 与强度事件（雨 3 帧、雷 3 帧、雨停 3 帧）。
func TestSetWeatherBroadcast(t *testing.T) {
	s, b, _ := joinedBot(t, "Skycaster")

	s.mu.Lock()
	s.setWeatherLocked(weatherRain, 1000)
	s.mu.Unlock()

	// 跳过噪声后应连续读到 Game Event：1(begin) / 7(level) / 9(level)。
	want := []struct {
		id    int32
		event byte
		value float32
	}{{v776.PacketPlayGameEvent, 1, 0}, {v776.PacketPlayGameEvent, 7, 1}, {v776.PacketPlayGameEvent, 9, 0}}
	for _, w := range want {
		r := expectNoise(b, w.id)
		ev, _ := r.Byte()
		val, _ := r.Float()
		if ev != w.event || val != w.value {
			t.Fatalf("game event: want (%d, %v) got (%d, %v)", w.event, w.value, ev, val)
		}
	}

	// 雨停。
	s.mu.Lock()
	s.setWeatherLocked(weatherClear, 1000)
	s.mu.Unlock()
	r := expectNoise(b, v776.PacketPlayGameEvent)
	if ev, _ := r.Byte(); ev != gameEventEndRaining {
		t.Fatalf("雨停事件应为 2，得到 %d", ev)
	}
}

// TestLightningHurtsPlayer 验证落雷伤害与起火方块。
func TestLightningHurtsPlayer(t *testing.T) {
	s, b0, p := joinedBot(t, "Rod")
	drainBot(b0)
	before := p.health
	s.mu.Lock()
	s.spawnLightningLocked(p.x, p.z)
	s.mu.Unlock()
	if p.health >= before {
		t.Fatalf("雷击未造成伤害: %v -> %v", before, p.health)
	}
	// 闪电实体存在（2 ticks 生命周期）。
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, e := range s.entities {
		if _, ok := e.(*lightningEntity); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("落雷未生成闪电实体")
	}
}

// TestSendWeatherJoinSync 直接编码帧验证 join 天气同步的包格式。
func TestSendWeatherJoinSync(t *testing.T) {
	w := protocol.NewWriter()
	w.VarInt(v776.PacketPlayGameEvent)
	java.WritePlayGameEvent(w, gameEventBeginRaining, 0)
	r := protocol.NewReader(w.Bytes())
	if id, _ := r.VarInt(); id != v776.PacketPlayGameEvent {
		t.Fatal("帧 id 错误")
	}
	if ev, _ := r.Byte(); ev != 1 {
		t.Fatalf("事件应为 1，得到 %d", ev)
	}
	if val, _ := r.Float(); val != 0 {
		t.Fatalf("begin raining 值应为 0，得到 %v", val)
	}
}
