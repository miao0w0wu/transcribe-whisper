# transcribe-whisper

基于本地 AI 的音视频语音识别、字幕生成、翻译与导出桌面应用。桌面宿主使用 Go + Wails，界面使用 React + TypeScript + Vite；音视频和字幕默认不离开本机。

## 当前状态

项目已完成需求文档中的阶段一至阶段五，形成一条可用的完整链路：

```text
导入音视频 → FFmpeg 提取 16kHz wav → faster-whisper 识别
→ Ollama / OpenAI-compatible 翻译（批处理 + 重试 + 术语表 + 缓存）
→ 工作台校对 → SRT / VTT / TXT / JSON / 双语 SRT 导出
```

- 工作台：任务进度、字幕表格（搜索、筛选、行内编辑）、状态徽章，操作按状态启用（暂停 / 恢复 / 取消 / 重试 / 重新翻译 / 翻译待译条目）；导出区下方可将字幕压制进视频另存为 MP4。
- 任务队列：全部任务的进度与生命周期管理，点击进入工作台；应用启动时自动把上次异常退出残留的任务标记为失败。
- 新建任务：系统文件对话框（`PickFile`）、已安装模型下拉、语言 / 设备 / 精度、是否翻译与 Provider 选择、输出格式。
- 翻译设置：Ollama（本地，默认）与 OpenAI-compatible（云端，需隐私确认）、**自动识别本机 Ollama 模型并手动选择**、翻译语气（含**露骨**选项，面向成人内容）、术语表（`源术语=译法`，每行一条）、API Key（只显示是否已配置）、Hugging Face 访问令牌（仅受限仓库需要）、连接测试。
- 模型管理：内置 Systran/faster-whisper 五档模型（tiny/base/small/medium/large-v3）**与 cohere-transcribe-03-2026**；hf-mirror 与 Hugging Face 多镜像自动降级、断点续传、进度事件、取消、删除保护（正在被任务使用的模型不可删除）；**支持自选本地模型目录**（自动识别 CTranslate2 / transformers 格式并校验关键文件）与**按仓库名 / 链接添加可下载模型**；环境探测（FFmpeg / Python / faster-whisper / transformers / GPU）。
- 双识别引擎：`faster-whisper`（CTranslate2 格式）与 `transformers`（safetensors 格式，如 cohere）；任务按模型格式自动路由，不看设置里的引擎名。
- 后端架构：`internal/` 分层（media / asr / translation / modelmanager / repository / exporter / service），Binding 层只做参数校验与结果映射，任务阶段与设置均持久化到 SQLite。
- 前端架构：`src/views/` 按视图拆分，`src/lib/` 封装 API 与事件订阅；纯浏览器打开时进入只读演示模式，不会崩溃。

## 本机验证记录

以下验证在开发机（Windows 11，RTX 4070 SUPER）实测通过：

- `go test ./...` 全部通过（含 service 状态机、暂停 / 取消 / 重试、翻译批处理的 100+ 用例；`-race` 通过）。
- 真实链路端到端：TTS 生成语音 → FFmpeg 提取 → faster-whisper tiny 识别出 3 条时间轴准确的中英字幕 → Ollama qwen2.5:7b 翻译 3/3 条成功 → 五种格式导出全部非空。
- 术语表实测生效：`pipeline` 按术语表译为“流水线”，`timestamps` 译为“时间戳”。
- 字幕压制实测出片：自动发现带 libass 的 `ffmpeg 8.1.2-full`，直接调用与穿过 service 层两条路径均产出 6.00 秒 MP4（67290 / 68430 字节）。
- **cohere-transcribe-03-2026 实测可用**：从 hf-mirror 完整下载 4.13GB 权重后，`transformers` 引擎成功加载并识别出与原文一致的英文字幕；70 秒长音频按 30 秒切块产出 3 段带真实时间轴的结果，静音块被正确跳过（未加静音检测时模型会在静音上幻觉出整段葡萄牙语/西班牙语文本，已修复）。
- 本机 Ollama 模型自动识别：实测读出 3 个已安装模型（含参数量与量化等级）。
- `wails build` 打包成功（`build/bin/transcribe-whisper.exe`）。
- 前端调用的 29 个 Wails Binding 与自动生成绑定一一对齐，无缺失方法。

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
| 音视频 | FFmpeg（探测 / 提取 / 压制能力检测；自动优先选带 `subtitles` 滤镜的构建） |
| ASR | faster-whisper（JSON Lines 子进程协议）；FunASR 协议已实现 |
| 翻译 | Ollama、OpenAI-compatible API（`/chat/completions`） |

```text
transcribe-whisper/
├─ app.go               # App 生命周期、依赖装配
├─ api.go               # Wails Binding（任务 / 模型 / 设置 / 环境）
├─ types.go             # 对前端的公共类型
├─ glossary.go          # 术语表解析与回显
├─ main.go              # Wails 入口
├─ internal/
│  ├─ media/            # FFmpeg 探测、音频提取、字幕压制
│  ├─ asr/              # faster-whisper / FunASR JSON Lines 引擎
│  ├─ translation/      # Provider 工厂、批处理流水线、缓存、术语表
│  ├─ modelmanager/     # 模型清单、多镜像下载、续传、删除保护
│  ├─ repository/       # SQLite 数据访问与 schema 迁移
│  ├─ exporter/         # SRT / VTT / TXT / JSON / 双语 SRT
│  └─ service/          # 任务队列、阶段推进、暂停 / 恢复 / 取消 / 重试
├─ frontend/
│  ├─ src/lib/          # API 封装、类型、事件订阅、格式化工具
│  ├─ src/views/        # 工作台 / 新建任务 / 任务队列 / 设置 / 模型管理
│  └─ src/App.css       # 设计稿视觉规范
└─ build/
```

任务数据库、模型缓存与工作目录默认位于 Windows 用户配置目录下的 `transcribe-whisper/`，均可在设置页调整。测试（`NewApp()`）使用系统临时目录，不会读写真实配置。

## 开发与构建

```powershell
cd frontend
npm install
npm run build       # tsc + vite build，零错误为验收标准
```

```powershell
go test ./...       # 全部包；真实链路测试默认跳过
wails dev           # 开发模式
wails build         # 产物在 build/bin/
```

### 可选的真实链路测试（需要外部资源，默认跳过）

```powershell
# 识别链路（需 ffmpeg 与装好 faster-whisper 的 Python）
$env:TRANSCRIBE_REAL_E2E='1'
$env:TRANSCRIBE_E2E_AUDIO='D:\path\to\speech.wav'
$env:TRANSCRIBE_E2E_PYTHON='D:\AI\WhisperJAV\python.exe'
$env:TRANSCRIBE_E2E_MODEL_DIR='D:\models\tiny'   # 复用已有模型，避免联网
go test -run TestRealPipeline -v ./

# 识别 + Ollama 翻译 + 五格式导出
$env:TRANSCRIBE_E2E_TRANSLATE='1'
$env:TRANSCRIBE_E2E_OLLAMA_MODEL='qwen2.5:7b'
go test -run TestRealFullPipeline -v ./

# 字幕压制（自动发现带 libass 的 FFmpeg，无需环境变量）
go test -run TestRealBurnSubtitle -v ./

# cohere-transcribe（transformers 引擎，需先下载模型与装好 torch/transformers）
$env:TRANSCRIBE_E2E_COHERE='1'
$env:TRANSCRIBE_E2E_AUDIO='D:\path\to\speech-16k.wav'
$env:TRANSCRIBE_E2E_PYTHON='D:\AI\WhisperJAV\python.exe'
$env:TRANSCRIBE_E2E_COHERE_DIR='D:\models\cohere-transcribe'   # 可选，默认 tmp-cohere-models
go test -run TestRealCohereTranscribe -v ./

# 模型下载冒烟（联网，走 hf-mirror）
$env:MODELMANAGER_NET_TEST='1'
go test -run TestNetworkSmoke -v ./internal/modelmanager/
```

## transformers 引擎（cohere 等模型）额外依赖

```powershell
pip install torch "transformers>=5.4.0" soundfile librosa sentencepiece protobuf
```

`transformers` 引擎与 faster-whisper 是两套独立依赖，互不影响：只用 Whisper 时不需要装 torch。

## 后续实施顺序（建议）

1. FunASR 引擎已在后端实现（脚本、协议、测试），接入模型管理并在 UI 暴露。
2. 长音频分段与断点续跑（当前单进程识别，依赖 faster-whisper 自身的分段）。
3. 翻译设置里补充术语表导入 / 导出与按任务覆盖。
4. 压制参数（字体、位置、描边）尚未做成设置项，目前字号可在工作台调整，边距固定 40。
5. CI 与 Windows 安装包验证。
