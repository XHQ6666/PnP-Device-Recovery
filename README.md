# PnP Device Recovery

[中文](#pnp-device-recovery) | [English](#english)

轻量级 **Windows PnP 设备自动恢复** 守护进程。

在真实 Windows 环境中，部分设备节点会偶发进入异常状态（设备管理器中的问题代码，例如 Code 10 / 31 / 43）。本工具通过 Windows 原生 PnP 通知持续监听配置中的目标设备；一旦确认设备不健康，即在受控条件下自动执行一次软复位式恢复：

**Disable Device → 等待 → Enable Device → 等待驱动重新初始化 → 再次检查状态**

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
| 开机计划任务 | 可作为开机启动项运行，但会等待系统进入稳定阶段后再做首次扫描，避免启动早期过早操作设备 |

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

守护进程不会在进程一启动就对设备执行 Disable / Enable。启动阶段 Windows 仍在完成会话与设备栈初始化，过早操作可能带来不稳定。

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
  │  等待交互式用户会话就绪（WTSActive 等）
  │  辅以 Shell 进程等软信号
  │  再加一段内部安全缓冲
  │  （超时后带警告继续，避免永久挂起）
  │
  ▼
INITIAL_SCAN（reason=startup）
  │  此时才允许首次 Recovery
  │
  ▼
NORMAL_RUNNING
     事件驱动 + 尾沿合并
```

就绪等待、安全缓冲、防抖间隔均为 **内部常量**，不会出现在 `config.json` 中。

---

## 主要功能

- **事件驱动 PnP 监听**（非固定轮询）
- **FriendlyName 精确匹配** 与 `regex:`（Go RE2）匹配
- **自动 Disable → Enable → 复查**
- **多设备**：独立会话、独立重试计数；不同设备可并行，同一设备串行
- **尾沿事件合并**：短时间内连续 PnP 事件合并为一次扫描，减轻枚举风暴
- **Exhausted 状态**：达到 `max_retries` 后该设备停止自动恢复；仅 `check` 可清除并重试
- **启动延迟扫描**：系统稳定后再做首次扫描
- **单实例 + 本地命名管道 IPC**
- **UAC 自动提权**
- **UTF-8 日志**，上限 10 MiB，超出后覆盖旧内容

---

## 配置

将 `config.json` 与可执行文件放在同一目录。配置结构保持精简，请勿自行添加启动延时、防抖、轮询间隔等字段。

```json
{
    "devices": [
        {
            "friendly_name": "Example Device Name",
            "max_retries": 10
        },
        {
            "friendly_name": "regex:^Example.*Adapter.*$",
            "max_retries": 5
        }
    ],
    "retry_delay": 2,
    "log_file": "PnP-Device-Recovery.log"
}
```

| 字段 | 说明 |
|------|------|
| `devices[].friendly_name` | 与设备管理器中的友好名称完全一致；或以 `regex:` 开头使用 Go RE2 |
| `devices[].max_retries` | 单次 Recovery Session 的最大尝试次数（必须 > 0） |
| `retry_delay` | Disable / Enable 之后的等待秒数（必须 ≥ 0） |
| `log_file` | 日志文件路径（不可为空） |

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
开启 Recovery Session（同设备事件合并，不并发）
    ↓
Disable → 等待 retry_delay → Enable → 等待 → 复查
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
- 只有执行 `PnP-Device-Recovery.exe check` 才会清除 Exhausted 并重新检测；若仍异常，新会话从 attempt = 1 开始

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

- 编码：UTF-8
- 不限制行数
- 大小上限：`10 * 1024 * 1024`（10 MiB）
- 写入前若 `当前大小 + 本行长度` 将超过上限，先覆盖（截断）旧内容再写入

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
| 启动后较久才第一次扫描 | 正在等待会话就绪与内部安全缓冲 |
| 找不到设备 | FriendlyName 与设备管理器不一致；可改用 `regex:` |
| 日志突然变短 | 已超过 10 MiB，写入前截断覆盖 |
| Disable / Enable 失败 | 查看日志中的 `api=… result=CR_xxx` |

---

## 案例：独显 Code 43 自动恢复

以下为真实环境中的一种用法示例，并非程序唯一用途。任意可通过 FriendlyName 匹配、且 Disable/Enable 有效的 PnP 设备均可纳入配置。

某笔记本独显偶发进入 **Code 43**。将 FriendlyName（例如 `NVIDIA GeForce RTX 3060 Laptop GPU`）写入 `config.json` 后，程序在系统就绪并完成初始扫描时检测到异常，执行 Disable → Enable；随后可见相关 GPU / Display 等 PnP 到达与移除事件，复查后 ProblemCode 回到 0，Recovery 成功。

在 **dGPU-only / MUX** 等机器上，若在 Windows 刚启动、显示栈尚未稳定时就对 GPU 做 Disable/Enable，可能造成显示输出异常甚至黑屏。这也是本工具坚持「先等系统就绪，再做首次扫描」的重要原因之一；该策略对其他在启动早期尚不稳定的设备同样适用。

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

**Disable Device → wait → Enable Device → wait for driver re-initialization → re-check status**

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

The daemon does **not** Disable/Enable devices immediately at process start. During early boot, Windows is still bringing up the user session and device stacks; operating too early can be unstable.

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
  │  Wait for an interactive session (e.g. WTSActive)
  │  Soft signals such as Shell process presence
  │  Internal safety buffer
  │  (timeout continues with a warning — no infinite hang)
  │
  ▼
INITIAL_SCAN (reason=startup)
  │  First Recovery allowed only here
  │
  ▼
NORMAL_RUNNING
     Event-driven + trailing-edge coalescing
```

Ready-wait, safety buffer, and debounce intervals are **internal constants** and are **not** part of `config.json`.

---

## Features

- **Event-driven PnP listening** (not fixed-interval polling)
- **Exact FriendlyName** and `regex:` (Go RE2) matching
- **Automatic Disable → Enable → re-check**
- **Multi-device**: independent sessions and counters; parallel across devices, serial per device
- **Trailing-edge coalescing**: bursts of PnP events collapse into one scan
- **Exhausted state**: after `max_retries`, no more automatic recovery until `check`
- **Deferred initial scan** after the system is ready
- **Single instance + local named-pipe IPC**
- **UAC auto-elevation**
- **UTF-8 log**, max **10 MiB**, overwrite when exceeded

---

## Configuration

Place `config.json` next to the executable. Keep the schema minimal — do not add startup delay, debounce, or poll-interval fields.

```json
{
    "devices": [
        {
            "friendly_name": "Example Device Name",
            "max_retries": 10
        },
        {
            "friendly_name": "regex:^Example.*Adapter.*$",
            "max_retries": 5
        }
    ],
    "retry_delay": 2,
    "log_file": "PnP-Device-Recovery.log"
}
```

| Field | Meaning |
|-------|---------|
| `devices[].friendly_name` | Exact Device Manager friendly name, or `regex:` + Go RE2 |
| `devices[].max_retries` | Max attempts per Recovery Session (must be > 0) |
| `retry_delay` | Seconds to wait after Disable / Enable (must be ≥ 0) |
| `log_file` | Log path (must be non-empty) |

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
Start Recovery Session (coalesce per device; no concurrent sessions)
    ↓
Disable → wait retry_delay → Enable → wait → re-check
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
- Only `PnP-Device-Recovery.exe check` clears Exhausted and rescans; a new session starts at attempt = 1 if still unhealthy

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

- Encoding: UTF-8
- No line-count limit
- Size cap: `10 * 1024 * 1024` (10 MiB)
- Before write: if `current_size + line_length` would exceed the cap, truncate then write

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
| Long delay before first scan | Waiting for session ready + internal buffer |
| Device not found | FriendlyName mismatch; try `regex:` |
| Log suddenly short | Exceeded 10 MiB; truncated before write |
| Disable / Enable failed | Check log for `api=… result=CR_xxx` |

---

## Case study: discrete GPU Code 43

This is one real-world usage example, not the only purpose. Any PnP device that matches by FriendlyName and benefits from Disable/Enable can be configured.

A laptop dGPU occasionally entered **Code 43**. After putting its FriendlyName (e.g. `NVIDIA GeForce RTX 3060 Laptop GPU`) in `config.json`, the tool detected the fault on the post-ready initial scan, ran Disable → Enable, observed related GPU / Display PnP arrival/removal events, then saw ProblemCode return to 0 and Recovery succeed.

On some **dGPU-only / MUX** machines, Disable/Enable of the GPU before the display stack is stable can blank the screen. That is one reason this tool waits for system readiness before the first scan — the same policy helps other devices that are unstable early in boot.

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
