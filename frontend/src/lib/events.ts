// Wails Events 的订阅封装，外加纯浏览器下的降级实现。
//
// 组件里统一调用 `subscribeTaskProgress` / `subscribeModelProgress`，返回一个取消订阅函数，
// 在 useEffect 的清理函数里调用即可；非 Wails 环境下返回空取消函数，不会抛错。
import { EventsOn } from '../../wailsjs/runtime/runtime';
import { isWailsRuntime } from './runtime';
import type { ModelProgressEvent, TaskProgressEvent } from './types';

type Unsubscribe = () => void;

/** 事件名常量，和后端 EventsEmit 使用的一致 */
export const EVENT_TASK_PROGRESS = 'task:progress';
export const EVENT_MODEL_PROGRESS = 'model:progress';

/** 安全地读取事件载荷中的字符串字段 */
function pickString(source: Record<string, unknown>, key: string): string {
  const value = source[key];
  return typeof value === 'string' ? value : '';
}

/** 安全地读取事件载荷中的数字字段 */
function pickNumber(source: Record<string, unknown>, key: string): number {
  const value = source[key];
  return typeof value === 'number' && Number.isFinite(value) ? value : 0;
}

function pickBoolean(source: Record<string, unknown>, key: string): boolean {
  const value = source[key];
  return value === true;
}

function asRecord(payload: unknown): Record<string, unknown> {
  if (payload && typeof payload === 'object') return payload as Record<string, unknown>;
  // Wails 偶尔会把 JSON 当作字符串推送，这里顺带兼容一下
  if (typeof payload === 'string') {
    try {
      const parsed: unknown = JSON.parse(payload);
      if (parsed && typeof parsed === 'object') return parsed as Record<string, unknown>;
    } catch {
      return {};
    }
  }
  return {};
}

function normalizeTaskProgress(payload: unknown): TaskProgressEvent {
  const record = asRecord(payload);
  return {
    taskId: pickString(record, 'taskId'),
    status: pickString(record, 'status'),
    stage: pickString(record, 'stage'),
    progress: pickNumber(record, 'progress'),
    currentStage: pickString(record, 'currentStage'),
    message: pickString(record, 'message'),
    error: pickString(record, 'error'),
  };
}

function normalizeModelProgress(payload: unknown): ModelProgressEvent {
  const record = asRecord(payload);
  return {
    modelId: pickString(record, 'modelId'),
    progress: pickNumber(record, 'progress'),
    message: pickString(record, 'message'),
    done: pickBoolean(record, 'done'),
    error: pickString(record, 'error'),
  };
}

/** 订阅任务进度事件，返回取消订阅函数 */
export function subscribeTaskProgress(handler: (event: TaskProgressEvent) => void): Unsubscribe {
  if (!isWailsRuntime()) return () => undefined;
  try {
    const off = EventsOn(EVENT_TASK_PROGRESS, (payload: unknown) => {
      handler(normalizeTaskProgress(payload));
    });
    return typeof off === 'function' ? off : () => undefined;
  } catch {
    return () => undefined;
  }
}

/** 订阅模型下载进度事件，返回取消订阅函数 */
export function subscribeModelProgress(handler: (event: ModelProgressEvent) => void): Unsubscribe {
  if (!isWailsRuntime()) return () => undefined;
  try {
    const off = EventsOn(EVENT_MODEL_PROGRESS, (payload: unknown) => {
      handler(normalizeModelProgress(payload));
    });
    return typeof off === 'function' ? off : () => undefined;
  } catch {
    return () => undefined;
  }
}
