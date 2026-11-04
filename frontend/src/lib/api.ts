// 统一的后端 API 封装。
// 所有页面只从这里取数据，不直接 import wailsjs 绑定；
// 这样纯浏览器（无 Wails 运行时）下的演示模式、错误文案统一处理都集中在一处。
import { invoke } from './runtime';
import type {
  CreateTaskInput,
  Dashboard,
  EnvironmentStatus,
  ExportFormat,
  ModelInfo,
  SettingsPatch,
  SettingsSnapshot,
  SubtitleSegment,
  Task,
} from './types';

export { ApiError, BackendUnavailableError, DEMO_MODE_NOTICE, isWailsRuntime } from './runtime';
export type {
  CreateTaskInput,
  Dashboard,
  EnvironmentStatus,
  ExportFormat,
  ModelInfo,
  SettingsPatch,
  SettingsSnapshot,
  SubtitleSegment,
  Task,
  TaskProgressEvent,
  ModelProgressEvent,
  TaskStatus,
  TranslationStatus,
} from './types';

/** 读取工作台首屏数据（活动任务 + 全部任务） */
export function getDashboard(): Promise<Dashboard> {
  return invoke<Dashboard>('GetDashboard', '读取工作台数据');
}

/** 创建任务（尚未开始） */
export function createTask(input: CreateTaskInput): Promise<Task> {
  return invoke<Task>('CreateTask', '创建任务', [input]);
}

/** 任务入队并开始执行 */
export function startTask(taskID: string): Promise<Task> {
  return invoke<Task>('StartTask', '启动任务', [taskID]);
}

/** 暂停任务 */
export function pauseTask(taskID: string): Promise<Task> {
  return invoke<Task>('PauseTask', '暂停任务', [taskID]);
}

/** 恢复任务 */
export function resumeTask(taskID: string): Promise<Task> {
  return invoke<Task>('ResumeTask', '恢复任务', [taskID]);
}

/** 取消任务 */
export function cancelTask(taskID: string): Promise<Task> {
  return invoke<Task>('CancelTask', '取消任务', [taskID]);
}

/** 失败后重试 */
export function retryTask(taskID: string): Promise<Task> {
  return invoke<Task>('RetryTask', '重试任务', [taskID]);
}

/** 重新翻译整个任务 */
export function retranslateTask(taskID: string): Promise<Task> {
  return invoke<Task>('RetranslateTask', '重新翻译', [taskID]);
}

/** 只翻译接口中尚未翻译的条目 */
export function translatePending(taskID: string): Promise<Task> {
  return invoke<Task>('TranslatePending', '翻译待译条目', [taskID]);
}

/** 打开系统文件选择框，返回绝对路径（用户取消时返回空串） */
export function pickFile(): Promise<string> {
  return invoke<string>('PickFile', '选择文件');
}

/** 删除任务 */
export function deleteTask(taskID: string): Promise<void> {
  return invoke<void>('DeleteTask', '删除任务', [taskID]);
}

/** 读取字幕条目 */
export function listSubtitles(taskID: string): Promise<SubtitleSegment[]> {
  return invoke<SubtitleSegment[]>('ListSubtitles', '读取字幕', [taskID]);
}

/** 保存字幕编辑结果 */
export function updateSubtitles(taskID: string, segments: SubtitleSegment[]): Promise<void> {
  return invoke<void>('UpdateSubtitles', '保存字幕', [taskID, segments]);
}

/** 生成导出文本（不落盘） */
export function exportSubtitles(taskID: string, format: ExportFormat): Promise<string> {
  return invoke<string>('ExportSubtitles', '导出字幕', [taskID, format]);
}

/** 让后端弹出系统保存对话框并写入文件，返回写入的路径 */
export function saveExport(taskID: string, format: ExportFormat): Promise<string> {
  return invoke<string>('SaveExport', '另存为', [taskID, format]);
}

/** 模型列表 */
export function listModels(): Promise<ModelInfo[]> {
  return invoke<ModelInfo[]>('ListModels', '读取模型列表');
}

/** 下载模型 */
export function downloadModel(modelID: string): Promise<ModelInfo> {
  return invoke<ModelInfo>('DownloadModel', '下载模型', [modelID]);
}

/** 取消模型下载 */
export function cancelModelDownload(modelID: string): Promise<void> {
  return invoke<void>('CancelModelDownload', '取消模型下载', [modelID]);
}

/** 删除本地模型 */
export function removeModel(modelID: string): Promise<void> {
  return invoke<void>('RemoveModel', '删除模型', [modelID]);
}

/** 读取设置 */
export function getSettings(): Promise<SettingsSnapshot> {
  return invoke<SettingsSnapshot>('GetSettings', '读取设置');
}

/** 更新设置（未给出的字段保持不变） */
export function updateSettings(patch: SettingsPatch): Promise<SettingsSnapshot> {
  return invoke<SettingsSnapshot>('UpdateSettings', '保存设置', [patch]);
}

/** 测试翻译服务连通性，成功返回中文结论 */
export function testTranslationProvider(): Promise<string> {
  return invoke<string>('TestTranslationProvider', '测试翻译服务');
}

/** 探测 FFmpeg / Python / faster-whisper / GPU */
export function getEnvironment(): Promise<EnvironmentStatus> {
  return invoke<EnvironmentStatus>('GetEnvironment', '检测运行环境');
}

/** 打开模型缓存目录 */
export function openModelFolder(): Promise<void> {
  return invoke<void>('OpenModelFolder', '打开缓存目录');
}
