# transcribe-whisper

基于本地 AI 的音视频语音识别、字幕生成、翻译与导出桌面应用。桌面宿主使用 Go + Wails，界面使用 React + TypeScript + Vite；音视频和字幕默认不离开本机。

## 当前状态

当前版本完成了第一阶段的桌面 UI 垂直切片，并接入了最小任务领域 API：

- 工作台：任务进度、字幕表格、音频播放条、任务详情和校验状态。
- 新建任务：音视频导入、识别参数、输出格式、任务预估和隐私提示。
- 翻译设置：Ollama / OpenAI-compatible Provider、语言风格、内容模式和高级参数。
- 模型管理：Hugging Face / 国内镜像、已安装模型、下载状态和 GPU 检测。
- Go 任务 API：读取任务面板、创建任务、校验源文件、启动识别状态。
- SQLite 任务持久化：任务创建、状态更新和应用重启恢复均写入本地数据库。
- SQLite 字幕持久化：字幕列表、编辑更新和 SRT 导出均使用任务对应的分段数据。

界面采用设计稿中的深色侧栏、浅色画布、蓝紫主色、卡片化信息层级。任务创建和状态读取已通过 Wails Binding 接入；字幕、翻译与模型数据仍待接入持久化服务。

## 产品链路

```text
导入音频/视频
    ↓
FFmpeg 提取音频
    ↓
Whisper / FunASR 语音识别
    ↓
生成带时间轴的字幕
    ↓
llama-server / Ollama / 云端 API 翻译
    ↓
字幕校对与编辑
    ↓
导出字幕或压制视频
```

优先本地运行。云端 Provider 必须由用户主动配置，并在发送字幕前显示隐私提示。所有本地服务默认只绑定 `127.0.0.1`。

## 技术栈与目录

| 层级 | 选型 |
|---|---|
| 桌面宿主 | Go + Wails v2 |
| 前端 | React + TypeScript + Vite |
| 数据库 | SQLite（`modernc.org/sqlite`，纯 Go） |
| 音视频 | FFmpeg |
| ASR | faster-whisper / FunASR |
| 翻译 | llama-server、Ollama、OpenAI-compatible API |

```text
transcribe-whisper/
├─ app.go
├─ main.go
├─ go.mod
├─ frontend/
│  └─ src/
│     ├─ App.tsx       # 工作台、任务、设置和模型管理视图
│     ├─ App.css       # 设计稿视觉规范
│     └─ style.css
├─ build/
├─ models/              # 用户自备模型，不随安装包分发
└─ README.md
```

当前任务数据库默认位于 Windows 用户配置目录下的 `transcribe-whisper/transcribe-whisper.db`。后续服务层按领域拆分为 `ProjectService`、`JobService`、`SubtitleService`、`TranslationService`、`ModelService`、`SettingsService` 和 `ExportService`。前端只调用这些领域接口，不直接操作文件系统、数据库或系统进程。

## 开发与构建

安装依赖并启动前端：

```powershell
cd frontend
npm install
npm run dev
```

启动 Wails 开发模式：

```powershell
wails dev
```

构建 Windows 桌面程序：

```powershell
wails build
```

产物位于 `build/bin/`。本地服务进程、模型路径、Provider 和 API Key 均应通过设置配置，不应写死在代码中。

## 后续实施顺序

1. 接入 SQLite 领域模型与 Wails Binding。
2. 接入 FFmpeg 音频提取和 Python faster-whisper JSON Lines 协议。
3. 实现任务队列、暂停/恢复/取消、断点恢复及 SRT/VTT/TXT/JSON 导出。
4. 接入统一 `TranslationProvider`、批量 JSON 校验、重试、术语表和缓存。
5. 增加模型下载、缓存、取消、进度事件和模型设置快照。
