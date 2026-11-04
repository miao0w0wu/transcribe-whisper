// 任务详情卡：展示任务的源文件、语言对、引擎/模型、输出格式等真实字段。
import type { Task } from '../../lib/api';
import {
  exportFormatLabel,
  formatDateTime,
  formatDuration,
  formatRelative,
  languageLabel,
  statusLabel,
  statusTone,
} from '../../lib/format';
import { DetailRow, StatusBadge } from '../components/primitives';

/** 文件扩展名，用于详情卡图标 */
function fileExtension(path: string): string {
  const match = path.match(/\.([a-zA-Z0-9]{1,5})$/);
  return match ? match[1].toUpperCase() : 'FILE';
}

export function TaskDetail({ task, onOpenQueue }: { task: Task; onOpenQueue: () => void }) {
  return (
    <aside className="task-details card">
      <div className="card-header">
        <div>
          <h2>任务信息</h2>
          <span className="card-subtitle">详情快照</span>
        </div>
        <StatusBadge tone={statusTone(task.status)}>{statusLabel(task.status)}</StatusBadge>
      </div>

      <div className="preview">
        <div className="preview-screen">
          <span>{fileExtension(task.sourceFile)}</span>
          <small>{formatDuration(task.duration)}</small>
        </div>
        <div className="preview-caption">
          <span>源文件</span>
          <strong>{task.name || '未命名任务'}</strong>
        </div>
      </div>

      <DetailRow label="源文件" value={<span className="detail-path" title={task.sourceFile}>{task.sourceFile || '—'}</span>} />
      <DetailRow label="时长" value={formatDuration(task.duration)} />
      <DetailRow label="语言" value={`${languageLabel(task.sourceLanguage)} → ${languageLabel(task.targetLanguage)}`} />
      <DetailRow label="识别引擎" value={task.engine || '—'} />
      <DetailRow label="模型" value={task.model || '—'} />
      <DetailRow label="翻译服务" value={task.provider || '未启用'} />
      <DetailRow label="输出格式" value={exportFormatLabel(task.outputFormat || 'srt')} />
      <DetailRow label="创建时间" value={formatDateTime(task.createdAt)} />
      <DetailRow label="更新时间" value={formatRelative(task.updatedAt || task.createdAt)} />

      <div className="detail-stats">
        <div>
          <strong>字幕条数</strong>
          <span>{task.subtitleCount}</span>
        </div>
        <div>
          <strong>已翻译</strong>
          <span>{task.translatedCount}</span>
        </div>
        <div>
          <strong>进度</strong>
          <span>{Math.min(100, Math.max(0, Math.round(task.progress || 0)))}%</span>
        </div>
      </div>

      <div className="detail-actions">
        <button type="button" className="button secondary small" onClick={onOpenQueue}>
          查看任务队列
        </button>
      </div>
    </aside>
  );
}
