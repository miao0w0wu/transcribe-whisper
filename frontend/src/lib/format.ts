// 展示层格式化工具。所有函数都做防御性处理：后端字段可能是空串或 0。
import type { ExportFormat, TaskStatus } from './types';

/** 秒数格式化为 `42:18`，超过一小时为 `1:02:18`；未知时长返回 `--:--` */
export function formatDuration(seconds: number | null | undefined): string {
  if (!seconds || !Number.isFinite(seconds) || seconds <= 0) return '--:--';
  const total = Math.floor(seconds);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  const mm = String(minutes).padStart(2, '0');
  const ss = String(secs).padStart(2, '0');
  return hours > 0 ? `${hours}:${mm}:${ss}` : `${mm}:${ss}`;
}

/** 秒数格式化为字幕时间轴 `00:12:35.200`（毫秒用点号） */
export function formatTimecode(seconds: number | null | undefined): string {
  if (seconds === null || seconds === undefined || !Number.isFinite(seconds) || seconds < 0) {
    return '00:00:00.000';
  }
  const total = Math.floor(seconds);
  const millis = Math.round((seconds - total) * 1000);
  const hours = Math.floor(total / 3600);
  const minutes = Math.floor((total % 3600) / 60);
  const secs = total % 60;
  return `${String(hours).padStart(2, '0')}:${String(minutes).padStart(2, '0')}:${String(secs).padStart(2, '0')}.${String(millis).padStart(3, '0')}`;
}

/** 时间轴字符串回写为秒；非法输入返回 0 */
export function parseTimecode(value: string): number {
  const match = value.trim().match(/^(?:(\d+):)?(\d{1,2}):(\d{1,2})(?:[.,](\d{1,3}))?$/);
  if (!match) return 0;
  const hours = Number(match[1] ?? 0);
  const minutes = Number(match[2]);
  const secs = Number(match[3]);
  const millis = Number((match[4] ?? '0').padEnd(3, '0'));
  return hours * 3600 + minutes * 60 + secs + millis / 1000;
}

/** 文件或字节数格式化为人类可读大小 */
export function formatBytes(bytes: number | null | undefined): string {
  if (!bytes || !Number.isFinite(bytes) || bytes <= 0) return '未知';
  const units = ['B', 'KB', 'MB', 'GB', 'TB'];
  let value = bytes;
  let unitIndex = 0;
  while (value >= 1024 && unitIndex < units.length - 1) {
    value /= 1024;
    unitIndex += 1;
  }
  const digits = value >= 100 || unitIndex === 0 ? 0 : 1;
  return `${value.toFixed(digits)} ${units[unitIndex]}`;
}

/** ISO 时间转 `2026-11-04 16:11`；解析失败时回退原串 */
export function formatDateTime(value: string | null | undefined): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const pad = (input: number) => String(input).padStart(2, '0');
  return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ${pad(date.getHours())}:${pad(date.getMinutes())}`;
}

/** 相对时间描述，列表里展示任务新旧 */
export function formatRelative(value: string | null | undefined): string {
  if (!value) return '—';
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  const diff = Date.now() - date.getTime();
  if (diff < 60_000) return '刚刚';
  if (diff < 3_600_000) return `${Math.floor(diff / 60_000)} 分钟前`;
  if (diff < 86_400_000) return `${Math.floor(diff / 3_600_000)} 小时前`;
  if (diff < 30 * 86_400_000) return `${Math.floor(diff / 86_400_000)} 天前`;
  return formatDateTime(value);
}

/** 只取文件名（兼容 Windows 反斜杠路径） */
export function baseName(filePath: string): string {
  if (!filePath) return '';
  const normalized = filePath.replace(/\\/g, '/');
  const index = normalized.lastIndexOf('/');
  return index >= 0 ? normalized.slice(index + 1) : normalized;
}

const STATUS_LABELS: Record<string, string> = {
  created: '待开始',
  queued: '排队中',
  extracting: '提取音频',
  transcribing: '识别中',
  translating: '翻译中',
  paused: '已暂停',
  completed: '已完成',
  failed: '失败',
  canceled: '已取消',
};

/** 任务状态的中文名 */
export function statusLabel(status: string | null | undefined): string {
  if (!status) return '未知状态';
  return STATUS_LABELS[status] ?? status;
}

/** 任务状态对应的徽章样式名（active / paused / success / failed / muted） */
export function statusTone(status: string | null | undefined): 'active' | 'paused' | 'success' | 'failed' | 'muted' {
  switch (status) {
    case 'extracting':
    case 'transcribing':
    case 'translating':
    case 'queued':
      return 'active';
    case 'paused':
      return 'paused';
    case 'completed':
      return 'success';
    case 'failed':
      return 'failed';
    default:
      return 'muted';
  }
}

/** 状态是否表示任务正在运行 */
export function isRunningStatus(status: string | null | undefined): boolean {
  return status === 'extracting' || status === 'transcribing' || status === 'translating' || status === 'queued';
}

/** 把进度值限制在 0-100 的整数 */
export function clampProgress(value: number | null | undefined): number {
  if (!value || !Number.isFinite(value)) return 0;
  return Math.min(100, Math.max(0, Math.round(value)));
}

/** 语言代码转中文名 */
export function languageLabel(code: string | null | undefined): string {
  if (!code) return '未知';
  const map: Record<string, string> = {
    auto: '自动检测',
    en: '英语',
    zh: '中文',
    'zh-CN': '简体中文',
    'zh-TW': '繁体中文',
    ja: '日语',
    ko: '韩语',
    fr: '法语',
    de: '德语',
    es: '西班牙语',
    ru: '俄语',
  };
  return map[code] ?? code;
}

/** 导出格式的中文名 */
export function exportFormatLabel(format: ExportFormat | string): string {
  const map: Record<string, string> = {
    srt: 'SRT',
    vtt: 'VTT',
    txt: 'TXT',
    json: 'JSON',
    'bilingual-srt': '双语 SRT',
  };
  return map[format] ?? format;
}

/** 导出格式对应的 MIME 类型 */
export function exportMimeType(format: ExportFormat): string {
  if (format === 'json') return 'application/json;charset=utf-8';
  if (format === 'vtt') return 'text/vtt;charset=utf-8';
  return 'text/plain;charset=utf-8';
}

/** 字幕条目翻译状态的中文名 */
export function translationStatusLabel(status: string | null | undefined): string {
  switch (status) {
    case 'translated':
      return '已译';
    case 'translating':
      return '翻译中';
    case 'failed':
      return '失败';
    default:
      return '待译';
  }
}

/** 从任务状态推出进度卡标题文案 */
export function headlineForStatus(status: string | null | undefined): string {
  if (status === 'failed') return '任务失败，可查看错误后重试';
  if (status === 'paused') return '任务已暂停，可随时恢复';
  if (status === 'completed') return '任务已完成，可以校对并导出字幕';
  if (status === 'canceled') return '任务已取消';
  if (!isRunningStatus(status)) return '任务尚未开始';
  return '任务正在处理中';
}

/** 生成浏览器下载用的文件名 */
export function downloadFileName(taskName: string, format: ExportFormat): string {
  const safeName = (taskName || 'subtitle').trim() || 'subtitle';
  return `${safeName}.${format}`;
}
