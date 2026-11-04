# transcribe-whisper 项目交接文档

## 1. 项目概况

这是一个基于 Go + Wails + React + TypeScript 的本地 AI 音视频字幕工作台。

产品目标：

```text
导入音视频 → FFmpeg 提取音频 → Whisper/FunASR 识别
→ 字幕翻译 → 字幕校对 → SRT/VTT/TXT/JSON 导出
```

设计依据：

- 项目需求：[README.md](./README.md)
- 视觉设计稿：`transcribe-whisper 字幕工作台.pptx`

当前优先本地运行（默认 Ollama），云端翻译需要用户显式切换并确认隐私提示。

## 2. 当前已完成（阶段一~五）

### 架构

单文件 `app.go` 已拆分为分层结构，Binding 层（`app.go` + `api.go` + `glossary.go`）只做参数校验、领域调用与结果映射，公共类型在 `types.go`：

| 包 | 职责 |
|---|---|
| `internal/media` | FFmpeg 探测（版本 / `subtitles`、`drawtext` 滤镜能力）、ffprobe 元数据、16kHz wav 提取、字幕压制（`Burn`） |
| `ffmpeg_select.go`（根包） | FFmpeg 自动发现：显式配置 > PATH 与常见安装目录探测（优先支持压制的构建）> 启动时限时深度扫描升级，成功后写回设置 |
| `internal/asr` | 两套 JSON Lines 子进程引擎：`faster-whisper`（CTranslate2）与 `transformers`（safetensors，如 cohere）；Python 脚本内嵌（`internal/asr/assets/*.py`）并释放到缓存目录 |
| `internal/translation` | `Provider` 统一接口、Ollama（`/api/chat` + `/api/tags` 本机模型列表）与 OpenAI-compatible（`/chat/completions`）、批处理流水线（分批、指数退避重试、缺失补批、SHA-256 缓存、术语表注入与后置校验、`BatchError` 聚合） |
| `internal/modelmanager` | 六档模型清单（five Whisper + cohere，实测字节数）、hf-mirror→HuggingFace 多镜像降级、HTTP Range 断点续传（`.part`）、进度节流、取消、删除保护、自选本地模型（`local.go`）、按链接添加仓库（`repo.go`） |
| `internal/repository` | tasks / subtitle_segments / settings 三表，schema 迁移（旧库自动 `ALTER TABLE` 补列），`MarkInterruptedTasks` 启动清理 |
| `internal/exporter` | SRT / VTT / TXT / JSON / 双语 SRT 纯函数导出器 |
| `internal/service` | 任务调度：并发槽位队列、阶段推进（提取→识别→翻译）、暂停 / 恢复 / 取消 / 重试 / 重新翻译、Wails 事件推送 |

### Wails Binding（29 个，已与前端一一对齐）

| 方法 | 用途 |
|---|---|
| `GetDashboard` | 任务列表与活动任务 |
| `CreateTask(input)` | 校验源文件存在性、归一化模型 ID、隐私门禁后入库 |
| `StartTask / PauseTask / ResumeTask / CancelTask / RetryTask` | 生命周期控制 |
| `RetranslateTask` | 清空译文重跑翻译 |
| `TranslatePending` | 只补翻 pending 条目（不覆盖已校对译文） |
| `DeleteTask` | 级联删除任务与字幕 |
| `ListSubtitles / UpdateSubtitles` | 字幕读写（完整性校验保留） |
| `ExportSubtitles / SaveExport` | 五格式导出；后者弹系统保存框写盘 |
| `BurnSubtitles(taskID, input)` | 把字幕压制进源视频；`outputPath` 为空时弹系统保存框，用户取消返回空串 |
| `ListModels / DownloadModel / CancelModelDownload / RemoveModel` | 模型管理（未知模型拒绝、下载中去重；本地模型拒绝下载、移除只解除注册不动文件） |
| `AddLocalModel(input)` | 把本地目录注册为识别模型，自动识别 CTranslate2 / transformers 格式并校验关键文件 |
| `AddRepoModel(input)` | 用仓库名或 HF 链接新增可下载模型，自动读取 tree 生成文件清单 |
| `PickDirectory` | 系统目录选择框（自选本地模型用） |
| `ListOllamaModels` | 列出本机 Ollama 已安装模型（翻译模型下拉选择） |
| `OpenModelFolder` | 打开缓存目录 |
| `GetSettings / UpdateSettings` | 设置读写（API Key 与 HF Token 只返回布尔；含 `translationStyle`、`glossaryText`、`localModels`） |
| `TestTranslationProvider` | 发一条测试字幕验证连通性 |
| `GetEnvironment` | FFmpeg / Python / faster-whisper / transformers / GPU 探测 |
| `PickFile` | 系统文件选择框（新建任务用） |

### Wails 事件

- `task:progress`：`TaskProgressEvent { taskId, status, stage, progress, currentStage, message, error }`
- `model:progress`：`ModelProgressEventPayload { modelId, progress, message, done, error }`

### 任务状态机

```
created → queued → extracting → transcribing → translating → completed
              ↘ paused（可恢复）        ↘ failed（可重试）  ↘ canceled
```

- 翻译失败不丢弃识别结果：任务记为 `completed` 并把失败原因写入 `error` 字段，界面可继续「翻译待译条目」。
- 「识别完成但翻译未完成」的阶段标识为 `translated-partial`。

### SQLite 持久化

数据库默认 `AppData\Roaming\transcribe-whisper\transcribe-whisper.db`；模型缓存与任务工作目录默认在同目录的 `models\` 与 `work\`，均可在设置页调整。

- `tasks` 表含引擎 / 模型 / Provider / 输出格式 / 媒体时长 / 阶段 / 统计等完整字段；旧库打开时自动补列。
- `settings` 表单行存储，含 `translation_style`（subtitle/formal/casual/literal）与 `glossary_json`（术语表 map）。
- `subtitle_segments` 保持 ID / 序号唯一、时间轴非负等约束，识别结果分批 `UpsertSegments` 写入（为断点续跑预留）。
- 应用启动时 `MarkInterruptedTasks` 把残留的进行中任务标记为失败。

### 前端

- `src/views/`：Workspace（进度卡 + 字幕表 + 详情）、NewTask、TaskQueue、SettingsView、ModelsView，公共组件在 `views/components/`。
- `src/lib/api.ts` 封装 25 个 Binding；`lib/events.ts` 订阅两个进度事件并正确取消；`lib/runtime.ts` 在纯浏览器下降级为只读演示模式（显示中文提示，不崩溃）。
- 新建任务优先走 `PickFile`（系统对话框拿绝对路径），浏览器环境回退 file input + 提示补全路径。
- 模型未安装时下拉为空并给出「先到模型管理下载」的中文引导。

## 3. 关键文件

| 文件 | 说明 |
|---|---|
| [app.go](./app.go) | App 生命周期、依赖装配（FFmpeg/引擎/模型管理器/队列/翻译工厂）、事件发射 |
| [api.go](./api.go) | 全部 Wails Binding 与类型映射 |
| [types.go](./types.go) | 前端公共类型（唯一落点，改字段要同步 `frontend/src/lib/types.ts`） |
| [glossary.go](./glossary.go) | 术语表文本 ⇄ map（`源=译`，支持全角等号、注释行、行号报错） |
| [internal/service/queue.go](./internal/service/queue.go) | 队列与生命周期（`busy()` 以数据库终态判断，避免收尾窗口竞态） |
| [internal/service/runner.go](./internal/service/runner.go) | 三阶段执行器与进度权重（提取 2-15%、识别 15-80%、翻译 80-98%） |
| [internal/translation/prompt.go](./internal/translation/prompt.go) | 提示词与**多形态解析**（数组 / id→译文对象 / 包装对象），见下文「重要教训」 |
| [internal/asr/worker.go](./internal/asr/worker.go) | JSON Lines 协议、payload 构造（`download_root` 透传模型缓存目录） |
| [frontend/src/lib/api.ts](./frontend/src/lib/api.ts) | 前端 API 封装（演示模式降级） |

## 4. 开发和验证

```powershell
go test ./...          # 全部通过；-race 亦通过
cd frontend; npm.cmd run build   # tsc + vite，零错误
wails build            # build/bin/transcribe-whisper.exe
```

真实链路测试（默认跳过，开关见 README）：

- `TestRealPipeline`：识别链路（本机 tiny 模型 + WhisperJAV Python 实测通过）。
- `TestRealFullPipeline`：识别 + Ollama 翻译 + 五格式导出（qwen2.5:7b 实测 3/3 条翻译成功、术语表生效）。
- `TestRealBurnSubtitle`：真实调用 `media.Burn` 压制字幕（自动发现带 libass 的 FFmpeg，实测出片 68430 字节 / 6.00 秒）。
- `TestRealBurnThroughService`：穿过 `service.Queue.BurnSubtitles` 的完整压制链路（实测 67290 字节 / 6.00 秒，同时校验译文优先与原文回退）。
- `TestRealCohereTranscribe`：cohere-transcribe 的 transformers 链路（`TRANSCRIBE_E2E_COHERE=1`）。
- `ModelManager` 联网冒烟：`MODELMANAGER_NET_TEST=1`，hf-mirror 实测 76MB 下载、进度节流正常。

## 5. 重要教训（改代码前先读）

1. **Ollama `format=json` 强制输出对象**。早期实现只解析数组，导致本地模型翻译 100% 失败（实测 qwen2.5 返回 `{"1":"译文"}`）。`parseChoices` 现在支持三种形态，并且对「未闭合数组被误读成对象」做了防护（结构字段名 `id`/`text` 等出现即报错）。改提示词时不要把「允许对象形态」删掉。
2. **任务收尾存在竞态窗口**：执行器写完终态到注销上下文之间有间隙。所有「任务是否在运行」的判断用 `queue.busy()`（running 表 + 数据库终态），不要只用 running 表。
3. **faster-whisper 加载本地模型目录**需要 `config.json`、`model.bin`、`tokenizer.json`、`vocabulary.txt`（large-v3 是 `vocabulary.json`）**全部齐全**，否则报 `Cannot load the vocabulary`。所以清单里四个文件都是 Required。
4. **测试必须隔离用户目录**：`NewApp()` 会把数据目录指向系统临时目录（`shutdown` 时清理）。不要在 `NewApp()` 里回退到 `os.UserConfigDir()`。
5. **本机网络**：`huggingface.co` 不可达，`hf-mirror.com` 可达；模型下载默认镜像顺序必须是 hf-mirror 优先。
6. **FFmpeg 构建差异与本机路径**：本机在用的 `ffmpeg 7.1.1`（WhisperJAV）**没有** `subtitles` 滤镜，而 `ffmpeg 8.1.2-full`（`D:\AI\video tools\jasna-windows-0.10.0\tools\ffmpeg.exe`）有。`ffmpeg_select.go` 已实现自动发现：显式配置优先 > PATH 与常见目录探测（优先支持压制的构建）> 启动时限时深度扫描升级，成功后写回 `settings.FFmpegPath`。
7. **滤镜图转义只能单级**：`subtitles='C\:/path/x.srt'` 正确，两级（`subtitles='C\\:/...'`）实测 `exit=-22 Invalid argument`。改 `escapeFilterValue` 前先用命令行 A/B 验证，别靠推测。
8. **`NormalizeModelID` 只「宽进」不保证等于清单 ID**。`viethang/cohere-transcribe-03-2026` 会归一化成 `cohere-transcribe-03-2026`，而清单 ID 是 `cohere-transcribe`。所有查找必须走 `findInCatalog`（同时比 ID、Repo 与归一化结果），不要用归一化输出做等值判断 —— `DownloadModel` 曾因此报「未知的模型」。
9. **cohere 官方仓库是 gated（403）**。`CohereLabs/cohere-transcribe-03-2026` 的 `gated` 为 `auto`，hf-mirror 的 resolve 直接 403，镜像站无法代为放行。清单因此指向内容完全一致的公开副本 `viethang/cohere-transcribe-03-2026`（文件与字节数逐一对齐，含中文在内的 14 语言）。别把清单改回官方仓库名。
10. **cohere 的建模代码与 transformers 5.x 有一处不兼容**：仓库里的 `modeling_cohere_asr.py` 把 `_keys_to_ignore_on_load_unexpected` 写成 list，而 transformers ≥5 会做 `list | set`，抛 `TypeError: unsupported operand type(s) for |`。`transformers_worker.py` 的 `patch_transformers_compat()` 在装载前把这两个忽略键规范成 set，**别删掉这段垫片**。
11. **cohere 在静音上会幻觉**。纯静音分块会让模型凭空生成整段无关文本（实测出现过葡萄牙语与西班牙语内容）。`transformers_worker.py` 的 `block_is_silent()` 用 RMS + 窗口占比双重判定跳过静音块，**这是必需项**，去掉会产生大量毫无根据的字幕。
12. **cohere 没有内建时间戳**：它是整段进整段出。worker 按 30 秒（重叠 0.5 秒）切块逐块生成，块边界即真实时间轴，这不是编造的时间。别为了「一条字幕更完整」把切块去掉，否则整段只能产出一条覆盖全片的字幕。
13. `frontend/wailsjs/` 是 `wails build` 自动生成的；前端手工补齐过的部分已全部被生成结果覆盖并验证（见 `binding_test.go` 的 Binding 对齐测试思路）。

## 6. 当前实现限制（接手后可继续）

1. FunASR：协议、脚本、引擎实现与测试都在（`internal/asr/funasr.go`），但模型管理清单不含 FunASR 模型、UI 也未暴露（引擎下拉只有 faster-whisper）。接入方式可参照新加的 `transformers` 引擎（`internal/asr/transformers.go` + `newEngineSelector`）。
2. 字幕压制链路已打通：`media.Burn` → `service.Queue.BurnSubtitles` → `App.BurnSubtitles` → 工作台按钮。启动时自动发现带 libass 的 FFmpeg，实测出片（`TestRealBurnSubtitle`、`TestRealBurnThroughService`）。**尚未做的是压制外观设置**：字号可在界面调，边距固定 40、字体随 libass 默认（`BurnOptions` 已支持 `FontName`/`FontSize`/`MarginV`，只是没进设置页）。
3. 长音频是单次识别；`UpsertSegments`/阶段字段已为断点续跑预留，但暂停后恢复会重跑当前阶段（识别结果保留在库里，不会丢）。transformers 引擎内部虽有 30 秒切块，但没有跨进程续跑。
4. 翻译温度、批大小、上下文长度等高级参数尚未暴露到设置页（后端已有默认值：批 20 条、重试 3 次、并发 1、温度 0.3）。
5. 术语表只支持全局一份；未做按任务覆盖与导入导出。
6. `SaveExport` / `PickFile` / `PickDirectory` / `BurnSubtitles` 的另存为路径依赖 Wails 运行时，浏览器演示模式不可用（已禁用按钮并给出中文提示）。
7. 自选本地模型只保存在本机会话 + 设置表里；换机器需要重新添加（目录路径是绝对路径，不会自动搬移）。
8. 无 CI；`go telemetry` 目录在本机偶发权限提示，不影响构建结果。

## 7. 接手时建议先做的检查

```powershell
go test ./... -count=1
Set-Location frontend; npm.cmd run build; Set-Location ..
wails build
wails dev
```

重点验证：

1. 新建任务（系统对话框选文件）→ 自动开始 → 工作台进度实时推进。
2. 暂停 / 恢复 / 取消 / 重试按钮随状态正确启用；删除任务后字幕级联消失。
3. 模型管理页下载 tiny → 进度条推进 → 完成后出现在「已安装」；删除有「正在使用」保护。
4. 设置页改镜像 / 模型目录 / 术语表并保存，重启后仍在；「测试连接」给出中文结论。
5. Ollama 在线时：识别完成自动翻译；断开 Ollama 时任务完成但 error 提示翻译失败，字幕不丢。
6. 导出五种格式内容正确，`SaveExport` 落盘成功。
