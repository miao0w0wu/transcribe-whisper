# 降噪与识别准确性增强 —— 团队共享契约

本文件由 Lead 维护，是并行开发的**唯一契约来源**。所有成员实现时字段名、
默认值、取值必须与本文件一致，不得自行改名。

参考实现：`D:\project\go\WhisperJAV`（Python）
- `whisperjav/modules/speech_enhancement/backends/ffmpeg_dsp.py` —— FFmpeg DSP 降噪滤镜链
- `whisperjav/data/hallucination_filters/regexp_v09.json` —— 幻觉字幕过滤规则

---

## 一、Bug：配置目录被误删

### 根因

`main.go` 调用 `NewApp()`，而 `NewApp()` 是**给测试用的**：它把 `dataDir` 设为
系统临时目录并置 `tempDataDir = true`。随后 `startup()` 把 `dataDir` 改写成真实
数据目录（`<exe目录>/transcribe-whisper-data`），**但没有重置 `tempDataDir`**。
于是 `shutdown()` 里 `isTemp && tempDir != ""` 成立，直接
`os.RemoveAll(真实数据目录)` —— 整个配置文件夹连同数据库、模型、FFmpeg 全被删除。

### 修复要求（三层防御，全部要做）

1. **构造分离**：新增 `newApp()`（生产构造，不建临时目录、`tempDataDir=false`），
   `NewApp()` 改为调用 `newApp()` 后再设置临时目录。`main.go` 改调 `newApp()`。
2. **startup 明确复位**：`startup()` 拿到真实数据目录后必须 `a.tempDataDir = false`。
3. **删除前校验**：`shutdown()` 删除前必须确认该路径**确实位于系统临时目录之下**
   （`os.TempDir()` 前缀比较，Windows 下大小写不敏感）。不在临时目录下一律不删。

新增测试：构造一个"看起来是真实目录"的场景，断言 `shutdown()` 后目录仍存在且
数据库文件仍在；同时断言真正的测试临时目录仍会被清理（不破坏原有测试语义）。

---

## 二、降噪（消除背景杂音）

### 2.1 设置字段（`repository.SettingsRecord`）

| 字段名 | 类型 | 默认值 | 取值 |
| --- | --- | --- | --- |
| `NoiseReduction` | `string` | `"off"` | `off` / `light` / `standard` / `strong` / `custom` |
| `NoiseReductionFilter` | `string` | `""` | 仅 `custom` 时生效，原始 FFmpeg `-af` 滤镜链 |
| `NoiseReductionSampleRate` | `int` | `0` | 0 表示沿用 16000；可选 16000/32000/44100/48000 |

数据库列（`settings` 表，均为 `ALTER TABLE ... ADD COLUMN` 兼容旧库）：

```
noise_reduction            TEXT    NOT NULL DEFAULT 'off'
noise_reduction_filter     TEXT    NOT NULL DEFAULT ''
noise_reduction_sample_rate INTEGER NOT NULL DEFAULT 0
```

### 2.2 降噪预设滤镜链

`standard` 与 `strong` 的滤镜若本机 FFmpeg 不支持，必须**自动剔除并降级**，
不能整条失败。返回实际生效的滤镜链供日志与界面展示。

```go
// 预设 -> 有序滤镜链（按顺序串联，逗号拼接后作为 -af 参数）
off      -> nil
light    -> ["highpass=f=80", "afftdn=nf=-30", "loudnorm=I=-16:TP=-1.5:LRA=11"]
standard -> ["highpass=f=80", "afftdn=nf=-25:nr=10",
             "acompressor=threshold=-20dB:ratio=4:attack=5:release=50",
             "loudnorm=I=-16:TP=-1.5:LRA=11"]
strong   -> ["highpass=f=90", "afftdn=nf=-20:nr=15",
             "acompressor=threshold=-18dB:ratio=6:attack=5:release=80",
             "deesser=i=0.4:m=0.5:f=0.5:s=o",
             "loudnorm=I=-16:TP=-1.5:LRA=11"]
```

每个滤镜归属的能力位：
- `highpass` → 恒可用（FFmpeg 2.x 起）
- `afftdn` → `Capabilities.SupportsDenoise`（FFmpeg 4.1+）
- `acompressor` → `Capabilities.SupportsCompressor`（FFmpeg 3.1+）
- `deesser` → `Capabilities.SupportsDeesser`（FFmpeg 4.2+）
- `loudnorm` → `Capabilities.SupportsLoudnorm`（FFmpeg 3.1+）

### 2.3 media 包契约（`internal/media/types.go`，由 Lead 写）

```go
// EnhanceRequest 是音频增强（降噪）参数。
type EnhanceRequest struct {
    InputPath  string
    OutputPath string
    // Preset 是预设名：off/light/standard/strong/custom。空值按 off 处理。
    Preset string
    // FilterChain 是自定义滤镜链，仅 Preset == "custom" 时生效。
    FilterChain string
    // SampleRate / Channels 为输出参数；SampleRate<=0 用 16000，Channels<=0 用 1。
    SampleRate int
    Channels   int
}

// EnhanceResult 汇报实际生效的滤镜。
type EnhanceResult struct {
    Applied []string // 实际生效的滤镜（已剔除不支持项）
    Skipped []string // 因能力缺失被剔除的滤镜
    Filter  string   // 最终 -af 串；为空表示未做任何处理
}

// Enhancer 是可对音频做降噪增强的实现。
type Enhancer interface {
    Enhance(ctx context.Context, req EnhanceRequest) (EnhanceResult, error)
}
```

`Capabilities` 追加字段（`json` 名与 Go 名一致，供前端诊断用）：

```go
SupportsDenoise    bool `json:"supportsDenoise"`
SupportsDeesser    bool `json:"supportsDeesser"`
SupportsLoudnorm   bool `json:"supportsLoudnorm"`
SupportsCompressor bool `json:"supportsCompressor"`
```

### 2.4 语义要求

- `Preset == "off"` 或滤镜链为空：**不执行 FFmpeg**，直接返回零值结果、`nil` 错误。
  调用方据 `Filter == ""` 判断是否需要换文件。
- 输出必须是 `-ar <SampleRate> -ac <Channels> -acodec pcm_s16le` 的 WAV。
- 所有能力位都不可用导致链为空时，返回 `EnhanceResult{Filter: ""}` 与 `nil` 错误
  （等价于降级为不处理），**不要报错**。
- `custom` 链不做能力剔除，直接透传；但空串按 `off` 处理。
- 错误信息一律中文，并带上 FFmpeg 的错误摘要。

---

## 三、识别准确性

### 3.1 设置字段（`repository.SettingsRecord`）

| 字段名 | 类型 | 默认值 | 取值 / 含义 |
| --- | --- | --- | --- |
| `BeamSize` | `int` | `5` | 1..10，越大越准越慢 |
| `VadFilter` | `bool` | `true` | 是否启用语音活动检测 |
| `VadThreshold` | `float64` | `0.5` | 0.1..0.9，VAD 灵敏度 |
| `VadMinSilenceMs` | `int` | `500` | 100..2000，最短静音间隔 |
| `VadPaddingMs` | `int` | `400` | 0..2000，语音前后留白 |
| `HallucinationFilter` | `string` | `"medium"` | `off` / `low` / `medium` / `high` |
| `ConditionOnPreviousText` | `bool` | `false` | 关掉可显著减少重复幻觉 |
| `NoSpeechThreshold` | `float64` | `0.6` | 0..1，越高越容易判为无语音 |
| `CompressionRatioThreshold` | `float64` | `2.4` | 1.0..5.0，越高越宽松 |

数据库列：

```
beam_size                   INTEGER NOT NULL DEFAULT 5
vad_filter                  INTEGER NOT NULL DEFAULT 1
vad_threshold               REAL    NOT NULL DEFAULT 0.5
vad_min_silence_ms          INTEGER NOT NULL DEFAULT 500
vad_padding_ms              INTEGER NOT NULL DEFAULT 400
hallucination_filter        TEXT    NOT NULL DEFAULT 'medium'
condition_on_previous_text  INTEGER NOT NULL DEFAULT 0
no_speech_threshold         REAL    NOT NULL DEFAULT 0.6
compression_ratio_threshold REAL    NOT NULL DEFAULT 2.4
```

### 3.2 asr.Request 新增字段（`internal/asr/types.go`，由 Lead 写）

```go
BeamSize                 int      // 已有，语义不变
VadFilter                bool     // 已有，语义不变
VadThreshold             float64
VadMinSilenceMs          int
VadPaddingMs             int
ConditionOnPreviousText  bool
NoSpeechThreshold        float64
CompressionRatioThreshold float64
```

### 3.3 Python worker 入参（`transcribe_worker.py`，由 Lead 写）

payload 新增 JSON 字段（下划线命名）：

```
vad_threshold, vad_min_silence_ms, vad_padding_ms,
condition_on_previous_text, no_speech_threshold, compression_ratio_threshold
```

`vad_filter` 为真时以 `vad_parameters={"threshold":..., "min_silence_duration_ms":...,
"speech_pad_ms":...}` 传给 `model.transcribe`。

### 3.4 幻觉字幕过滤（新包 `internal/subtitleclean`，由 Lead 写）

```go
type Level string

const (
    LevelOff    Level = "off"
    LevelLow    Level = "low"
    LevelMedium Level = "medium"
    LevelHigh   Level = "high"
)

type Options struct{ Level Level }

type Result struct {
    Segments []asr.Segment // 清洗后的字幕
    Dropped  int           // 被丢弃的条数
}

func Clean(segments []asr.Segment, opts Options) Result
```

规则（`low` ⊂ `medium` ⊂ `high`）：
- 全部等级：去 emoji / 装饰符号、去首尾空白、去纯标点条目。
- `medium` 起：去括号内音效描述 `（…）` `(…)` `【…】` `[…]`；压缩连续重复条目。
- `high` 起：去已知幻觉短语（订阅/点赞/字幕组水印/感谢观看等中英日常用语）；
  去长度小于 2 个可见字符的条目。

`Dropped` 只统计整条被丢弃的数量，不统计被裁剪文本的条目。

---

## 四、前端契约

### 4.1 `SettingsSnapshot` / `SettingsPatch` 追加字段

```
noiseReduction: string
noiseReductionFilter: string
noiseReductionSampleRate: number
beamSize: number
vadFilter: boolean
vadThreshold: number
vadMinSilenceMs: number
vadPaddingMs: number
hallucinationFilter: string
conditionOnPreviousText: boolean
noSpeechThreshold: number
compressionRatioThreshold: number
```

`SettingsPatch` 同名字段全部可选（`?`）。

### 4.2 常量（`frontend/src/lib/types.ts`）

```ts
NOISE_REDUCTION_PRESETS = [
  { value: 'off',      label: '关闭降噪',       hint: '保持原始音频' },
  { value: 'light',    label: '轻度',           hint: '去低频轰鸣与底噪，几乎不改变音色' },
  { value: 'standard', label: '标准（推荐）',    hint: '降噪 + 动态压缩 + 响度归一化' },
  { value: 'strong',   label: '强力',           hint: '噪声环境适用，可能略微损失齿音细节' },
  { value: 'custom',   label: '自定义滤镜链',    hint: '直接填写 FFmpeg -af 滤镜链' },
]

HALLUCINATION_FILTERS = [
  { value: 'off',    label: '关闭',   hint: '保留全部识别结果' },
  { value: 'low',    label: '轻度',   hint: '仅去除表情符号与纯标点' },
  { value: 'medium', label: '标准（推荐）', hint: '另去除音效描述与重复条目' },
  { value: 'high',   label: '严格',   hint: '另去除常见幻觉短语与超短条目' },
]
```

### 4.3 UI 要求

设置页新增两个区块：**「音频降噪」** 与 **「识别准确性」**。
- 每个字段带中文标签与一行说明，与现有 `FormField` / `settings-section` 风格一致。
- `noiseReduction === 'custom'` 时才显示自定义滤镜链输入框。
- `vadFilter` 关闭时，VAD 三个子项置灰（`disabled`）。
- 数值项用 `number` 输入，设置合理 `min`/`max`/`step`。
- 不改动现有字段的行为与布局。

---

## 五、写作用域（避免冲突）

| 成员 | 独占文件 |
| --- | --- |
| `data-dir-fixer` | `app.go`、`appdirs.go`、`main.go`、新增 `*_test.go` |
| `media-enhancer` | `internal/media/enhance.go`、`internal/media/enhance_test.go`、`internal/media/probe.go` |
| `settings-store` | `internal/repository/types.go`、`internal/repository/settings.go`、`internal/repository/settings_enhance_test.go` |
| `frontend-ui` | `frontend/src/lib/types.ts`、`frontend/src/lib/api.ts`、`frontend/src/views/SettingsView.tsx`、`frontend/src/App.css` |
| Lead | `internal/media/types.go`、`internal/asr/**`、`internal/service/**`、`internal/subtitleclean/**`、`api.go`、`types.go`、`app.go` 装配部分（等 `data-dir-fixer` 完成后） |

**禁止越界修改他人文件。** 需要别人文件配合时，用 `send_message` 通知 Lead。

---

## 六、最终交付状态（Lead 验收记录）

全部任务已完成并通过验收。

### 配置目录误删 bug —— 已修复

三层防御全部落地，并用真实构建产物做了端到端验证：

1. `newApp()` 生产构造 + `NewApp()` 测试构造分离，`main.go` 调用 `newApp()`。
2. `startup()` 在同锁内显式 `tempDataDir = false`。
3. `shutdown()` 经 `shouldRemoveTempDataDir()` 校验，只删系统临时目录下的路径。

验证方式：`wails build -clean` 产出真实 exe → 放到非临时目录运行 → 正常关闭窗口 →
配置目录（含 `models/` 与 `transcribe-whisper.db`）完整保留 → 再次启动再关闭仍然保留。
另做了反向验证：临时移除第三层防御后，`TestShutdownKeepsRealDataDirWhenFlagStuck`
立即失败，证明该测试确实能捕获此 bug。

### 降噪与识别准确性 —— 已实现

| 层 | 文件 |
| --- | --- |
| 设置字段与迁移 | `internal/repository/types.go`、`settings.go` |
| FFmpeg 降噪实现 | `internal/media/enhance.go`、`enhance_types.go`、`probe.go` |
| 编排管线 | `internal/service/runner.go`、`deps.go`、`queue.go` |
| 幻觉字幕过滤 | `internal/subtitleclean/clean.go` |
| 识别参数下发 | `internal/asr/types.go`、`worker.go`、`assets/transcribe_worker.py` |
| API 与装配 | `api.go`、`types.go`、`app.go` |
| 前端设置页 | `frontend/src/lib/types.ts`、`views/SettingsView.tsx` |

真实 FFmpeg（7.1.1）验证：`afftdn` / `deesser` / `loudnorm` / `acompressor` / `highpass`
全部可用，四个预设（light/standard/strong/custom）滤镜链均可执行，输出有效 WAV；
降噪后音量从 -27.00 dB 归一化到 -15.10 dB（符合 -16 LUFS 目标）。

### 一处设计取舍

设置层把数值 `0` 视为「未设置」并原样保存（保持 `TestSettingsRoundTrip` 的
`reflect.DeepEqual` 语义），默认值由消费方回落：

- `internal/service/runner.go`：`resolveBeamSize` / `resolveFloat` / `resolveInt`
- `api.go`：`resolveBeamSize` / `resolveDefaultFloat` / `resolveDefaultInt`

两处回落的默认值与契约表格一致（5 / 0.5 / 500 / 400 / 0.6 / 2.4）。

### 验证命令

```bash
go build ./... && go vet ./... && go test ./... -count=1
cd frontend && npm run build
wails build -clean
```

