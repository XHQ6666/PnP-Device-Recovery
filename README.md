# PnP Device Recovery

Windows 即插即用（PnP）设备自动恢复工具。以 **事件驱动 + 尾沿合并（trailing-edge debounce）** 监听设备变化；当配置中的设备不健康（`DN_HAS_PROBLEM` 和/或 `ProblemCode != 0`）时，执行 **禁用 → 等待 → 启用 → 再检查**。

> **安全声明 / Disclaimer**  
> 本工具会禁用并重新启用硬件设备节点，可能导致短暂断网、黑屏或系统不稳定。请仅在了解风险的环境中以管理员身份使用。作者不对数据丢失或硬件损坏负责。故障排查时可使用系统自带的 `pnputil`（仅供人工诊断，**本程序从不调用** pnputil / PowerShell / WMIC）。

---

## 为什么启动时绝不立即 Recovery（dGPU / MUX 黑屏）

笔记本电脑在登录与桌面就绪之前，混合显卡（dGPU）和 MUX 切换尚未稳定。若在此时对 RTX 等独显做 Disable/Enable，显示器可能停在黑屏、驱动未完成初始化。

因此守护进程 **禁止** 在创建监听器后立刻扫描恢复。必须等到交互会话稳定，再做一次 `reason=startup` 的初始扫描。

```
启动
  │
  ├─ 解析 CLI（无参数 = 守护进程）
  ├─ 单实例互斥量
  ├─ 如需则 UAC 提权（仅守护进程 / 单次 check）
  │
  ▼
phase = STARTING
  │
  ├─ 注册 PnP 通知（CM_Register_Notification / WM_DEVICECHANGE）
  │     此阶段到达的 PnP 事件：只记 pending，不 Recovery
  │
  ▼
phase = WAIT_FOR_SYSTEM_READY
  │
  ├─ WTSGetActiveConsoleSessionId + WTSQuerySessionInformation
  │     等待 WTSActive 交互会话
  ├─ explorer.exe 作为软信号（非唯一条件）
  ├─ 会话就绪后再加一段硬编码安全缓冲（约 12 秒）
  ├─ 最长等待约 8 分钟后带警告继续，避免永久挂起
  │
  │  此阶段 PnP 事件：只打日志 “startup PnP event pending”，不 Recovery
  │
  ▼
INITIAL_SCAN  reason=startup
  │     此时才允许第一次 Recovery
  │
  ▼
phase = NORMAL_RUNNING
        事件驱动 + 尾沿合并；DISPLAY 事件只触发对配置目标的扫描
```

就绪检测的超时、缓冲、防抖间隔均为 **内部常量**，**不会** 写入 `config.json`。

---

## 事件驱动与尾沿合并

监控 **不以固定轮询为主**：

1. 优先 `CM_Register_Notification`
2. 回退 `RegisterDeviceNotification` + 隐藏窗口 `WM_DEVICECHANGE`
3. **PnP 回调内绝不 Disable/Enable**，只入队

进入 `NORMAL_RUNNING` 后：

- 每个 PnP 事件记录 `trigger_instance` / `trigger_action`，并 **重置** 内部防抖定时器（约 500ms）
- 定时器触发后，对配置设备做 **一次** 扫描（`reason=pnp_event`，带 `trigger_*` 字段）
- **DISPLAY 事件同样进入合并队列** → 扫描配置目标；**不会** 把 DISPLAY 实例直接映射成 RTX 恢复
- 某设备已在 Recovering：只合并，不开启第二会话

启动阶段（`STARTING` / `WAIT_FOR_SYSTEM_READY`）的 PnP 事件不启动恢复，只设 pending；就绪后的那一次初始扫描覆盖它们。

---

## Exhausted 与 `check`

每个设备（键 = **大小写不敏感** 的 InstanceID）有三种状态：

| 状态 | 含义 |
|------|------|
| Idle | 空闲 |
| Recovering | 正在 Disable → Enable → 再检查 |
| Exhausted | 已用尽 `max_retries` |

- 耗尽后标记 **Exhausted**，尝试计数清零（便于 status 显示），**普通 PnP 事件不再自动开新会话**
- 只有 CLI / IPC 的 **`check`** 会清除 Exhausted 并重新扫描；若仍不健康，新会话从 **attempt=1** 开始
- 不同设备可并行；同一设备串行

这改变了旧版「新 PnP 会话自动从 attempt 1 再试」的语义，避免反复折腾已放弃的设备。

---

## CLI、单实例、IPC

```text
PnP-Device-Recovery.exe              守护进程（单实例）
PnP-Device-Recovery.exe check        清除 Exhausted 并重扫
PnP-Device-Recovery.exe status       打印守护进程状态
PnP-Device-Recovery.exe help         用法（-h / --help）
```

| 命令 | 行为 |
|------|------|
| 无参数 | 守护进程：互斥量 → UAC → 监听 → 等系统就绪 → 初始扫描 → 事件循环 |
| `check` | 经命名管道通知守护进程；**若守护进程未运行**则单次检查后退出（不留下监听器） |
| `status` | IPC 状态转储；无守护进程时打印 `not running` 并以退出码 0 结束 |
| `help` / `-h` / `--help` | 用法，退出码 0（不需要互斥量或守护进程） |
| 未知参数 | 打印用法，退出码非 0 |

- 单实例互斥量：`Global\PnPDeviceRecovery_SingleInstance`
- 本地命名管道：`\\.\pipe\PnPDeviceRecovery`（`PIPE_REJECT_REMOTE_CLIENTS`）
- IPC：一行 JSON，`{"cmd":"check"}` 或 `{"cmd":"status"}`，有基本校验
- 配置固定为可执行文件旁的 `config.json`（第一参数不再当作配置路径）

### status 输出

- `phase`：`STARTING` / `WAIT_FOR_SYSTEM_READY` / `NORMAL_RUNNING`
- `listener_registered`
- 每个配置设备：`instance`、`healthy`、`problem`、`state`（Idle / Recovering / Exhausted）、`attempt`、`max_retries`

---

## 多设备

- `friendly_name` 精确匹配；`regex:` 前缀使用 Go RE2
- 每个设备独立会话、独立 Exhausted
- InstanceID 一律 `ToUpper` / `EqualFold` 规范化，避免 `PCI\VEN_…` 与 `pci\ven_…` 被当成两台设备

---

## 配置（config.json）

**不要** 增加 `startup_delay`、`debounce`、`poll_interval` 等字段。就绪等待、防抖、安全缓冲都是内部常量。

```json
{
    "devices": [
        {
            "friendly_name": "NVIDIA GeForce RTX 3060 Laptop GPU",
            "max_retries": 10
        },
        {
            "friendly_name": "regex:^Intel.*BE200.*$",
            "max_retries": 5
        }
    ],
    "retry_delay": 2,
    "log_file": "PnP-Device-Recovery.log"
}
```

| 字段 | 含义 |
|------|------|
| `devices[].friendly_name` | 精确匹配或 `regex:` + RE2 |
| `devices[].max_retries` | 单次恢复会话最大尝试次数（必须 > 0） |
| `retry_delay` | 禁用/启用后的等待秒数（必须 >= 0） |
| `log_file` | 日志路径（不可为空） |

启动/扫描时设备不存在：记警告并继续监听。

---

## 恢复算法（未改）

```
检测到不健康（且非 Exhausted）→ 开启/复用会话（同设备 coalesce）
  attempt++
  if attempt > max_retries → 标记 Exhausted（计数归零供显示）；监听继续
  Disable → sleep(retry_delay) → Enable → sleep(retry_delay) → 复查
  成功 → Idle，计数清零
```

Disable/Enable 失败日志形如：`api=CM_Disable_DevNode result=CR_xxx (0x..)`（如有则附加 win32）。

CM 回调经常没有 DEVINST：日志写 `event_devinst_unset`，**不会把 0 当成真实句柄**；`CM_Locate_DevNode` 成功后写 `resolved_DEVINST=...`。

---

## 日志

- UTF-8
- **写入前** 判断：若 `当前大小 + 本行长度 > 10 MiB（10×1024×1024）`，先截断再写
- 默认文件名：`PnP-Device-Recovery.log`

---

## 编译与运行

源码在 `src/`。在 Linux 上交叉编译：

```bash
cd src
go test ./...
go test -race ./...
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../PnP-Device-Recovery.exe .
```

```bash
file PnP-Device-Recovery.exe
sha256sum PnP-Device-Recovery.exe
```

将 `PnP-Device-Recovery.exe` 与 `config.json` 放在同一目录，以管理员身份运行。

Linux 下 `*_other.go` 为 stub，便于 `go test` 覆盖配置、匹配、日志、CLI、防抖、Exhausted 状态机。WTS / 命名管道 / 真实 Disable-Enable 只能在 Windows 上验证。

---

## 限制

- 仅 Windows 上有真实 PnP / SetupAPI / CfgMgr32 / UAC / 命名管道
- 不能修复损坏的驱动或硬件；Disable/Enable 只是软复位设备节点
- 独显恢复仍可能导致短暂黑屏——这正是启动阶段要等会话就绪的原因
- Exhausted 后必须人工 `check`（或重启守护进程）才会再试
- 混合显卡 / MUX / 外接显示器拓扑因机器而异，DISPLAY 事件只用来触发扫描，不会对错误的实例做恢复
- 需要管理员权限才能 Disable/Enable

---

## 故障排查（人工）

本程序 **不** 调用下列工具：

```bat
pnputil /enum-devices /problem
pnputil /restart-device "PCI\VEN_...."
```

| 现象 | 可能原因 |
|------|----------|
| 启动即退出 | 配置校验失败；或单实例互斥量已被占用 |
| UAC 后仍退出 | 用户拒绝提升 |
| `status` 显示 not running | 守护进程未启动 |
| Exhausted 后不再恢复 | 预期行为；运行 `check` |
| 启动后很久才第一次扫描 | 正在等 WTSActive + 安全缓冲 |
| 找不到设备 | FriendlyName 与设备管理器不一致；改用 `regex:` |
| 日志突然变短 | 已超过 10MiB，写入前截断 |

---

## 模块路径

`github.com/XHQ6666/PnP-Device-Recovery`

## 构建标签

| 文件 | 标签 | 说明 |
|------|------|------|
| `*_windows.go` | `windows` | SetupAPI / CfgMgr32 / PnP / UAC / WTS / 命名管道 / 互斥量 |
| `*_other.go` | `!windows` | stub，便于 Linux `go test` |

## 许可证与免责

按原样提供，无任何明示或暗示担保。使用即表示你理解禁用/启用设备的风险。
