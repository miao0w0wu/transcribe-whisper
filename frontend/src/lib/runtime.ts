// Wails 运行时探测与底层调用封装。
//
// 关键点：`npm run dev` 在纯浏览器里打开时不存在 `window.go`，直接调用生成绑定会抛
// TypeError。这里统一做运行时判断，并把调用统一包装成中文错误信息。
import * as AppBridge from '../../wailsjs/go/main/App';

/** 演示模式提示文案，界面上多处复用 */
export const DEMO_MODE_NOTICE = '未检测到本地后端，界面处于演示模式';

/** 浏览器端错误：后端不在场时使用，页面据此进入演示模式而不是崩溃 */
export class BackendUnavailableError extends Error {
  constructor(message: string = DEMO_MODE_NOTICE) {
    super(message);
    this.name = 'BackendUnavailableError';
  }
}

/** 业务错误：后端返回了可读的错误原文，已补上中文前缀 */
export class ApiError extends Error {
  constructor(message: string) {
    super(message);
    this.name = 'ApiError';
  }
}

type AppBridgeFunction = (...args: unknown[]) => Promise<unknown>;

/** 判断当前是否运行在 Wails 容器内（window.go.main.App 可用） */
export function isWailsRuntime(): boolean {
  if (typeof window === 'undefined') return false;
  const bridge = (window as unknown as { go?: { main?: { App?: unknown } } }).go;
  return Boolean(bridge && bridge.main && bridge.main.App);
}

/** 把各种形态的异常统一成带中文前缀的 Error */
function normalizeError(error: unknown, action: string): Error {
  if (error instanceof BackendUnavailableError || error instanceof ApiError) return error;
  // Wails 绑定的失败会以字符串形式抛出（后端 panic / error 的原文）
  if (typeof error === 'string' && error.trim()) {
    return new ApiError(`${action}失败：${error.trim()}`);
  }
  if (error instanceof Error && error.message) {
    return new ApiError(`${action}失败：${error.message}`);
  }
  return new ApiError(`${action}失败，请稍后重试。`);
}

/**
 * 调用一个 Wails 绑定方法。
 * - 不在 Wails 运行时：抛出 BackendUnavailableError（调用方据此走演示/只读分支）
 * - 后端抛错：统一转换成带中文前缀的 ApiError
 */
export async function invoke<T>(name: string, action: string, args: unknown[] = []): Promise<T> {
  if (!isWailsRuntime()) {
    throw new BackendUnavailableError();
  }
  const fn = (AppBridge as unknown as Record<string, AppBridgeFunction | undefined>)[name];
  if (typeof fn !== 'function') {
    throw new ApiError(`${action}失败：当前后端未提供 ${name} 接口，请重新执行 wails build 生成绑定。`);
  }
  try {
    const result = args.length > 0 ? await fn(...args) : await fn();
    return result as T;
  } catch (error) {
    throw normalizeError(error, action);
  }
}
