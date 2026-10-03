<div align="center">

# ⛏️ Gopherite

**A Minecraft: Java Edition 26.2 vanilla server implemented in Go**

[![License: MIT](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Go](https://img.shields.io/badge/Go-1.27+-00ADD8.svg?logo=go&logoColor=white)](https://go.dev)
[![Minecraft](https://img.shields.io/badge/Minecraft-26.2-62BF8B.svg)](https://minecraft.wiki)
[![Protocol](https://img.shields.io/badge/Protocol-776-orange.svg)](https://minecraft.wiki/w/Protocol_version)
[![CI](https://img.shields.io/badge/CI-GitHub_Actions-2088FF.svg?logo=githubactions&logoColor=white)](.github/workflows/ci.yml)
[![中文文档](https://img.shields.io/badge/文档-中文-red.svg)](README.md)

*Library + thin binary · Fully embedded resources · Single-binary release · Performance-first*

</div>

---

Gopherite is a ground-up implementation of a Minecraft Java Edition vanilla server in **Go**, targeting **26.2 (protocol 776)**. It ships no JVM code and requires no Java runtime — the build artifact is a **static 3–4 MB single binary** you can drop anywhere and run.

> Why this project exists: the Go ecosystem currently has **no usable server implementation for modern Java Edition (1.21.4+ / 26.x)**. The closest attempt, [Zeppelin](https://github.com/ZeppelinMC/Zeppelin) (targeting 1.21.3), has been dormant for roughly two years. Meanwhile Mojang has stopped obfuscating the game jar, so decompiling the official server yields near-source-quality reference code — dramatically improving the feasibility of behaviour-accurate reimplementation.

## ✨ Features

### Implemented (M0 / M1 / M2)

- **Single binary, fully embedded resources** — default config and EULA template are compiled in via `go:embed`; startup performs zero extraction, and the server only writes files that must be persisted (`eula.txt`, `server.properties`, and `world/` in a later milestone)
- **Vanilla-compatible EULA gate** — first launch generates `eula.txt` and refuses to start until the Minecraft EULA is accepted; `--accept-eula` short-circuits it. The file format matches vanilla, so existing panels and scripts keep working
- **Protocol 776 network layer** — VarInt / VarLong / packet framing / buffers with zero-copy reads and zero-allocation hot paths
- **Complete server list ping** — handshake (Intent=Status) → status response (version / protocol / MOTD / player cap / icon) → ping/pong; Gopherite shows up in the vanilla client's server list with live latency
- **Graceful login rejection** — login intents receive a proper Login Disconnect chat component (replaced by the real login flow in M2)
- **Full login flow (online + offline modes)** — with `online-mode=true` the server performs the RSA-1024 key exchange, AES/CFB8 encryption and Mojang session-server validation; with `false` the offline UUID is derived by the vanilla rules. Both paths are covered by protocol-level end-to-end tests
- **Zlib compression layer** — vanilla-exact layering (compression inside encryption, plaintext outer length), threshold configurable
- **Superflat world + spawn & movement** — configuration-phase registry sync (29 dynamic registries) → play phase → superflat chunk streaming (heightmaps / sections / skylight) → teleport confirm, movement handling and keep-alive
- **Multi-version seam** — packet constants live in per-version sub-packages (`protocol/java/v776`), ready for version negotiation later

### Planned

See the [Roadmap](#-roadmap).

## 📦 Quick Start

### Build from source

```bash
git clone https://github.com/masgzy/gopherite.git
cd gopherite
make build          # produces dist/gopherite (single binary)
```

or with the Go toolchain directly:

```bash
go build -trimpath -ldflags "-s -w" -o gopherite ./cmd/gopherite
```

### Run

```bash
./gopherite                      # first launch: generates eula.txt + server.properties, then exits
./gopherite --accept-eula        # accept the EULA and start (or set eula=true manually)
```

Add `127.0.0.1` in a vanilla 26.2 client and Gopherite appears in the server list.

> **EULA note**: running a Minecraft server means you agree to the [Minecraft EULA](https://aka.ms/MinecraftEULA). Gopherite enforces this step exactly like vanilla. This project is not affiliated with or endorsed by Mojang Studios / Microsoft.

### Command-line flags

| Flag | Description |
|---|---|
| `--config <path>` | config file path (default `server.properties`) |
| `--dir <path>` | working directory for `eula.txt` and world data |
| `--port <port>` | override `server-port` from the config |
| `--accept-eula` | accept the EULA and start |
| `--version` | print version info |

## ⚙️ Configuration

Vanilla-style `server.properties` (`key=value`, `#` comments). Unknown keys are ignored, so vanilla configs never break Gopherite.

| Key | Default | Description |
|---|---|---|
| `server-port` | `25565` | listen port |
| `server-ip` | (empty) | bind address; empty = all interfaces |
| `motd` | `A Gopherite Server` | server list message |
| `max-players` | `20` | advertised player cap |
| `online-mode` | `true` | Mojang account validation (effective since M2) |
| `network-compression-threshold` | `256` | packet compression threshold (effective since M2) |
| `server-icon` | (empty) | 64×64 PNG icon path |
| `read-timeout` | `30` | handshake/status read timeout (seconds) |
| `write-timeout` | `10` | per-packet write timeout (seconds); guards broadcasts against stalled clients |

## 🏗️ Architecture

```
gopherite/
├── cmd/gopherite/       # thin entry point: flags, EULA gate, wiring, signals
├── config/              # server.properties parsing + embedded defaults
├── eula/                # vanilla-compatible EULA state machine
├── protocol/            # version-agnostic primitives (VarInt, buffers, framing)
│   └── java/            # Java Edition shared layer (handshake, status, intents)
│       └── v776/        # 26.2-specific constants (multi-version seam)
└── server/              # server core (connection state machine, ping, options)
```

**Design principles**

1. **Library + thin binary** — `server`, `protocol` and friends are importable libraries; `cmd/gopherite` is only wiring. Future Paper/Purpur patches can override or extend library components while the release artifact stays a single binary.
2. **Everything embedded** — default assets are compiled in (`go:embed`); the server writes only what must be persisted: world saves (`world/`, Anvil-compatible from M4 so vanilla clients can read them), logs, and operator-edited config.
3. **Performance first** — zero allocations on hot paths (VarInt, framing, buffers); `Reader` exposes transient zero-copy views over packet payloads, and only `String` copies (because packet buffers are pooled and reused). Later milestones add buffer pooling, batched chunk encoding and sharded world ticking.
4. **Correctness over coverage** — every milestone cross-checks protocol behaviour against the unobfuscated official 26.2 jar and community protocol docs. Slow and right beats fast and wrong.

## 🚀 Roadmap

- [x] **M0** project skeleton, community files, CI
- [x] **M1** network layer + server list ping
- [x] **M2** login flow (offline + online modes, encryption, compression) + superflat chunks + walking in a world
- [x] **M3** block breaking/placing, chunk management, simplified lighting
- [x] **M4** full NBT + Anvil world persistence
- [x] **M5** entities (physics, basic AI)
- [x] **M6** registry pipeline: extract blocks/items/recipes from the vanilla jar and generate Go code
- [x] **M7** inventory / crafting / commands (Brigadier port) / basic redstone
- [x] **M8** survival basics (health / hunger / eating / fall & void damage / death & respawn / `/kill`) + passive mobs (pig / cow / sheep / chicken: wander AI, panic on hit, knockback, death drops)
- [x] **M8.5** multiplayer visibility (players see each other / tab-list sync) + XP orbs + mob fall damage + herd top-up
- [x] **M9** hostile mobs (zombie / skeleton / creeper: chase AI, melee / archery / fuse explosion, daylight burning, night spawning) + day/night cycle (26.2 WorldClock) + entity sounds
- [x] **M10** container blocks (27-slot chest + furnace: fuel / smelting / burn & cook progress / burn-out cooldown, XP settlement, break & explosion content spills, vanilla-compatible Anvil persistence); fixes the M7 wire bug where container clicks were never dispatched plus the window-id collision
- [x] **M11** combat & defense: armor reduction (official 26.2 ArmorMaterials values and the `bypasses_armor` tag, vanilla reduction formula, diamond/netherite toughness), player bow (draw & charge, vanilla power curve, hits mobs and other players, stuck arrows collectible, survival ammo consumption), melee upgrades (per-weapon attack-speed cooldown, falling critical hits at 1.5x with the crit animation, PvP melee with knockback, the full 26.2 weapon damage table including the copper tier and the rebalanced axes), arm-swing animation broadcast
- [x] **M12** world dynamics & smart GC: weather cycle (clear/rain/thunder state machine + 26.2 Game Event sync + join weather snapshot), thunderstorms with lightning strikes (lightning bolt entity, strike damage & sound, surface fires), TNT (flint-and-steel ignition, primed entity with fuse, vanilla-scale explosion with chain ignition, sharing the common blast path with creepers), beds (two-block placement, sleeping through the night, bed spawn points, break coupling), a `/gc` observability command, and a smart GC governor (lock-free runtime/metrics sampling of GC CPU fraction / allocation rate / pause P99 / heap watermark; threshold-band policy with stepping, cooldown and anti-hunting to adapt GOGC; GOMEMLIMIT auto-probed at 90% of cgroup/system memory to guard against OOM)
- [x] **M13** Status effects & potions: all 40 effects of 26.2 (incl. the new `breath_of_the_nautilus`), effect engine (vanilla upgrade/refresh rules, regeneration/poison/wither/hunger vanilla tick intervals, instant health/damage with undead inversion), combat & survival wiring (strength/weakness, resistance, absorption hearts, fire immunity, haste/mining fatigue, jump boost/slow falling, speed/slowness mob movement, invisibility/glowing flag sync), `/effect give|clear`, consumables (golden/enchanted apple, rotten flesh, spider eye, pufferfish, milk bucket), full `potion_contents` component pipeline (inventory/drops/pickup/Anvil NBT), drinking (glass bottle back), splash/lingering throwing (vanilla distance falloff), brewing stand (400t / 20-op blaze powder fuel, complete vanilla mix table incl. start-mix semantics, `has_bottle_*` blockstate, progress data slots, persistence)
- [x] **M14** Potions without shortcuts: a real **area effect cloud entity** (26.2 decompiled semantics ported in full: radius 3.0 / duration 600t / wait time 10t / radius-on-use −0.5 / per-tick shrink −3/600, 20t victim immunity window, 5t scan interval, instantaneous effects at 0.5 potency with undead inversion, dissolves once the radius is spent) with three-field metadata sync (radius / waiting / `entity_effect` particle color) and the potion color table (all 40 effects averaged weighted by amp+1, = `PotionContents.getColor`) — **lingering potions now spawn a cloud that re-applies full-duration effects instead of a one-shot 0.25 discount**; **tipped arrows** (keep `potion_contents` in flight, crit flag at full draw, apply full-duration effects on hit, fade back to plain arrows after 600t in ground, restore as tipped arrows on pickup, never cross-stack with plain arrows); fixes thrown potions rendering at the origin (proper spawn packet + item-stack metadata); the whole inventory/drop/death-drop path now carries the `potion_contents` component (the hotbar sync previously dropped it)
- [ ] **P**  Paper / Purpur behaviour parity

## 🧑‍💻 Development

```bash
make test      # go test ./...
make vet       # go vet ./...
make fmt       # gofmt across the repo
make build     # dist/gopherite
```

### Registry data pipeline (M6)

`server/blocks_gen.go`, `items_gen.go` and `recipes_gen.go` are generated from vanilla 26.2 data by committed scripts. To bump versions, re-run the pipeline:

```bash
# 1) extract recipes / item tags and generate the data reports (JDK 21+)
python3 scripts/extract_vanilla_data.py --jar server.jar --out vanilla

# 2) regenerate the three Go tables (blocks / items / recipes) + gofmt
make gen VANILLA=vanilla
```

- Generators emit deterministic output (blocks sorted by name, recipes by file name), so committed diffs stay noise-free;
- The item → placed-block mapping is derived by name equality plus a small hand-audited exception table (`ADD_BLOCK`/`DEL_BLOCK` in `gen_items.py`); spot-check it after a version bump;
- `recipes_gen.go` resolves item tags recursively at generation time; ingredient expressions are `minecraft:item`, `#minecraft:tag` or '|'-joined alternatives.

Requires Go 1.27+ (install easily with [g](https://github.com/voidint/g)). Contributions are welcome — start with [CONTRIBUTING.md](CONTRIBUTING.md).

## 🤝 Contributing

All contributions are welcome: protocol verification, benchmarks, bug reproductions, documentation. Please ensure `make test`, `make vet` and `gofmt` pass before opening a PR.

## 🛡️ Security

Please report vulnerabilities privately through the channel in [SECURITY.md](SECURITY.md) instead of opening a public issue.

## 📄 License

Released under the [MIT License](LICENSE).

*Minecraft is a trademark of Mojang Studios. Gopherite is not affiliated with or endorsed by Mojang Studios / Microsoft.*

## 🙏 Acknowledgments

- [ZeppelinMC/Zeppelin](https://github.com/ZeppelinMC/Zeppelin) (Apache-2.0) — the pioneer of vanilla Go servers and an architecture reference
- [Tnze/go-mc](https://github.com/Tnze/go-mc) (MIT) — the Go MC protocol library
- [df-mc/dragonfly](https://github.com/df-mc/dragonfly) (MIT) — Go Bedrock server, an engineering benchmark
- [minekube/gate](https://github.com/minekube/gate) (Apache-2.0) — an active Java protocol implementation reference
- [minecraft.wiki](https://minecraft.wiki/w/Protocol) — protocol documentation
- Everyone in the Minecraft community who paved the way for open implementations
- [x] **M17** Advancements (26.2 decompile-verified): embedded vanilla advancement definitions (126 tree JSONs from the official datapack, go:embed), exact `TreeNodePosition` layout port, `AdvancementVisibilityEvaluator` visibility rules (26.2 roots are real advancements — fresh players see an empty screen until "Minecraft" completes), the three advancement packets (0x82/0x55/0x32) with the 26.2 field order, a trigger engine (tick/inventory_changed/kills/arrows/consume/place/effects/levitation/bed/fall/brewing with loot-condition subset evaluation; unsupported criteria stay locked like a vanilla server missing the feature), vanilla-compatible `advancements/<uuid>.json` persistence, XP rewards on the vanilla level curve, framed chat announcements with hover tooltips, and the full `/advancement grant|revoke` command set; also fixes the M10 fuel-table tag-prefix bug, an entity-tick concurrency window and an M16 test race
- [x] **M17.5** 网络协议全面体检（只修 bug 不加功能）：修复 `Declare Commands` 根节点被误标为 argument 类型（M7b 起存活 10 个里程碑的真实客户端崩溃根源——客户端 `getRoot()` 强转 `RootCommandNode` 抛 `ClassCastException`，即"网络协议错误，进不了服务器"）并在回归测试中断言"全树恰有一个类型 0 节点且其索引=根索引"；修复正版模式加密请求发错包号（客户端方向 0x00 是 Login Disconnect，应为 `PacketLoginHello` 0x01）；修复物品栈 wire 全面 off-by-one（26.2 Slot 与 26.1+ `ItemStackTemplate` 均用注册表**原始 id**，旧的 "holder id+1" 使背包/掉落物/进度图标全部错位一格——现行 wiki Slot data 与 Pumpkin 双源交叉验证）与 `DataComponentPatch` 计数顺序（新增/移除两个计数相邻在前、条目在后，旧交织写法会被客户端把组件类型读成移除计数）；status 阶段回完 pong 的正常关闭不再误报"会话异常"；连接关停先置写管线 dead 再关套接字、`net.ErrClosed` 静默收尾，不再误报"写入失败: use of closed network connection"；修复进度 flush 测试与 `net.Pipe` 拷贝协程的约 7% 假阴性竞态；其余协议层（VarInt 边界/帧长度/压缩炸弹上限/CFB8/AuthDigest/离线 UUID v3/登录配置状态机/join 序列/写超时）逐文件复核无恙
