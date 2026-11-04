// 任务队列视图：列出全部任务，支持行内暂停/恢复/取消/重试/删除，点击行进入工作台。
import { useCallback, useEffect, useMemo, useState } from 'react';
import * as api from '../lib/api';
import {
  clampProgress,
  formatDateTime,
  formatDuration,
  isRunningStatus,
  languageLabel,
  statusLabel,
  statusTone,
} from '../lib/format';
import type { Task, TaskProgressEvent } from '../lib/types';
import { DemoNotice, EmptyState, ErrorBar, LoadingState, ProgressBar, SegmentControl, StatusBadge } from './components/primitives';

type QueueFilter = 'all' | 'running' | 'finished';

export interface TaskQueueProps {
  demo: boolean;
  activeTaskId: string | null;
  progressEvent: TaskProgressEvent | null;
  onOpenTask: (task: Task) => void;
  onNewTask: () => void;
  onTasksChanged: () => void;
}

/** 行内操作定义 */
type RowAction = 'pause' | 'resume' | 'cancel' | 'retry' | 'delete' | 'start';

interface RowActionItem {
  action: RowAction;
  label: string;
  tone?: 'danger';
}

/** 依据任务状态决定行内按钮 */
function rowActions(status: string): RowActionItem[] {
  switch (status) {
    case 'extracting':
    case 'transcribing':
    case 'translating':
      return [
        { action: 'pause', label: '暂停' },
        { action: 'cancel', label: '取消', tone: 'danger' },
      ];
    case 'queued':
      return [{ action: 'cancel', label: '取消', tone: 'danger' }];
    case 'paused':
      return [
        { action: 'resume', label: '恢复' },
        { action: 'cancel', label: '取消', tone: 'danger' },
      ];
    case 'failed':
      return [
        { action: 'retry', label: '重试' },
        { action: 'delete', label: '删除', tone: 'danger' },
      ];
    case 'created':
      return [
        { action: 'start', label: '开始' },
        { action: 'delete', label: '删除', tone: 'danger' },
      ];
    default:
      return [{ action: 'delete', label: '删除', tone: 'danger' }];
  }
}

export function TaskQueue({ demo, activeTaskId, progressEvent, onOpenTask, onNewTask, onTasksChanged }: TaskQueueProps) {
  const [tasks, setTasks] = useState<Task[]>([]);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [busyId, setBusyId] = useState<string | null>(null);
  const [filter, setFilter] = useState<QueueFilter>('all');

  const load = useCallback(async () => {
    if (demo) {
      setTasks([]);
      setLoading(false);
      return;
    }
    try {
      const dashboard = await api.getDashboard();
      setTasks(dashboard.tasks ?? []);
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : '读取任务列表失败，请稍后重试。');
    } finally {
      setLoading(false);
    }
  }, [demo]);

  useEffect(() => {
    setLoading(true);
    void load();
  }, [load]);

  // 全局进度事件：就地更新对应任务，跑完的任务自动补刷列表
  useEffect(() => {
    if (!progressEvent) return;
    setTasks((prev) =>
      prev.map((task) =>
        task.id === progressEvent.taskId
          ? {
              ...task,
              status: progressEvent.status || task.status,
              progress: Number.isFinite(progressEvent.progress) ? progressEvent.progress : task.progress,
              currentStage: progressEvent.currentStage || task.currentStage,
              message: progressEvent.message || task.message,
              error: progressEvent.error || task.error,
            }
          : task,
      ),
    );
    if (progressEvent.status === 'completed' || progressEvent.status === 'failed' || progressEvent.status === 'canceled') {
      void load();
      onTasksChanged();
    }
  }, [progressEvent, load, onTasksChanged]);

  const runningCount = useMemo(() => tasks.filter((task) => isRunningStatus(task.status)).length, [tasks]);
  const finishedCount = tasks.length - runningCount;

  const visible = useMemo(() => {
    if (filter === 'running') return tasks.filter((task) => isRunningStatus(task.status));
    if (filter === 'finished') return tasks.filter((task) => !isRunningStatus(task.status));
    return tasks;
  }, [tasks, filter]);

  const runAction = async (task: Task, action: RowAction) => {
    if (demo) return;
    if (action === 'delete' && !window.confirm(`确定删除任务「${task.name}」及其字幕吗？该操作不可恢复。`)) {
      return;
    }
    setBusyId(task.id);
    setError('');
    try {
      let updated: Task | null = null;
      switch (action) {
        case 'pause':
          updated = await api.pauseTask(task.id);
          break;
        case 'resume':
          updated = await api.resumeTask(task.id);
          break;
        case 'cancel':
          updated = await api.cancelTask(task.id);
          break;
        case 'retry':
          updated = await api.retryTask(task.id);
          break;
        case 'start':
          updated = await api.startTask(task.id);
          break;
        case 'delete':
          await api.deleteTask(task.id);
          setTasks((prev) => prev.filter((item) => item.id !== task.id));
          onTasksChanged();
          return;
      }
      if (updated) {
        const next = updated;
        setTasks((prev) => prev.map((item) => (item.id === next.id ? next : item)));
        onTasksChanged();
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : '任务操作失败，请稍后重试。');
    } finally {
      setBusyId(null);
    }
  };

  return (
    <div className="page">
      <DemoNotice show={demo} />
      <div className="page-heading">
        <div>
          <div className="eyebrow">任务调度</div>
          <h1>任务队列</h1>
          <p className="muted">
            共 {tasks.length} 个任务 · 进行中 {runningCount} 个 · 已结束 {finishedCount} 个
          </p>
        </div>
        <div className="heading-actions">
          <button type="button" className="button secondary" disabled={demo} onClick={() => void load()}>
            刷新
          </button>
          <button type="button" className="button primary" onClick={onNewTask} disabled={demo}>
            新建任务
          </button>
        </div>
      </div>

      <ErrorBar message={error} onDismiss={() => setError('')} />

      {loading ? (
        <LoadingState text="正在读取任务队列…" />
      ) : tasks.length === 0 ? (
        <EmptyState
          icon="≡"
          title="队列里还没有任务"
          description={demo ? '演示模式不会显示虚构任务，请通过桌面客户端打开。' : '新建一个任务后，它会出现在这里并显示实时进度。'}
          actions={
            <button type="button" className="button primary" onClick={onNewTask} disabled={demo}>
              新建任务
            </button>
          }
        />
      ) : (
        <section className="queue-card card">
          <div className="card-header queue-header">
            <div>
              <h2>全部任务</h2>
              <span className="card-subtitle">点击任意一行进入工作台查看字幕</span>
            </div>
            <SegmentControl<QueueFilter>
              value={filter}
              onChange={setFilter}
              options={[
                { value: 'all', label: '全部', count: tasks.length },
                { value: 'running', label: '进行中', count: runningCount },
                { value: 'finished', label: '已结束', count: finishedCount },
              ]}
            />
          </div>

          <div className="queue-table">
            <div className="queue-head">
              <span>任务</span>
              <span>状态</span>
              <span>进度</span>
              <span>创建时间</span>
              <span>操作</span>
            </div>
            {visible.length === 0 ? (
              <div className="subtitle-empty filter-empty">没有符合当前筛选条件的任务。</div>
            ) : (
              visible.map((task) => {
                const rowActionsList = rowActions(task.status);
                const active = task.id === activeTaskId;
                return (
                  <div
                    key={task.id}
                    className={`queue-row ${active ? 'active' : ''}`}
                    role="button"
                    tabIndex={0}
                    onClick={() => onOpenTask(task)}
                    onKeyDown={(event) => {
                      if (event.key === 'Enter' || event.key === ' ') {
                        event.preventDefault();
                        onOpenTask(task);
                      }
                    }}
                  >
                    <div className="queue-task">
                      <strong>{task.name || '未命名任务'}</strong>
                      <small>
                        {formatDuration(task.duration)}
                        <span className="separator">·</span>
                        {languageLabel(task.sourceLanguage)} → {languageLabel(task.targetLanguage)}
                        <span className="separator">·</span>
                        {task.model || task.engine || '未指定模型'}
                      </small>
                      {task.message ? <small className="queue-message">{task.message}</small> : null}
                    </div>
                    <div className="queue-status">
                      <StatusBadge tone={statusTone(task.status)}>{statusLabel(task.status)}</StatusBadge>
                      <small>{task.currentStage || task.stage || '—'}</small>
                    </div>
                    <div className="queue-progress">
                      <ProgressBar
                        value={clampProgress(task.progress)}
                        compact
                        tone={task.status === 'failed' ? 'failed' : task.status === 'paused' ? 'paused' : task.status === 'completed' ? 'success' : 'active'}
                      />
                      <small>
                        {clampProgress(task.progress)}%
                        {task.subtitleCount > 0 ? ` · ${task.translatedCount}/${task.subtitleCount} 条已译` : ''}
                      </small>
                    </div>
                    <div className="queue-time">{formatDateTime(task.createdAt)}</div>
                    <div className="queue-actions" onClick={(event) => event.stopPropagation()}>
                      {rowActionsList.map((item) => (
                        <button
                          key={item.action}
                          type="button"
                          className={`queue-action ${item.tone === 'danger' ? 'danger' : ''}`}
                          disabled={demo || busyId === task.id}
                          onClick={() => void runAction(task, item.action)}
                        >
                          {item.label}
                        </button>
                      ))}
                      {task.error ? (
                        <span className="queue-action-error" title={task.error}>
                          错误
                        </span>
                      ) : null}
                    </div>
                  </div>
                );
              })
            )}
          </div>
        </section>
      )}
    </div>
  );
}
