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
- [x] **M9** hostile mobs (zombie / skeleton / creeper: chase AI, melee / archery / fuse explosion, daylight burning, night spawning) + day/night cycle (26.2 WorldClock) + entity sounds (current)
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
