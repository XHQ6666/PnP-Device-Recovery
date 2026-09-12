# PnP Device Recovery Utility

Windows 即插即用（PnP）设备自动恢复工具。通过 **事件驱动** 监听设备状态变化；当受监控设备出现问题（`DN_HAS_PROBLEM` 且/或 `ProblemCode != 0`）时，自动执行 **禁用 → 等待 → 启用 → 再检查** 的恢复流程。

> **安全声明 / Disclaimer**  
> 本工具会禁用并重新启用硬件设备节点，可能导致短暂断网、黑屏或系统不稳定。请仅在了解风险的环境中使用。作者不对数据丢失或硬件损坏负责。请以管理员身份运行。故障排查时可使用系统自带的 `pnputil`（仅供人工诊断，**本程序从不调用** pnputil / PowerShell / WMIC）。

---

## 目录结构

- 根目录：`README.md`、`config.json`（示例配置）
- `src/`：全部 Go 源码（`go.mod`、`*.go`、测试）

## 工作原理

```
Windows PnP
     │
     ▼
PnP Event
     │
     ▼
Device Enumeration
     │
     ▼
FriendlyName Match
     │
     ▼
Status / ProblemCode
     │
 ┌───┴────┐
正常     异常
 │         │
结束     Recovery
           │
       Disable
           │
        Enable
           │
      Re-check
```

监控为 **事件驱动**（优先 `CM_Register_Notification`，回退 `RegisterDeviceNotification` + 隐藏窗口 `WM_DEVICECHANGE`），**不以固定轮询作为主监控手段**。Disable/Enable 从不在 PnP 回调线程内执行，而是入队由 Recovery worker 处理。

---

## 功能特性

- **事件驱动 PnP**：优先 `CM_Register_Notification`；不可用时回退到 `RegisterDeviceNotification` + 隐藏消息窗口（`WM_DEVICECHANGE`）。**不以轮询作为主监控手段**。
- **PnP 回调内绝不 Disable/Enable**：事件入队，由 worker 处理。
- **按设备会话恢复**：独立锁与尝试计数；成功清零；耗尽 `max_retries` 仅结束该会话，**不** `os.Exit`，**不**停止监听。
- **同设备事件合并（coalesce）**：同一 Instance ID 不会并发恢复；多设备可并发。
- **FriendlyName 精确匹配**；`regex:` 前缀使用 Go RE2。
- **设备状态**：仅通过 SetupAPI / CfgMgr32（`golang.org/x/sys/windows` + LazyDLL）。健康条件：无 `DN_HAS_PROBLEM` 且 `ProblemCode == 0`。
- **UAC**：未提升时 `ShellExecuteEx` + `runas`；用户拒绝则记日志并干净退出。
- **日志**：UTF-8；超过 **10×1024×1024** 字节后截断覆盖（按字节，非按行）。
- **优雅关闭**：SIGINT/SIGTERM → 停止新恢复 → 注销 PnP → 等待 worker → 关闭日志。
- **交叉编译**：`GOOS=windows` 真实实现；Linux 下 stub + `cd src && go test ./...` 覆盖配置/匹配/日志/恢复状态机。

---

## 配置（config.json）

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
| `devices[].friendly_name` | 精确匹配设备“友好名称”；或以 `regex:` 开头使用 RE2 |
| `devices[].max_retries` | 单次恢复会话最大尝试次数（必须 > 0） |
| `retry_delay` | 禁用/启用后的等待秒数（必须 >= 0） |
| `log_file` | 日志路径（不可为空） |

### 校验错误（启动时失败，运行中不 panic）

- 配置文件缺失 / JSON 非法  
- `devices` 为空  
- `friendly_name` 为空  
- `regex:` 后为空或非法正则  
- `max_retries <= 0`  
- `retry_delay < 0`  
- `log_file` 为空  

### 匹配规则

1. **精确匹配**：`friendly_name` 与设备 FriendlyName **完全相等**（区分大小写）。  
2. **正则**：`friendly_name` 以 `regex:` 为前缀时，对剩余部分编译 Go `regexp`（RE2），对 FriendlyName 做 `MatchString`。  

启动时若配置的设备不存在：记录警告并继续监听。

---

## 恢复语义

```
检测到不健康 → 开启/复用会话（同设备 coalesce）
  attempt++
  if attempt > max_retries → 结束会话（计数归零便于下次会话从 1 开始）；监听继续
  Disable → sleep(retry_delay) → Enable → sleep(retry_delay) → 复查
  成功 → 计数清零，结束会话
```

- 关闭时：`context` 取消，停止新会话，等待进行中的 worker。  
- 日志记录：时间、名称、Instance ID、DEVINST、Status、ProblemCode、尝试次数、禁用/启用结果、恢复后状态、最终结果、PnP 事件。

---

## 日志

- 编码：UTF-8  
- 轮转：文件大小超过 **10 MiB（10×1024×1024）** 时 **截断覆盖** 旧内容（不是按行数）。  
- 默认文件名：`PnP-Device-Recovery.log`（运行时创建；仓库中可有空占位文件）。

---

## UAC

程序检查当前进程是否已提升。若否，通过 `ShellExecuteEx` 以 `runas` 动词重新启动自身。若用户在 UAC 对话框中拒绝，写入错误日志并干净退出（退出码非 0）。

---

## 编译与运行

### Windows 目标（在 Linux 上交叉编译）

```bash
cd /workspace/PnP-Device-Recovery
cd src
GOOS=windows GOARCH=amd64 CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o ../PnP-Device-Recovery.exe .
```

验证：

```bash
file PnP-Device-Recovery.exe
sha256sum PnP-Device-Recovery.exe
```

### Linux 测试（stub + 纯逻辑）

```bash
cd src && go test ./...
```

### 在 Windows 上运行

1. 将 `PnP-Device-Recovery.exe` 与 `config.json` 放在同一目录（或传入配置路径参数）。  
2. 双击或在终端运行；若未管理员，会弹出 UAC。  
3. 查看 `PnP-Device-Recovery.log`。

```text
PnP-Device-Recovery.exe
PnP-Device-Recovery.exe path\to\config.json
```

---

## 故障排查（人工）

本程序 **不** 调用下列工具；仅供你在设备管理器之外人工诊断：

```bat
pnputil /enum-devices /problem
pnputil /restart-device "USB\VID_...."
```

常见问题：

| 现象 | 可能原因 |
|------|----------|
| 启动即退出 | 配置校验失败；查看 stderr |
| UAC 后仍退出 | 用户拒绝提升 |
| 找不到设备 | FriendlyName 与设备管理器不一致；改用 `regex:` |
| 恢复无效 | 驱动/硬件故障；检查 ProblemCode |
| 日志突然变短 | 已超过 10MB 并截断轮转 |

---

## 模块路径

`github.com/XHQ6666/PnP-Device-Recovery`

## 构建标签

| 文件 | 标签 | 说明 |
|------|------|------|
| `*_windows.go` | `windows` | SetupAPI / CfgMgr32 / PnP / UAC |
| `*_other.go` | `!windows` | stub，便于 Linux `go test` |

## 许可证与免责

按原样提供，无任何明示或暗示担保。使用即表示你理解禁用/启用设备的风险。
