// 本文件集中定义前端使用的领域类型。
// 之所以不直接复用 wailsjs 生成的 class，是为了让前端在纯浏览器（无 Wails 运行时）下
// 也能安全构造对象，同时避免依赖生成文件的内部实现细节。
// 字段名与后端 types.go 的 JSON tag 严格一致。

/** 任务状态：与后端 Task.Status 的取值保持一致 */
export type TaskStatus =
  | 'created'
  | 'queued'
  | 'extracting'
  | 'transcribing'
  | 'translating'
  | 'paused'
  | 'completed'
  | 'failed'
  | 'canceled';

/** 字幕条目的翻译状态 */
export type TranslationStatus = 'translated' | 'pending' | 'translating' | 'failed';

/** 一个字幕任务的完整快照 */
export interface Task {
  id: string;
  name: string;
  sourceFile: string;
  sourceLanguage: string;
  targetLanguage: string;
  status: string;
  stage: string;
  progress: number;
  currentStage: string;
  subtitleCount: number;
  translatedCount: number;
  message: string;
  error: string;
  engine: string;
  model: string;
  provider: string;
  outputFormat: string;
  duration: number;
  createdAt: string;
  updatedAt: string;
}

/** 新建任务入参 */
export interface CreateTaskInput {
  sourceFile: string;
  sourceLanguage: string;
  targetLanguage: string;
  outputFormat: string;
  engine: string;
  model: string;
  translate: boolean;
  provider: string;
}

/** 工作台首屏数据 */
export interface Dashboard {
  activeTask: Task | null;
  tasks: Task[];
}

/** 单条字幕 */
export interface SubtitleSegment {
  id: string;
  sequence: number;
  start: number;
  end: number;
  sourceText: string;
  translatedText: string;
  translationStatus: string;
}

/** 可下载 / 已安装的识别模型 */
export interface ModelInfo {
  id: string;
  name: string;
  repo: string;
  sizeBytes: number;
  installed: boolean;
  installing: boolean;
  progress: number;
  localPath: string;
  mirror: string;
  description: string;
  files: string[];
  /** 适用的识别引擎：faster-whisper 或 transformers */
  engine: string;
  /** 用户自选 / 自建条目，不是内置模型 */
  custom: boolean;
  /** Hugging Face 受限仓库，下载需要访问令牌 */
  gated: boolean;
  /** 本地目录模型：不下载、不删除 */
  local: boolean;
  /** 本地模型目录是否可用（目录被移动时为 false） */
  installedOk: boolean;
}

/** 自选本地模型入参 */
export interface AddLocalModelInput {
  directory: string;
  id: string;
}

/** 按链接添加模型入参 */
export interface AddRepoModelInput {
  repo: string;
  id: string;
  engine: string;
}

/** 本机 Ollama 中已安装的一个模型 */
export interface OllamaModelInfo {
  name: string;
  size: number;
  parameterSize: string;
  quantization: string;
  family: string;
  modifiedAt: string;
}

/** 设置快照（API Key 只返回是否已配置） */
export interface SettingsSnapshot {
  ffmpegPath: string;
  pythonPath: string;
  modelDir: string;
  mirror: string;
  huggingFaceEndpoint: string;
  asrEngine: string;
  asrDevice: string;
  computeType: string;
  defaultModel: string;
  translationProvider: string;
  translationEndpoint: string;
  translationModel: string;
  translationApiKeySet: boolean;
  cloudPrivacyConfirm: boolean;
  /** 翻译语气：subtitle / formal / casual / literal / explicit */
  translationStyle: string;
  /** 术语表原文，每行一条：源术语=目标译法 */
  glossaryText: string;
  /** 是否已配置 Hugging Face 访问令牌（不返回明文） */
  huggingFaceTokenSet: boolean;
  /** 自选的本地识别模型：标识 -> 目录 */
  localModels: Record<string, string>;
}

/** 设置的部分更新入参，未给出的字段表示保持不变 */
export interface SettingsPatch {
  ffmpegPath?: string;
  pythonPath?: string;
  modelDir?: string;
  mirror?: string;
  huggingFaceEndpoint?: string;
  asrEngine?: string;
  asrDevice?: string;
  computeType?: string;
  defaultModel?: string;
  translationProvider?: string;
  translationEndpoint?: string;
  translationModel?: string;
  translationApiKey?: string;
  cloudPrivacyConfirm?: boolean;
  translationStyle?: string;
  glossaryText?: string;
  /** HF 访问令牌；空串表示保持不变，"-" 表示清除 */
  huggingFaceToken?: string;
}

/** 字幕压制入参 */
export interface BurnSubtitlesInput {
  /** 留空时由后端弹出系统「另存为」对话框 */
  outputPath: string;
  fontName: string;
  fontSize: number;
  marginV: number;
}

/** FFmpeg 下载源 */
export interface FFmpegSource {
  id: string;
  label: string;
  url: string;
  note: string;
  license: string;
}

/** FFmpeg 安装与可用性状态 */
export interface FFmpegStatus {
  /** 是否指定过路径；false 即「未安装」 */
  configured: boolean;
  path: string;
  version: string;
  ok: boolean;
  /** 是否支持字幕压制（需带 libass 的完整版） */
  canBurn: boolean;
  detail: string;
  /** 默认安装目录（应用数据目录下的 ffmpeg/） */
  defaultDir: string;
  dataDir: string;
  downloading: boolean;
  sources: FFmpegSource[];
}

/** FFmpeg 下载入参 */
export interface DownloadFFmpegInput {
  /** 安装目录，留空用默认目录 */
  dir: string;
  /** 指定下载源，留空按内置顺序自动降级 */
  sourceId: string;
}

/** ffmpeg:progress 事件载荷 */
export interface FFmpegProgressEvent {
  progress: number;
  message: string;
  done: boolean;
  error: string;
  path: string;
}

/** 运行环境探测结果 */
export interface EnvironmentStatus {
  ffmpegPath: string;
  ffmpegOk: boolean;
  ffmpegDetail: string;
  ffmpegSubtitle: boolean;
  /** FFmpeg 默认安装目录，未安装时用来提示用户 */
  ffmpegDefaultDir: string;
  pythonPath: string;
  pythonOk: boolean;
  pythonDetail: string;
  fasterWhisper: boolean;
  /** 是否安装了 torch + transformers（cohere 等模型需要） */
  transformers: boolean;
  gpu: string;
  modelDir: string;
}

/** task:progress 事件载荷 */
export interface TaskProgressEvent {
  taskId: string;
  status: string;
  stage: string;
  progress: number;
  currentStage: string;
  message: string;
  error: string;
}

/** model:progress 事件载荷 */
export interface ModelProgressEvent {
  modelId: string;
  progress: number;
  message: string;
  done: boolean;
  error: string;
}

/** 支持的导出格式 */
export type ExportFormat = 'srt' | 'vtt' | 'txt' | 'json' | 'bilingual-srt';

/** 导出格式的下拉选项 */
export const EXPORT_FORMATS: { value: ExportFormat; label: string }[] = [
  { value: 'srt', label: 'SRT 字幕' },
  { value: 'vtt', label: 'WebVTT 字幕' },
  { value: 'txt', label: '纯文本 TXT' },
  { value: 'json', label: '结构化 JSON' },
  { value: 'bilingual-srt', label: '双语 SRT' },
];

/** 源语言选项 */
export const SOURCE_LANGUAGES: { value: string; label: string }[] = [
  { value: 'auto', label: '自动检测' },
  { value: 'en', label: '英语' },
  { value: 'zh', label: '中文' },
  { value: 'ja', label: '日语' },
  { value: 'ko', label: '韩语' },
  { value: 'fr', label: '法语' },
  { value: 'de', label: '德语' },
  { value: 'es', label: '西班牙语' },
  { value: 'ru', label: '俄语' },
];

/** 目标语言选项 */
export const TARGET_LANGUAGES: { value: string; label: string }[] = [
  { value: 'zh-CN', label: '简体中文' },
  { value: 'zh-TW', label: '繁体中文' },
  { value: 'en', label: '英语' },
  { value: 'ja', label: '日语' },
  { value: 'ko', label: '韩语' },
];

/** 识别引擎选项 */
export const ASR_ENGINES: { value: string; label: string }[] = [
  { value: 'faster-whisper', label: 'faster-whisper' },
];

/** 运行设备选项 */
export const ASR_DEVICES: { value: string; label: string }[] = [
  { value: 'auto', label: '自动选择' },
  { value: 'cpu', label: 'CPU' },
  { value: 'cuda', label: 'CUDA（NVIDIA 显卡）' },
];

/** 计算精度选项 */
export const COMPUTE_TYPES: { value: string; label: string }[] = [
  { value: 'auto', label: '自动' },
  { value: 'int8', label: 'int8（省显存）' },
  { value: 'float16', label: 'float16（质量优先）' },
];

/** 翻译 Provider 选项 */
export const TRANSLATION_PROVIDERS: { value: string; label: string; hint: string }[] = [
  { value: 'ollama', label: 'Ollama', hint: '本地模型，数据不出本机' },
  { value: 'openai', label: 'OpenAI 兼容接口', hint: '云端服务，需要确认隐私设置' },
];

/** 翻译语气选项 */
export const TRANSLATION_STYLES: { value: string; label: string; hint?: string }[] = [
  { value: 'subtitle', label: '影视字幕（简洁口语）' },
  { value: 'formal', label: '正式书面' },
  { value: 'casual', label: '轻松口语' },
  { value: 'literal', label: '直译（贴近原文）' },
  {
    value: 'explicit',
    label: '露骨（成人内容）',
    hint: '面向成人影音：直白翻译性相关内容，不淡化、不净化、不回避',
  },
];
