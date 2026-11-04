// 字幕表格：搜索、筛选、行内编辑译文、保存与「翻译待译条目」。
// 数据全部来自 ListSubtitles，编辑结果通过 onSave 交回父组件保存。
import { useMemo, useState } from 'react';
import type { SubtitleSegment } from '../../lib/api';
import { formatTimecode, translationStatusLabel } from '../../lib/format';
import { EmptyState, SegmentControl } from '../components/primitives';

type FilterMode = 'all' | 'translated' | 'pending';

/** 已翻译判定：状态为 translated，或已有非空译文 */
function isTranslated(segment: SubtitleSegment): boolean {
  return segment.translationStatus === 'translated' || Boolean((segment.translatedText ?? '').trim());
}

export interface SubtitleTableProps {
  segments: SubtitleSegment[];
  loading: boolean;
  demo: boolean;
  saving: boolean;
  translating: boolean;
  dirty: boolean;
  saveHint: string;
  onChange: (segmentId: string, text: string) => void;
  onSave: () => void;
  onTranslatePending: () => void;
  onReload: () => void;
}

export function SubtitleTable({
  segments,
  loading,
  demo,
  saving,
  translating,
  dirty,
  saveHint,
  onChange,
  onSave,
  onTranslatePending,
  onReload,
}: SubtitleTableProps) {
  const [keyword, setKeyword] = useState('');
  const [filter, setFilter] = useState<FilterMode>('all');
  const [editingId, setEditingId] = useState<string | null>(null);

  const translatedCount = useMemo(() => segments.filter(isTranslated).length, [segments]);
  const pendingCount = segments.length - translatedCount;

  const visible = useMemo(() => {
    const search = keyword.trim().toLowerCase();
    return segments.filter((segment) => {
      if (filter === 'translated' && !isTranslated(segment)) return false;
      if (filter === 'pending' && isTranslated(segment)) return false;
      if (!search) return true;
      return (
        (segment.sourceText ?? '').toLowerCase().includes(search) ||
        (segment.translatedText ?? '').toLowerCase().includes(search)
      );
    });
  }, [segments, filter, keyword]);

  const renderBody = () => {
    if (loading) {
      return (
        <div className="subtitle-empty">
          <span className="loading-dot" />
          正在读取字幕…
        </div>
      );
    }
    if (segments.length === 0) {
      return (
        <div className="subtitle-empty">
          <EmptyState
            icon="▤"
            title="还没有生成字幕"
            description={demo ? '演示模式下没有真实字幕数据。' : '识别完成后，逐条字幕与时间轴会显示在这里。'}
            actions={
              <button type="button" className="button secondary" onClick={onReload}>
                重新读取
              </button>
            }
          />
        </div>
      );
    }
    if (visible.length === 0) {
      return <div className="subtitle-empty filter-empty">没有符合当前筛选条件的字幕。</div>;
    }
    return visible.map((segment) => {
      const editing = editingId === segment.id;
      const status = isTranslated(segment) ? 'translated' : segment.translationStatus;
      const text = segment.translatedText ?? '';
      return (
        <div className={`subtitle-row ${editing ? 'editing' : ''}`} key={segment.id}>
          <span className="row-sequence">{segment.sequence}</span>
          <div className="timecode">
            <span>{formatTimecode(segment.start)}</span>
            <span>{formatTimecode(segment.end)}</span>
          </div>
          <div className="source-text">{segment.sourceText}</div>
          {editing ? (
            <textarea
              className="translation-textarea"
              autoFocus
              value={text}
              placeholder="输入译文…"
              onChange={(event) => onChange(segment.id, event.target.value)}
              onBlur={() => setEditingId(null)}
              onKeyDown={(event) => {
                if (event.key === 'Escape') setEditingId(null);
                if (event.key === 'Enter' && (event.ctrlKey || event.metaKey)) setEditingId(null);
              }}
            />
          ) : (
            <button
              type="button"
              className={`translation-text ${text ? '' : 'empty'}`}
              disabled={demo}
              onClick={() => setEditingId(segment.id)}
            >
              {text || '点击填写译文…'}
            </button>
          )}
          <div className={`segment-status ${status}`}>{translationStatusLabel(status)}</div>
        </div>
      );
    });
  };

  return (
    <section className="subtitle-card">
      <div className="card-header subtitle-card-header">
        <div>
          <h2>字幕</h2>
          <span className="card-subtitle">
            共 {segments.length} 条 · 已译 {translatedCount} · 待译 {pendingCount} · 点击译文即可编辑
          </span>
        </div>
        <div className="subtitle-tools">
          <label className="search">
            <span>⌕</span>
            <input value={keyword} placeholder="搜索原文或译文" onChange={(event) => setKeyword(event.target.value)} />
          </label>
          <SegmentControl<FilterMode>
            value={filter}
            onChange={setFilter}
            options={[
              { value: 'all', label: '全部', count: segments.length },
              { value: 'translated', label: '已译', count: translatedCount },
              { value: 'pending', label: '待译', count: pendingCount },
            ]}
          />
          <button
            type="button"
            className="button secondary small"
            disabled={demo || translating || pendingCount === 0}
            onClick={onTranslatePending}
          >
            {translating ? '翻译中…' : '翻译待译条目'}
          </button>
          <button type="button" className="button primary small" disabled={demo || saving || !dirty} onClick={onSave}>
            {saving ? '保存中…' : dirty ? '保存字幕 ●' : '保存字幕'}
          </button>
        </div>
      </div>

      {saveHint ? <div className="inline-hint">{saveHint}</div> : null}

      <div className="subtitle-table">
        <div className="table-head">
          <span>序号</span>
          <span>时间轴</span>
          <span>原文</span>
          <span>译文</span>
          <span>状态</span>
        </div>
        {renderBody()}
      </div>
    </section>
  );
}
