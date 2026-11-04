// 通用 UI 小组件：视图之间复用的进度条、徽章、空状态、错误条、提示条等。
// 只做展示，不包含业务逻辑，便于各视图保持视觉一致。
import type { ReactNode } from 'react';

/** 状态徽章色调 */
export type BadgeTone = 'active' | 'paused' | 'success' | 'failed' | 'muted';

/** 状态徽章 */
export function StatusBadge({ tone, children }: { tone: BadgeTone; children: ReactNode }) {
  return <span className={`status-badge ${tone}`}>{children}</span>;
}

/** 进度条；tone 用于失败/暂停时换色 */
export function ProgressBar({
  value,
  tone = 'active',
  compact = false,
}: {
  value: number;
  tone?: 'active' | 'failed' | 'paused' | 'success';
  compact?: boolean;
}) {
  const width = Math.min(100, Math.max(0, Number.isFinite(value) ? value : 0));
  return (
    <div className={`progress-track ${compact ? 'compact' : ''} ${tone}`} role="progressbar" aria-valuenow={width} aria-valuemin={0} aria-valuemax={100}>
      <div style={{ width: `${width}%` }} />
    </div>
  );
}

/** 空状态卡片：图标 + 标题 + 说明 + 可选操作 */
export function EmptyState({
  icon,
  title,
  description,
  actions,
}: {
  icon: string;
  title: string;
  description: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <div className="empty-state">
      <div className="empty-icon">{icon}</div>
      <strong>{title}</strong>
      <p>{description}</p>
      {actions ? <div className="empty-actions">{actions}</div> : null}
    </div>
  );
}

/** 错误条 */
export function ErrorBar({ message, onDismiss }: { message: string; onDismiss?: () => void }) {
  if (!message) return null;
  return (
    <div className="error-bar" role="alert">
      <span className="error-icon">⚠</span>
      <span>{message}</span>
      {onDismiss ? (
        <button type="button" className="error-dismiss" onClick={onDismiss} aria-label="关闭提示">
          ×
        </button>
      ) : null}
    </div>
  );
}

/** 成功 / 信息提示条 */
export function NoticeBar({ tone = 'info', children }: { tone?: 'info' | 'success' | 'warning'; children: ReactNode }) {
  return <div className={`notice-bar ${tone}`}>{children}</div>;
}

/** 演示模式提示：非 Wails 环境下始终显示 */
export function DemoNotice({ show }: { show: boolean }) {
  if (!show) return null;
  return (
    <div className="demo-banner" role="status">
      <span className="demo-badge">演示模式</span>
      <span>未检测到本地后端，界面处于演示模式。请通过 <code>wails dev</code> 或打包后的桌面客户端打开以使用完整功能。</span>
    </div>
  );
}

/** 表单字段：标签 + 控件 */
export function FormField({ label, hint, children }: { label: string; hint?: string; children: ReactNode }) {
  return (
    <div className="form-field">
      <span className="form-field-label">{label}</span>
      {children}
      {hint ? <small className="form-field-hint">{hint}</small> : null}
    </div>
  );
}

/** 只读信息行，用于任务详情 */
export function DetailRow({ label, value }: { label: string; value: ReactNode }) {
  return (
    <div className="detail-row">
      <span>{label}</span>
      <strong>{value}</strong>
    </div>
  );
}

/** 加载态占位 */
export function LoadingState({ text = '正在加载…' }: { text?: string }) {
  return (
    <div className="loading-state">
      <span className="loading-dot" />
      <span>{text}</span>
    </div>
  );
}

/** 分段按钮组（用于筛选） */
export function SegmentControl<T extends string>({
  value,
  options,
  onChange,
}: {
  value: T;
  options: { value: T; label: string; count?: number }[];
  onChange: (value: T) => void;
}) {
  return (
    <div className="segment-control">
      {options.map((option) => (
        <button
          key={option.value}
          type="button"
          className={option.value === value ? 'filter active' : 'filter'}
          onClick={() => onChange(option.value)}
        >
          {option.label}
          {typeof option.count === 'number' ? ` ${option.count}` : ''}
        </button>
      ))}
    </div>
  );
}
