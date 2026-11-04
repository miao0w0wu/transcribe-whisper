// Workspace：工作台主视图。
// 读取真实任务与字幕，承接暂停/恢复/取消/重试/重新翻译、字幕编辑保存与导出。
import { useCallback, useEffect, useState } from 'react';
import * as api from '../lib/api';
import {
  downloadFileName,
  exportFormatLabel,
  exportMimeType,
  formatDuration,
  languageLabel,
  statusLabel,
  statusTone,
} from '../lib/format';
import type { ExportFormat, SubtitleSegment, Task, TaskProgressEvent } from '../lib/types';
import { EXPORT_FORMATS } from '../lib/types';
import { DemoNotice, EmptyState, ErrorBar, LoadingState, NoticeBar, StatusBadge } from './components/primitives';
import { ProgressCard, type TaskAction } from './workspace/ProgressCard';
import { SubtitleTable } from './workspace/SubtitleTable';
import { TaskDetail } from './workspace/TaskDetail';

export interface WorkspaceProps {
  /** 当前要查看的任务 id；为空时回退到后端返回的 activeTask */
  taskId: string | null;
  demo: boolean;
  /** 全局订阅的任务进度事件，由 App 分发 */
  progressEvent: TaskProgressEvent | null;
  onNewTask: () => void;
  onOpenQueue: () => void;
  onTasksChanged: () => void;
}

/** 浏览器端触发文本文件下载 */
function downloadText(content: string, fileName: string, mimeType: string) {
  const blob = new Blob([content], { type: mimeType });
  const url = URL.createObjectURL(blob);
  const link = document.createElement('a');
  link.href = url;
  link.download = fileName;
  document.body.appendChild(link);
  link.click();
  document.body.removeChild(link);
  // 立即释放可能打断下载，延后释放更稳妥
  window.setTimeout(() => URL.revokeObjectURL(url), 1000);
}

/** 用进度事件更新任务快照，只覆盖事件里提供的字段 */
function applyProgress(task: Task, event: TaskProgressEvent): Task {
  return {
    ...task,
    status: event.status || task.status,
    stage: event.stage || task.stage,
    progress: Number.isFinite(event.progress) ? event.progress : task.progress,
    currentStage: event.currentStage || task.currentStage,
    message: event.message || task.message,
    error: event.error || (event.status === 'failed' ? task.error : ''),
  };
}

export function Workspace({ taskId, demo, progressEvent, onNewTask, onOpenQueue, onTasksChanged }: WorkspaceProps) {
  const [task, setTask] = useState<Task | null>(null);
  const [taskCount, setTaskCount] = useState(0);
  const [segments, setSegments] = useState<SubtitleSegment[]>([]);
  const [loading, setLoading] = useState(true);
  const [segmentsLoading, setSegmentsLoading] = useState(false);
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [saving, setSaving] = useState(false);
  const [translating, setTranslating] = useState(false);
  const [dirty, setDirty] = useState(false);
  const [saveHint, setSaveHint] = useState('');
  const [exportFormat, setExportFormat] = useState<ExportFormat>('srt');
  const [exportHint, setExportHint] = useState('');
  const [exporting, setExporting] = useState(false);

  const loadSubtitles = useCallback(
    async (id: string) => {
      if (demo) return;
      setSegmentsLoading(true);
      try {
        const rows = await api.listSubtitles(id);
        setSegments(rows ?? []);
        setDirty(false);
      } catch (err) {
        setError(err instanceof Error ? err.message : '读取字幕失败，请稍后重试。');
      } finally {
        setSegmentsLoading(false);
      }
    },
    [demo],
  );

  const loadDashboard = useCallback(async () => {
    if (demo) {
      setLoading(false);
      return;
    }
    try {
      const dashboard = await api.getDashboard();
      const list = dashboard.tasks ?? [];
      setTaskCount(list.length);
      const current = (taskId ? list.find((item) => item.id === taskId) : undefined) ?? dashboard.activeTask ?? null;
      setTask(current);
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : '读取工作台数据失败，请稍后重试。');
    } finally {
      setLoading(false);
    }
  }, [demo, taskId]);

  useEffect(() => {
    setLoading(true);
    void loadDashboard();
  }, [loadDashboard]);

  // 任务切换时重置局部状态并重新加载字幕
  useEffect(() => {
    setSaveHint('');
    setExportHint('');
    if (task?.id) {
      setExportFormat((task.outputFormat as ExportFormat) || 'srt');
      void loadSubtitles(task.id);
    } else {
      setSegments([]);
    }
  }, [task?.id]); // 只在任务 id 变化时触发

  // 实时进度：只更新当前任务，结束瞬间补拉一次字幕
  useEffect(() => {
    if (!progressEvent || !task || progressEvent.taskId !== task.id) return;
    setTask((prev) => (prev ? applyProgress(prev, progressEvent) : prev));
    const finished = progressEvent.status === 'completed' || progressEvent.status === 'failed';
    if (finished && !demo) {
      void loadSubtitles(task.id);
    }
  }, [progressEvent, task, demo, loadSubtitles]);

  const handleAction = async (action: TaskAction) => {
    if (!task || demo) return;
    setBusy(true);
    setError('');
    try {
      let updated: Task;
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
        case 'retranslate':
          updated = await api.retranslateTask(task.id);
          setSaveHint('已提交翻译请求，翻译进度会通过事件实时更新。');
          break;
      }
      setTask(updated);
      onTasksChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : '任务操作失败，请稍后重试。');
    } finally {
      setBusy(false);
    }
  };

  const handleSegmentChange = (segmentId: string, text: string) => {
    setSegments((prev) => prev.map((segment) => (segment.id === segmentId ? { ...segment, translatedText: text } : segment)));
    setDirty(true);
    setSaveHint('');
  };

  const handleSave = async () => {
    if (!task || demo) return;
    setSaving(true);
    setError('');
    try {
      await api.updateSubtitles(task.id, segments);
      setDirty(false);
      setSaveHint(`已保存 ${segments.length} 条字幕。`);
      onTasksChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存字幕失败，请稍后重试。');
    } finally {
      setSaving(false);
    }
  };

  const handleTranslatePending = async () => {
    if (!task || demo) return;
    setTranslating(true);
    setError('');
    try {
      // 只补翻尚未翻译的条目，不会覆盖已校对过的译文。
      const updated = await api.translatePending(task.id);
      setTask(updated);
      const remaining = updated.subtitleCount - updated.translatedCount;
      setSaveHint(
        remaining > 0
          ? `已提交翻译，仍有 ${remaining} 条待确认，请稍后刷新查看。`
          : '待译条目已全部翻译完成。',
      );
      onTasksChanged();
      await loadSubtitles(task.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : '提交翻译失败，请稍后重试。');
    } finally {
      setTranslating(false);
    }
  };

  const handleDownload = async () => {
    if (!task || demo) return;
    setExporting(true);
    setError('');
    setExportHint('');
    try {
      const content = await api.exportSubtitles(task.id, exportFormat);
      downloadText(content, downloadFileName(task.name, exportFormat), exportMimeType(exportFormat));
      setExportHint(`已生成 ${exportFormatLabel(exportFormat)} 文件并开始下载。`);
    } catch (err) {
      setError(err instanceof Error ? err.message : '导出字幕失败，请稍后重试。');
    } finally {
      setExporting(false);
    }
  };

  const handleSaveAs = async () => {
    if (!task || demo) return;
    setExporting(true);
    setError('');
    setExportHint('');
    try {
      const target = await api.saveExport(task.id, exportFormat);
      setExportHint(target ? `已保存到 ${target}` : '已取消保存。');
    } catch (err) {
      setError(err instanceof Error ? err.message : '另存为失败，请稍后重试。');
    } finally {
      setExporting(false);
    }
  };

  if (loading) {
    return (
      <div className="page">
        <LoadingState text="正在加载工作台…" />
      </div>
    );
  }

  // 空状态：没有任何任务时给出中文引导，不显示任何演示数据
  if (!task) {
    return (
      <div className="page">
        <DemoNotice show={demo} />
        <ErrorBar message={error} onDismiss={() => setError('')} />
        <EmptyState
          icon="◒"
          title="还没有字幕任务"
          description={
            demo
              ? '演示模式下不会显示虚构任务。请通过 wails dev 或桌面客户端打开，导入本地音视频后即可开始。'
              : '导入一个本地音视频文件，就能开始提取音频、识别字幕并翻译。'
          }
          actions={
            <>
              <button type="button" className="button primary" onClick={onNewTask}>
                新建任务
              </button>
              {taskCount > 0 ? (
                <button type="button" className="button secondary" onClick={onOpenQueue}>
                  查看任务队列（{taskCount}）
                </button>
              ) : null}
            </>
          }
        />
      </div>
    );
  }

  const retranslateDisabled = demo || busy || task.status === 'extracting' || task.status === 'transcribing';
  const retranslateTitle = task.subtitleCount === 0 ? '需要先完成识别才能翻译' : '重新翻译字幕';

  return (
    <div className="page workspace-page">
      <DemoNotice show={demo} />
      <div className="page-heading">
        <div>
          <div className="eyebrow">当前任务 · {statusLabel(task.status)}</div>
          <h1>{task.name || '未命名任务'}</h1>
          <p className="muted">
            {task.sourceFile || '未知源文件'}
            <span className="separator">·</span>
            {formatDuration(task.duration)}
            <span className="separator">·</span>
            {languageLabel(task.sourceLanguage)} → {languageLabel(task.targetLanguage)}
            <span className="separator">·</span>
            {task.model || task.engine || '未指定模型'}
          </p>
        </div>
        <div className="heading-actions">
          <StatusBadge tone={statusTone(task.status)}>{statusLabel(task.status)}</StatusBadge>
          <button
            type="button"
            className="button secondary"
            disabled={retranslateDisabled || task.subtitleCount === 0}
            title={retranslateTitle}
            onClick={() => void handleAction('retranslate')}
          >
            重新翻译
          </button>
          <button type="button" className="button primary" onClick={onNewTask} disabled={demo}>
            新建任务
          </button>
        </div>
      </div>

      <ErrorBar message={error} onDismiss={() => setError('')} />
      {saveHint ? <NoticeBar tone="success">{saveHint}</NoticeBar> : null}

      <ProgressCard task={task} busy={busy} demo={demo} onAction={(action) => void handleAction(action)} />

      <div className="export-bar card">
        <div className="export-bar-info">
          <strong>导出字幕</strong>
          <span>选择格式后可直接下载，或让后端弹出系统「另存为」对话框。</span>
        </div>
        <div className="export-bar-actions">
          <select className="select" value={exportFormat} onChange={(event) => setExportFormat(event.target.value as ExportFormat)}>
            {EXPORT_FORMATS.map((item) => (
              <option key={item.value} value={item.value}>
                {item.label}
              </option>
            ))}
          </select>
          {demo ? <span className="progress-controls-hint">演示模式不可导出</span> : null}
          <button type="button" className="button secondary" disabled={demo || exporting} onClick={() => void handleDownload()}>
            {exporting ? '处理中…' : '下载'}
          </button>
          <button type="button" className="button primary" disabled={demo || exporting} onClick={() => void handleSaveAs()}>
            另存为…
          </button>
        </div>
      </div>
      {exportHint ? <div className="inline-hint">{exportHint}</div> : null}

      <div className="workspace-grid">
        <SubtitleTable
          segments={segments}
          loading={segmentsLoading}
          demo={demo}
          saving={saving}
          translating={translating}
          dirty={dirty}
          saveHint=""
          onChange={handleSegmentChange}
          onSave={() => void handleSave()}
          onTranslatePending={() => void handleTranslatePending()}
          onReload={() => void loadSubtitles(task.id)}
        />
        <TaskDetail task={task} onOpenQueue={onOpenQueue} />
      </div>
    </div>
  );
}
