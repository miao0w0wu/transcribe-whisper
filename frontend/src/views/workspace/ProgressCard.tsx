// 工作台进度卡：展示真实 progress / stage / currentStage / message，并按状态给出可用操作。
import type { Task } from '../../lib/api';
import { clampProgress, headlineForStatus, isRunningStatus, statusLabel, statusTone } from '../../lib/format';
import { ProgressBar, StatusBadge } from '../components/primitives';

export type TaskAction = 'start' | 'pause' | 'resume' | 'cancel' | 'retry' | 'retranslate';

interface ActionButton {
  action: TaskAction;
  label: string;
  variant: 'accent' | 'danger' | 'plain';
}

/** 根据任务状态决定进度卡上显示哪些按钮 */
function availableActions(status: string): ActionButton[] {
  switch (status) {
    case 'extracting':
    case 'transcribing':
    case 'translating':
      return [
        { action: 'pause', label: 'Ⅱ 暂停', variant: 'plain' },
        { action: 'cancel', label: '× 取消', variant: 'danger' },
      ];
    case 'queued':
      return [{ action: 'cancel', label: '× 取消', variant: 'danger' }];
    case 'paused':
      return [
        { action: 'resume', label: '▶ 恢复', variant: 'accent' },
        { action: 'cancel', label: '× 取消', variant: 'danger' },
      ];
    case 'failed':
      return [{ action: 'retry', label: '↻ 重试', variant: 'accent' }];
    case 'created':
      return [{ action: 'start', label: '▶ 开始识别', variant: 'accent' }];
    default:
      return [];
  }
}

/** 进度条配色与状态对应 */
function barTone(status: string): 'active' | 'failed' | 'paused' | 'success' {
  if (status === 'failed') return 'failed';
  if (status === 'paused') return 'paused';
  if (status === 'completed') return 'success';
  return 'active';
}

export function ProgressCard({
  task,
  busy,
  demo,
  onAction,
}: {
  task: Task;
  busy: boolean;
  demo: boolean;
  onAction: (action: TaskAction) => void;
}) {
  const progress = clampProgress(task.progress);
  const running = isRunningStatus(task.status);
  const actions = availableActions(task.status);

  return (
    <div className={`progress-card ${task.status === 'failed' ? 'has-error' : ''}`}>
      <div className="progress-top">
        <div className="progress-headline">
          <strong>{headlineForStatus(task.status)}</strong>
          <StatusBadge tone={statusTone(task.status)}>{statusLabel(task.status)}</StatusBadge>
        </div>
        <strong className="progress-value">{progress}%</strong>
      </div>
      <ProgressBar value={progress} tone={barTone(task.status)} />
      <div className="progress-bottom">
        <span>{task.currentStage || task.stage || (running ? '等待后端返回阶段信息' : '暂无阶段信息')}</span>
        {task.subtitleCount > 0 ? (
          <span>
            字幕 {task.translatedCount}/{task.subtitleCount} 条已翻译
          </span>
        ) : (
          <span>尚未生成字幕</span>
        )}
        {task.message ? <span className="progress-message">{task.message}</span> : null}
        <div className="progress-controls">
          {demo ? <span className="progress-controls-hint">演示模式不可操作</span> : null}
          {actions.map((item) => (
            <button
              key={item.action}
              type="button"
              className={`progress-action ${item.variant}`}
              disabled={busy || demo}
              onClick={() => onAction(item.action)}
            >
              {item.label}
            </button>
          ))}
        </div>
      </div>
      {task.error ? <div className="progress-error">✕ {task.error}</div> : null}
    </div>
  );
}
