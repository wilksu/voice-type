# voice-type

本地中英双语（含中英混输）系统级语音输入工具，专为 **Linux Wayland (KDE Plasma / Bazzite)** 设计，提供接近豆包输入法的“边说边预览 + 长句 AI 纠错润色”体验。

## 目录结构

```text
voice-type/
├── go/                  # Go 主程序源码（含内嵌 Web 设置页与 HUD）
├── scripts/package.sh   # 一键测试并打绿色发布包
├── dist/                # 编译产物（voice-type/ 与 voice-type-linux-amd64.tar.gz）
└── _legacy_python/      # 早期 Python 原型归档
```

## 系统依赖

- **必需（Linux Wayland 桌面环境）**：
  - `pw-record`（PipeWire 录音）
  - `wl-copy`（`wl-clipboard` 剪贴板写入）
  - `ydotool` + `ydotoold`（模拟 `Ctrl+V` 粘贴）
  - `/dev/input/event*` 读取权限（一般需将用户加入 `input` 组）及 `/dev/uinput` 写权限
- **可选增强**：
  - `python3` + `PyGObject (GTK 4)`：用于底部半透明悬浮胶囊 HUD；若目标环境缺少 GTK4，程序会自动平滑降级为提示音/通知，主功能不受影响。

## 从打包产物全新安装

```bash
# 1. 解压到用户目录
mkdir -p ~/.local/opt
tar -xzf dist/voice-type-linux-amd64.tar.gz -C ~/.local/opt/

# 2. 检查系统依赖与权限（确保在 input 组且有 pw-record / wl-copy / ydotool）
~/.local/opt/voice-type/voice-type doctor

# 3. 下载 SenseVoice-Small INT8 语音模型（默认 ModelScope 国内源，约 230MB）
~/.local/opt/voice-type/voice-type download-model

# 4. 注册 KWin 胶囊规则 + 安装并启动 systemd 用户级开机自启服务
~/.local/opt/voice-type/voice-type service install
```

## 日常管理命令

```bash
~/.local/opt/voice-type/voice-type service status    # 查看运行状态
~/.local/opt/voice-type/voice-type service logs      # 查看实时日志 (journald)
~/.local/opt/voice-type/voice-type service restart   # 重启服务
~/.local/opt/voice-type/voice-type service stop      # 停止服务
~/.local/opt/voice-type/voice-type service uninstall # 卸载自启服务
```

## 源码重新打包

```bash
bash scripts/package.sh
```
