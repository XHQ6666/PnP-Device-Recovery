# PnP Device Recovery

[中文](#pnp-device-recovery) | [English](#english)

轻量级 **Windows PnP 设备自动恢复** 守护进程。

在真实 Windows 环境中，部分设备节点会偶发进入异常状态（设备管理器中的问题代码，例如 Code 10 / 31 / 43）。本工具通过 Windows 原生 PnP 通知持续监听配置中的目标设备；一旦确认设备不健康，即在受控条件下自动执行一次软复位式恢复：

**Disable Device → 固定 3s → Enable Device → 复查状态**（失败尝试之间另用 `advanced.retry_interval`）

程序直接调用 SetupAPI / CfgMgr32 等 Windows API，**不**依赖 PowerShell、`pnputil`、WMIC 等外部命令解析。

> **安全说明**  
> Disable / Enable 会暂时中断设备工作，可能造成短暂断连或输出中断。请以管理员权限运行，并仅在了解风险的环境中使用。本工具不能修复硬件损坏、驱动包损坏、固件或 BIOS 等根本性问题。

---

## 它解决什么问题

| 场景 | 说明 |
|------|------|
| 设备偶发异常 | 设备仍在系统中枚举，但带有 Problem Code，功能不可用 |
| 需要自动软复位 | 手工在设备管理器中「禁用 → 启用」往往有效，希望无人值守自动完成 |
| 多设备并行监控 | 可同时监控多块不同设备，各自独立重试与状态 |
| 开机计划任务 | 可作为开机启动项运行；Ready（系统 uptime ≥ 1s，可用 advanced 调整）后再首次扫描，并可按设备配置 rec_delay |

---

## 工作原理

```
Windows PnP
     │
     ▼
PnP Event（事件驱动，非固定轮询）
     │
     ▼
尾沿合并（Event Coalescing）
     │
     ▼
枚举并匹配配置中的 FriendlyName
     │
     ▼
读取 Status / ProblemCode
     │
 ┌───┴────┐
正常     异常
 │         │
结束     Recovery Session
           │
       Disable
           │
        Enable
           │
      Re-check
```

**监控方式**：优先使用 `CM_Register_Notification`；不可用时回退到 `RegisterDeviceNotification` + 隐藏窗口的 `WM_DEVICECHANGE`。PnP 回调线程内 **绝不** 执行 Disable / Enable，仅入队由工作协程处理。

**健康判定**（CfgMgr32）：

- 无 `DN_HAS_PROBLEM`
- `ProblemCode == 0`

否则视为异常（包含但不限于 Code 10 / 31 / 43）。

---

## 启动时序

守护进程不会在进程一启动就对设备执行 Disable / Enable。过早操作（例如仍在 Boot 画面）可能不稳定。Ready 判定为 **系统已开机计时（uptime）≥ 1 秒**（内部常量；不进 config）。

```
进程启动
  │
  ├─ 解析命令行
  ├─ 获取单实例互斥量
  ├─ 如需则 UAC 提权
  │
  ▼
STARTING
  │  注册 PnP 通知
  │  （此阶段事件只记 pending，不 Recovery）
  │
  ▼
WAIT_FOR_SYSTEM_READY
  │  GetTickCount64：系统 uptime ≥ 1s 即视为启动完成
  │  （内部轮询；总超时约 1 分钟；超时后带警告继续，避免永久挂起）
  │  不再使用 LogonUI / 输入桌面名 / explorer
  │
  ▼
INITIAL_SCAN（reason=startup）
  │  此时才允许首次自动 Recovery（仍受 devices[].rec_delay / ProblemCode 约束）
  │
  ▼
NORMAL_RUNNING
     事件驱动 + 尾沿合并
```

真正推迟自动 Recover 请用 `devices[].rec_delay` / `ProblemCode`。Ready 的 uptime/轮询/超时等可通过可选 `advanced` 调整；省略则用默认值。

---

## 主要功能

- **事件驱动 PnP 监听**（非固定轮询）
- **FriendlyName 精确匹配** 与 `regex:`（Go RE2）匹配
- **自动 Disable → Enable → 复查**
- **多设备**：独立会话、独立重试计数；不同设备可并行，同一设备串行
- **尾沿事件合并**：短时间内连续 PnP 事件合并为一次扫描，减轻枚举风暴
- **Exhausted 状态**：达到 `max_retries` 后该设备停止自动恢复；仅 `check` 可清除并重试
- **Ready 后再首次扫描**：系统 uptime ≥ 1s
- **每设备 rec_delay / ProblemCode**：自动 Recover 可延后；可按问题代码过滤
- **Disable/Enable 状态门闩**：仅在设备当前为启用时 Disable，仅在已禁用时 Enable
- **单实例 + 本地命名管道 IPC**
- **UAC 自动提权**
- **UTF-8 日志**（`log` 对象）：可关闭；`normal`/`debug`；上限 10 MiB

---

## 配置

将 `config.json` 与可执行文件放在同一目录。`log` 对象为 **必填**。相对日志路径相对于 **可执行文件目录**（不是进程 cwd）。**不再支持**旧字段 `log_file`。

```json
{
  "log": {
    "enabled": true,
    "level": "normal",
    "path": ""
  },
  "devices": [
    {
      "friendly_name": "Example Device Name",
      "max_retries": 10,
      "rec_delay": 10,
      "ProblemCode": [43]
    },
    {
      "friendly_name": "regex:^Example.*Adapter.*$",
      "max_retries": 5
    }
  ]
}
```

| 字段 | 说明 |
|------|------|
| `log` | **必填**对象 |
| `log.enabled` | `false` 时不写文件、不向控制台打印 |
| `log.level` | `"normal"` 或 `"debug"`（debug 含跳过原因、Ready 轮询细节等） |
| `log.path` | 日志路径；空/省略 → 可执行文件目录下 `PnP-Device-Recovery.log`；相对路径相对 exe 目录；绝对路径亦可 |
| `devices[].friendly_name` | 与设备管理器中的友好名称完全一致；或以 `regex:` 开头使用 Go RE2 |
| `devices[].max_retries` | 单次 Recovery Session 的最大尝试次数（必须 > 0） |
| `devices[].rec_delay` | 自动 Recover 前额外等待（**默认关**）。裸数字 `N` ≡ `{"based":"uptime","sec":N}`；对象须同时含 `based`（`uptime`\|`daemon`\|`device`）与 `sec`；**省略或 0** 则关闭不等。`check` 忽略。 |
| `devices[].ProblemCode` | 单个数字或数字数组；仅当设备当前 ProblemCode 落在集合内才 Recover；省略 = 不额外过滤 |
| `advanced` | **可选**对象；省略整个对象或其中字段时使用代码默认值（见「高级参数」） |

### Deprecated / 已移除字段与迁移（≥ v1.2）

自 **v1.2.0** 起下列字段 **已删除且无兼容映射**：JSON 仍可能解析通过，但旧键会被 **静默忽略、不再生效**。旧版（≤ v1.1.x）请先按表改写再升级。

| 旧字段 | 状态 | 迁移到 |
|--------|------|--------|
| `log_file` | 已移除（更早版本） | `log.path`（`log` 对象必填） |
| `devices[].delay` | **已移除** | `devices[].rec_delay` |
| `retry_delay`（顶层） | **已移除** | **不要**再配置。Disable→Enable 间隔改为代码内固定 **3 秒**；失败尝试间隔改为 `advanced.retry_interval`（默认 3） |

**`delay` → `rec_delay` 对照**

| 旧写法 | 新写法 | 含义 |
|--------|--------|------|
| 省略 `delay` | 省略 `rec_delay` 或 `"rec_delay": 0` | 不等待（默认关） |
| `"delay": 60` | `"rec_delay": 60` | 等价于 `{ "based": "uptime", "sec": 60 }`：系统 uptime ≥ 60s 才允许自动 Recover |
| （无对象形式） | `"rec_delay": { "based": "daemon", "sec": 30 }` | 本进程启动后满 30s |
| （无对象形式） | `"rec_delay": { "based": "device", "sec": 15 }` | 本会话首次匹配该设备后满 15s |
| `"retry_delay": 2` | 删除该键；需要时可设 `"advanced": { "retry_interval": 3 }` | 旧值曾用于 Disable/Enable **之后**等待；新语义拆开：Disable→Enable 固定 3s；失败重试间隔用 `retry_interval` |

`rec_delay` 时钟：
- `uptime`：`GetTickCount64` 开机以来
- `daemon`：本进程启动以来
- `device`：本会话中该设备首次被 Scan/Handle 匹配的时间（短暂消失不重置）

**硬编码（不可配置）**：Disable 与 Enable **之间**固定等待 **3 秒**；若跳过 Disable 则不等待；Enable 后复查前 **无**固定等待。

### 高级参数 `advanced`

日常可省略整个 `advanced`。仅在需要微调 Ready / 重试节奏 / 日志体积 / PnP 合并时再写。所有字段均可单独省略，省略则用默认值。

示例（按需裁剪字段）：

```json
{
  "log": { "enabled": true, "level": "normal", "path": "" },
  "devices": [
    { "friendly_name": "Example Device Name", "max_retries": 10, "rec_delay": 10, "ProblemCode": [43] }
  ],
  "advanced": {
    "retry_interval": 3,
    "ready_min_uptime_sec": 1,
    "ready_poll_ms": 200,
    "ready_max_wait_sec": 60,
    "pnp_debounce_ms": 500,
    "log_max_bytes": 10485760,
    "pnp_register_wait_sec": 3,
    "uptime_bypass": 60
  }
}
```

| 字段 | 默认 | 说明 |
|------|------|------|
| `retry_interval` | `3` | 一次 Recover **尝试失败后**，到下一次尝试前的等待秒数。**不是** Disable→Enable 间隔。须 ≥ 0。 |
| `ready_min_uptime_sec` | `1` | Ready 条件：系统 uptime 至少达到该秒数后才允许首次扫描 / 自动 Recover 入口。须 ≥ 0。 |
| `ready_poll_ms` | `200` | Ready 阶段轮询间隔（毫秒）。须 > 0。 |
| `ready_max_wait_sec` | `60` | Ready 最长等待秒数；超时后按实现继续（仍会记日志）。须 ≥ 0。 |
| `pnp_debounce_ms` | `500` | PnP 事件尾沿合并窗口（毫秒）：窗口内连续事件合并为一次扫描。须 ≥ 0。 |
| `log_max_bytes` | `10485760`（10 MiB） | 单日志文件超过该大小时，写入前截断覆盖。须 > 0。 |
| `pnp_register_wait_sec` | `3` | 启动时等待 PnP 通知注册完成的秒数。须 ≥ 0。 |
| `uptime_bypass` | `60` | 仅当 `rec_delay.based` 为 `uptime`（含裸数字形式）时生效：若当前系统 uptime ≥ 该值，则 **忽略** `rec_delay.sec`，立即允许自动 Recover。设为 `0` 关闭旁路。守护进程开得晚、uptime 已经很大时避免再空等 `sec`。须 ≥ 0。 |

启动时若目标设备尚未枚举：记录「device not found」并继续监听，待后续 PnP 事件再处理。

### 匹配规则

- 普通字符串：与 FriendlyName **完全匹配**
- `regex:…`：去掉前缀后按 Go RE2 编译（不支持 lookahead / lookbehind 等 PCRE 特性）

Instance ID 比较 **不区分大小写**，避免同一设备因大小写差异被当成两台。

---

## Recovery 行为

```
发现异常（且非 Exhausted）
    ↓
自动路径：未满 devices[].rec_delay？→ 跳过，继续监听
ProblemCode 已配置且不匹配？→ 跳过
    ↓
开启 Recovery Session（同设备事件合并，不并发）
    ↓
若当前为启用 → Disable；若已禁用则跳过 Disable（不强制）
    ↓
等待固定 3s（仅 Disable→Enable）
    ↓
若当前为禁用 → Enable；若已启用则跳过 Enable（不强制）
    ↓
复查
    ↓
┌──────────┴──────────┐
成功                   仍异常
 ↓                      ↓
计数清零，回到 Idle    attempt + 1
                        ↓
                   未达 max_retries？
                   ┌────┴────┐
                   是        否
                   ↓         ↓
                 继续重试   标记 Exhausted
                            （全局监听继续；
                             该设备不再自动 Recovery）
```

- 达到 `max_retries` **不会** `os.Exit()`，也 **不会** 停止全局 PnP 监听
- Exhausted 设备遇到普通 PnP 事件只记录状态，不自动开新会话
- `PnP-Device-Recovery.exe check` 清除 Exhausted 并重新检测；**忽略** `rec_delay`，但仍应用 ProblemCode 过滤与 Disable/Enable 状态门闩；若仍异常，新会话从 attempt = 1 开始
- 禁用/启用判定：CfgMgr `CM_PROB_DISABLED`（22）；设备可「已启用但仍不健康」（如 Code 43）

---

## 命令行

```text
PnP-Device-Recovery.exe              启动守护进程（单实例）
PnP-Device-Recovery.exe check        清除 Exhausted 并重新扫描
PnP-Device-Recovery.exe status       查看运行状态
PnP-Device-Recovery.exe help         显示用法（亦支持 -h / --help）
```

| 命令 | 行为 |
|------|------|
| 无参数 | 守护进程：互斥量 → UAC → 注册 PnP → 等待系统就绪 → 初始扫描 → 事件循环 |
| `check` | 通过 IPC 通知已运行的守护进程；若守护进程未运行，则执行一次性检查后退出（不留下第二个常驻进程） |
| `status` | 通过 IPC 读取实时状态；未运行时输出 `not running` 并以 0 退出 |
| `help` | 打印用法后退出（不占用互斥量） |
| 未知参数 | 打印用法，以非零退出码退出 |

- 单实例互斥量：`Global\PnPDeviceRecovery_SingleInstance`
- 本地命名管道：`\\.\pipe\PnPDeviceRecovery`（拒绝远程客户端）

`status` 输出包括：当前阶段（`STARTING` / `WAIT_FOR_SYSTEM_READY` / `NORMAL_RUNNING`）、监听是否已注册，以及每台配置设备的 Instance ID、健康状态、ProblemCode、Idle / Recovering / Exhausted、当前 attempt 与 `max_retries`。

---

## 日志

- 由 `log` 对象控制；`enabled=false` 时无文件、无控制台输出
- `level=debug` 时额外记录跳过原因、Ready 轮询细节等
- 编码：UTF-8
- 不限制行数
- 大小上限：`10 * 1024 * 1024`（10 MiB）；启用时超出则覆盖
- 写入前若 `当前大小 + 本行长度` 将超过上限，先覆盖（截断）旧内容再写入
- 默认文件名：`PnP-Device-Recovery.log`（exe 同目录）

典型字段包括：时间、FriendlyName、Instance ID、Status、ProblemCode、扫描原因（`startup` / `pnp_event` / `recovery_postcheck`）、触发事件、Recovery attempt、Disable/Enable 的 API 结果、最终结果等。

---

## 编译

源码位于 `src/`。

```bat
cd src
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w" -o ..\PnP-Device-Recovery.exe .
```

在 Linux / macOS 上交叉编译：

```bash
cd src
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../PnP-Device-Recovery.exe .
```

建议同时运行：

```bash
cd src
go test ./...
go test -race ./...
```

将生成的 `PnP-Device-Recovery.exe` 与 `config.json` 放在同一目录，以管理员身份运行（或依赖程序内置 UAC 提权）。

---

## 限制

- 仅在 Windows 上具备真实 PnP / SetupAPI / CfgMgr32 / UAC / 命名管道能力
- **不是**万能驱动修复工具：无法保证解决硬件故障、驱动包损坏、固件 / BIOS 问题或系统显示栈本身的缺陷
- Disable / Enable 只是对设备节点的软复位，成功与否取决于设备和驱动
- Exhausted 之后必须显式执行 `check`（或重启守护进程）才会再次自动恢复该设备
- 需要管理员权限

---

## 故障排查

下列命令仅供人工诊断，**程序自身不会调用**：

```bat
pnputil /enum-devices /problem
```

| 现象 | 可能原因 |
|------|----------|
| 启动后立即退出 | `config.json` 校验失败；或单实例互斥量已被占用 |
| UAC 后仍退出 | 用户拒绝提升权限 |
| `status` 显示 not running | 守护进程未启动 |
| Exhausted 后不再自动恢复 | 预期行为；运行 `check` |
| 启动后较久才第一次扫描 | 正在等待 uptime ≥ 1s 或 `devices[].rec_delay` |
| 找不到设备 | FriendlyName 与设备管理器不一致；可改用 `regex:` |
| 日志突然变短 | 已超过 10 MiB，写入前截断覆盖 |
| Disable / Enable 失败 | 查看日志中的 `api=… result=CR_xxx` |

---

## 案例：独显 Code 43 自动恢复

以下为真实环境中的一种用法示例，并非程序唯一用途。任意可通过 FriendlyName 匹配、且 Disable/Enable 有效的 PnP 设备均可纳入配置。

某笔记本**内置显示器损坏**后，独显报 **Code 43**，外接显示器也无法正常使用。将 FriendlyName（例如 `NVIDIA GeForce RTX 3060 Laptop GPU`）写入 `config.json`，并配置 `ProblemCode: [43]` 与合适的 `rec_delay` 后，程序在 Ready 与延时条件满足后检测到该状态，执行 Disable → Enable；随后可见相关 GPU / Display 等 PnP 到达与移除事件，复查后 ProblemCode 回到 0，外接输出恢复可用。

在 **dGPU-only / MUX** 等机器上，若在 Boot 画面阶段就对 GPU 做 Disable/Enable，可能造成显示输出异常甚至黑屏。本工具以系统 uptime ≥ 1s 作为 Ready，并建议用 `devices[].rec_delay` / `ProblemCode` 进一步推迟与过滤自动 Recover。

---

## 目录结构

```
├── README.md
├── config.json          # 示例配置
└── src/                 # Go 源码与测试
    ├── go.mod
    ├── main.go
    ├── …
```

模块路径：`github.com/XHQ6666/PnP-Device-Recovery`

| 构建标签 | 说明 |
|----------|------|
| `*_windows.go` | SetupAPI / CfgMgr32 / PnP / UAC / WTS / 命名管道 / 互斥量 |
| `*_other.go` | 非 Windows stub，便于在 Linux 上跑单元测试 |

---

## 免责声明

本软件按原样提供，不附带任何明示或暗示担保。使用前请自行评估 Disable / Enable 设备节点的风险。

---

<a id="english"></a>

# English

[中文](#pnp-device-recovery) | [English](#english)

A lightweight **Windows PnP device auto-recovery** daemon.

On real Windows systems, some device nodes occasionally enter a problem state (Device Manager problem codes such as Code 10 / 31 / 43). This tool listens for configured targets via native Windows PnP notifications and, when a device is unhealthy, performs a controlled soft reset:

**Disable Device → fixed 3s → Enable Device → post-check** (failed attempts use `advanced.retry_interval`)

It calls SetupAPI / CfgMgr32 and related Windows APIs directly. It does **not** shell out to PowerShell, `pnputil`, or WMIC.

> **Safety**  
> Disable / Enable temporarily interrupts the device and may cause brief disconnects or output loss. Run elevated, and only in environments where you accept the risk. This tool cannot fix hardware failure, corrupted driver packages, firmware, or BIOS issues.

---

## What it is for

| Scenario | Description |
|----------|-------------|
| Intermittent device faults | The device is still enumerated but has a Problem Code and does not work |
| Automated soft reset | Manually Disable → Enable in Device Manager often helps; this automates it unattended |
| Multiple devices | Monitor several devices in parallel, each with its own retry state |
| At-startup tasks | Safe to run at logon/startup: it waits until the system is stable before the first scan, avoiding early-boot device operations |

---

## How it works

```
Windows PnP
     │
     ▼
PnP Event (event-driven, not fixed polling)
     │
     ▼
Trailing-edge coalescing
     │
     ▼
Enumerate and match configured FriendlyName
     │
     ▼
Read Status / ProblemCode
     │
 ┌───┴────┐
OK     Problem
 │         │
done    Recovery Session
           │
       Disable
           │
        Enable
           │
      Re-check
```

**Monitoring**: prefers `CM_Register_Notification`; falls back to `RegisterDeviceNotification` + a hidden-window `WM_DEVICECHANGE` pump. The PnP callback **never** Disable/Enable — work is queued to workers.

**Healthy** (CfgMgr32):

- no `DN_HAS_PROBLEM`
- `ProblemCode == 0`

Anything else is treated as unhealthy (including but not limited to Codes 10 / 31 / 43).

---

## Startup sequence

The daemon does **not** Disable/Enable devices immediately at process start. Ready means **system uptime ≥ 1 second** (internal constant; not in config).

```
Process start
  │
  ├─ Parse CLI
  ├─ Acquire single-instance mutex
  ├─ UAC elevation if needed
  │
  ▼
STARTING
  │  Register PnP notifications
  │  (events only mark pending — no Recovery)
  │
  ▼
WAIT_FOR_SYSTEM_READY
  │  GetTickCount64: uptime ≥ 1s counts as boot-complete for this gate
  │  (internal poll; overall timeout ~1 minute; then continue with a warning — no infinite hang)
  │  LogonUI / input-desktop name / explorer are NOT used
  │
  ▼
INITIAL_SCAN (reason=startup)
  │  First automatic Recovery allowed here (still subject to devices[].rec_delay / ProblemCode)
  │
  ▼
NORMAL_RUNNING
     Event-driven + trailing-edge coalescing
```

Defer automatic Recover with `devices[].rec_delay` / `ProblemCode`. Ready uptime/poll/max-wait are tunable via optional `advanced` (defaults if omitted).

---

## Features

- **Event-driven PnP listening** (not fixed-interval polling)
- **Exact FriendlyName** and `regex:` (Go RE2) matching
- **Automatic Disable → Enable → re-check**
- **Multi-device**: independent sessions and counters; parallel across devices, serial per device
- **Trailing-edge coalescing**: bursts of PnP events collapse into one scan
- **Exhausted state**: after `max_retries`, no more automatic recovery until `check`
- **Ready before first scan**: system uptime ≥ 1s
- **Per-device rec_delay / ProblemCode**: defer automatic Recover; filter by problem codes
- **Disable/Enable state gates**: Disable only when enabled; Enable only when disabled
- **Single instance + local named-pipe IPC**
- **UAC auto-elevation**
- **UTF-8 log** (`log` object): can disable; `normal`/`debug`; max **10 MiB**

---

## Configuration

Place `config.json` next to the executable. The `log` object is **required**. Relative log paths resolve against the **executable directory** (not the process cwd). The old `log_file` field is **not** supported.

```json
{
  "log": {
    "enabled": true,
    "level": "normal",
    "path": ""
  },
  "devices": [
    {
      "friendly_name": "Example Device Name",
      "max_retries": 10,
      "rec_delay": 10,
      "ProblemCode": [43]
    },
    {
      "friendly_name": "regex:^Example.*Adapter.*$",
      "max_retries": 5
    }
  ]
}
```

| Field | Meaning |
|-------|---------|
| `log` | **Required** object |
| `log.enabled` | When `false`: no file writes and no console prints |
| `log.level` | `"normal"` or `"debug"` (debug includes skip reasons, Ready poll details, etc.) |
| `log.path` | Log path; empty/omit → `PnP-Device-Recovery.log` under the exe directory; relative paths are relative to the exe directory; absolute paths OK |
| `devices[].friendly_name` | Exact Device Manager friendly name, or `regex:` + Go RE2 |
| `devices[].max_retries` | Max attempts per Recovery Session (must be > 0) |
| `devices[].rec_delay` | Extra wait before **automatic** Recover (**off by default**). Bare number `N` ≡ `{"based":"uptime","sec":N}`; object requires `based` (`uptime`\|`daemon`\|`device`) and `sec`; **omit or 0** disables. Ignored by `check`. |
| `devices[].ProblemCode` | Number or array of numbers; Recover only when the device's current ProblemCode is in the set; omit = no extra filter |
| `advanced` | **Optional** object; omit the whole object or any field → code defaults (see **Advanced settings**) |

### Deprecated / removed fields and migration (≥ v1.2)

Starting with **v1.2.0**, the fields below are **removed with no compatibility mapping**: JSON may still load, but old keys are **silently ignored and have no effect**. Migrate configs from ≤ v1.1.x before upgrading.

| Old field | Status | Migrate to |
|-----------|--------|------------|
| `log_file` | Removed earlier | `log.path` (`log` object required) |
| `devices[].delay` | **Removed** | `devices[].rec_delay` |
| `retry_delay` (top-level) | **Removed** | **Do not** set. Disable→Enable gap is a fixed **3 seconds** in code; failed-attempt spacing is `advanced.retry_interval` (default 3) |

**`delay` → `rec_delay`**

| Old | New | Meaning |
|-----|-----|---------|
| omit `delay` | omit `rec_delay` or `"rec_delay": 0` | no wait (off by default) |
| `"delay": 60` | `"rec_delay": 60` | same as `{ "based": "uptime", "sec": 60 }`: allow auto Recover only when system uptime ≥ 60s |
| (no object form) | `"rec_delay": { "based": "daemon", "sec": 30 }` | 30s after this process started |
| (no object form) | `"rec_delay": { "based": "device", "sec": 15 }` | 15s after first match of this device in the session |
| `"retry_delay": 2` | delete the key; optionally `"advanced": { "retry_interval": 3 }` | old value waited after Disable/Enable; new model: fixed 3s only between Disable and Enable; use `retry_interval` between failed attempts |

`rec_delay` clocks:
- `uptime`: `GetTickCount64` since boot
- `daemon`: since this process started
- `device`: first Scan/Handle match for this device in the session (not reset on transient disappear)

**Hardcoded (not configurable):** fixed **3 seconds** only **between** Disable and Enable; no wait if Disable is skipped; **no** fixed wait after Enable before post-check.

### Advanced settings (`advanced`)

Omit the whole `advanced` object for normal use. Add it only to tune Ready / retry pacing / log size / PnP coalescing. Every field is optional; omitted fields use defaults.

Example (trim fields as needed):

```json
{
  "log": { "enabled": true, "level": "normal", "path": "" },
  "devices": [
    { "friendly_name": "Example Device Name", "max_retries": 10, "rec_delay": 10, "ProblemCode": [43] }
  ],
  "advanced": {
    "retry_interval": 3,
    "ready_min_uptime_sec": 1,
    "ready_poll_ms": 200,
    "ready_max_wait_sec": 60,
    "pnp_debounce_ms": 500,
    "log_max_bytes": 10485760,
    "pnp_register_wait_sec": 3,
    "uptime_bypass": 60
  }
}
```

| Field | Default | Meaning |
|-------|---------|---------|
| `retry_interval` | `3` | Seconds to wait **after a failed Recover attempt** before the next attempt. **Not** the Disable→Enable gap. Must be ≥ 0. |
| `ready_min_uptime_sec` | `1` | Ready: minimum system uptime (seconds) before first scan / auto Recover entry. Must be ≥ 0. |
| `ready_poll_ms` | `200` | Ready poll interval (ms). Must be > 0. |
| `ready_max_wait_sec` | `60` | Ready max wait (seconds); on timeout the process continues per implementation (still logged). Must be ≥ 0. |
| `pnp_debounce_ms` | `500` | PnP trailing-edge coalesce window (ms): bursts collapse into one scan. Must be ≥ 0. |
| `log_max_bytes` | `10485760` (10 MiB) | Truncate/overwrite the log file before write when it exceeds this size. Must be > 0. |
| `pnp_register_wait_sec` | `3` | Seconds to wait for PnP notification registration at startup. Must be ≥ 0. |
| `uptime_bypass` | `60` | Only when `rec_delay.based` is `uptime` (including bare-number form): if current system uptime ≥ this value, **ignore** `rec_delay.sec` and allow auto Recover immediately. `0` disables the bypass. Avoids waiting `sec` again when the daemon starts late and uptime is already large. Must be ≥ 0. |

If a target is missing at startup, the process logs “device not found” and keeps listening.

### Matching rules

- Plain string: **exact** FriendlyName match
- `regex:…`: strip the prefix and compile with Go RE2 (no PCRE lookaround)

Instance ID comparison is **case-insensitive**.

---

## Recovery behavior

```
Unhealthy (and not Exhausted)
    ↓
Auto path: devices[].rec_delay not elapsed? → skip, keep listening
ProblemCode configured and not in set? → skip
    ↓
Start Recovery Session (coalesce per device; no concurrent sessions)
    ↓
If currently enabled → Disable; if already disabled, skip Disable (do not force)
    ↓
wait fixed 3s (Disable→Enable only)
    ↓
If currently disabled → Enable; if already enabled, skip Enable (do not force)
    ↓
re-check
    ↓
┌──────────┴──────────┐
Success                Still unhealthy
 ↓                      ↓
Reset to Idle          attempt + 1
                        ↓
                   Under max_retries?
                   ┌────┴────┐
                   Yes       No
                   ↓         ↓
                 Retry    Mark Exhausted
                          (global listener continues;
                           no auto Recovery for this device)
```

- Hitting `max_retries` does **not** `os.Exit()` and does **not** stop the global PnP listener
- Exhausted devices only log on ordinary PnP events; no new automatic session
- `PnP-Device-Recovery.exe check` clears Exhausted and rescans; it **ignores** `rec_delay` but still applies the ProblemCode filter and Disable/Enable state gates; a new session starts at attempt = 1 if still unhealthy
- Disabled vs enabled: CfgMgr `CM_PROB_DISABLED` (22); a device may be enabled yet unhealthy (e.g. Code 43)

---

## Command line

```text
PnP-Device-Recovery.exe              Run daemon (single instance)
PnP-Device-Recovery.exe check        Clear Exhausted and rescan
PnP-Device-Recovery.exe status       Show daemon status
PnP-Device-Recovery.exe help         Usage (-h / --help also work)
```

| Command | Behavior |
|---------|----------|
| (none) | Daemon: mutex → UAC → register PnP → wait until ready → initial scan → event loop |
| `check` | IPC to the running daemon; if none, one-shot check then exit (no second resident listener) |
| `status` | IPC status dump; if not running, print `not running` and exit 0 |
| `help` | Print usage and exit (no mutex) |
| unknown | Print usage and exit non-zero |

- Mutex: `Global\PnPDeviceRecovery_SingleInstance`
- Pipe: `\\.\pipe\PnPDeviceRecovery` (remote clients rejected)

`status` includes phase (`STARTING` / `WAIT_FOR_SYSTEM_READY` / `NORMAL_RUNNING`), whether the listener is registered, and per configured device: Instance ID, health, ProblemCode, Idle / Recovering / Exhausted, attempt, and `max_retries`.

---

## Logging

- Controlled by the `log` object; `enabled=false` means no file and no console output
- `level=debug` adds skip reasons, Ready poll details, etc.
- Encoding: UTF-8
- No line-count limit
- Size cap: `10 * 1024 * 1024` (10 MiB); when enabled, overwrite when exceeded
- Before write: if `current_size + line_length` would exceed the cap, truncate then write
- Default filename: `PnP-Device-Recovery.log` (next to the exe)

Typical fields: time, FriendlyName, Instance ID, Status, ProblemCode, scan reason (`startup` / `pnp_event` / `recovery_postcheck`), trigger event, Recovery attempt, Disable/Enable API results, final outcome.

---

## Build

Sources live under `src/`.

```bat
cd src
set GOOS=windows
set GOARCH=amd64
set CGO_ENABLED=0
go build -trimpath -ldflags="-s -w" -o ..\PnP-Device-Recovery.exe .
```

Cross-compile on Linux / macOS:

```bash
cd src
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../PnP-Device-Recovery.exe .
```

Recommended tests:

```bash
cd src
go test ./...
go test -race ./...
```

Place `PnP-Device-Recovery.exe` and `config.json` in the same folder. Run elevated, or rely on built-in UAC relaunch.

---

## Limitations

- Real PnP / SetupAPI / CfgMgr32 / UAC / named pipes exist only on Windows
- **Not** a universal driver fixer: it cannot guarantee fixes for hardware failure, corrupted packages, firmware/BIOS, or display-stack defects
- Disable / Enable is only a soft reset of the device node
- After Exhausted, you must run `check` (or restart the daemon) to retry that device automatically
- Administrator rights are required

---

## Troubleshooting

These commands are for **manual** diagnosis only; the program does **not** invoke them:

```bat
pnputil /enum-devices /problem
```

| Symptom | Likely cause |
|---------|----------------|
| Exits immediately | Invalid `config.json`, or single-instance mutex already held |
| Still exits after UAC | Elevation denied |
| `status` → not running | Daemon not started |
| No auto recovery after Exhausted | Expected; run `check` |
| Long delay before first scan | Waiting for uptime ≥ 1s or devices[].rec_delay |
| Device not found | FriendlyName mismatch; try `regex:` |
| Log suddenly short | Exceeded 10 MiB; truncated before write |
| Disable / Enable failed | Check log for `api=… result=CR_xxx` |

---

## Case study: discrete GPU Code 43

This is one real-world usage example, not the only purpose. Any PnP device that matches by FriendlyName and benefits from Disable/Enable can be configured.

After a laptop **built-in panel failed**, the discrete GPU reported **Code 43** and the **external display was also unusable**. With its FriendlyName (e.g. `NVIDIA GeForce RTX 3060 Laptop GPU`) in `config.json`, plus `ProblemCode: [43]` and an appropriate `rec_delay`, the tool detected that state after Ready/delay, ran Disable → Enable, observed related GPU / Display PnP arrival/removal events, then saw ProblemCode return to 0 and external output become usable again.

On some **dGPU-only / MUX** machines, Disable/Enable of the GPU during the Boot screen can blank the display. This tool uses system uptime ≥ 1s as Ready; further gate automatic Recover with `devices[].rec_delay` / `ProblemCode`.

---

## Layout

```
├── README.md
├── config.json          # sample config
└── src/                 # Go sources and tests
    ├── go.mod
    ├── main.go
    ├── …
```

Module path: `github.com/XHQ6666/PnP-Device-Recovery`

| Build tags | Role |
|------------|------|
| `*_windows.go` | SetupAPI / CfgMgr32 / PnP / UAC / WTS / named pipe / mutex |
| `*_other.go` | Non-Windows stubs for Linux unit tests |

---

## Disclaimer

Provided as-is, without warranty of any kind. Evaluate the risk of Disable / Enable before use.
