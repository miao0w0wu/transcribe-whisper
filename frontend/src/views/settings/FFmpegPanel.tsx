// FFmpegPanel：FFmpeg 的安装、下载与手动选择。
//
// 应用不再自动发现系统里的 FFmpeg，因此这里要承担全部「让用户用上 FFmpeg」的工作：
//   - 未安装时给醒目提示 + 一键下载完整版（带 libass，可压制字幕）；
//   - 已安装时展示版本与能力，允许改路径或清除；
//   - 下载进度通过 ffmpeg:progress 事件实时展示。
import { useCallback, useEffect, useState } from 'react';
import * as api from '../../lib/api';
import { subscribeFFmpegProgress } from '../../lib/events';
import type { FFmpegProgressEvent, FFmpegStatus } from '../../lib/types';
import { ErrorBar, NoticeBar, ProgressBar } from '../components/primitives';

export interface FFmpegPanelProps {
  demo: boolean;
  /** 状态变化后通知父组件（设置页需要更新表单里的 ffmpegPath） */
  onChanged?: (path: string) => void;
}

export function FFmpegPanel({ demo, onChanged }: FFmpegPanelProps) {
  const [status, setStatus] = useState<FFmpegStatus | null>(null);
  const [loading, setLoading] = useState(!demo);
  const [error, setError] = useState('');
  const [hint, setHint] = useState('');
  const [busy, setBusy] = useState(false);
  const [progress, setProgress] = useState<FFmpegProgressEvent | null>(null);
  // 下载源选择：默认跟随后端顺序（gyan 优先，失败自动降级）
  const [sourceId, setSourceId] = useState('');
  const [customDir, setCustomDir] = useState('');

  const load = useCallback(async () => {
    if (demo) {
      setLoading(false);
      return;
    }
    try {
      const next = await api.getFFmpegStatus();
      setStatus(next);
      setCustomDir(next.defaultDir ?? '');
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : '读取 FFmpeg 状态失败。');
    } finally {
      setLoading(false);
    }
  }, [demo]);

  useEffect(() => {
    void load();
  }, [load]);

  // 订阅下载进度；完成或失败时刷新状态。
  useEffect(() => {
    if (demo) return undefined;
    return subscribeFFmpegProgress((event) => {
      setProgress(event);
      if (event.done || event.error) {
        void load().then(() => {
          if (event.done && event.path) onChanged?.(event.path);
        });
      }
    });
  }, [demo, load, onChanged]);

  const handleDownload = async () => {
    if (demo) return;
    setBusy(true);
    setError('');
    setHint('');
    setProgress(null);
    try {
      await api.downloadFFmpeg({ dir: customDir.trim(), sourceId });
      setHint('已开始下载，进度会实时更新。完成后会自动启用，无需再点保存。');
    } catch (err) {
      setError(err instanceof Error ? err.message : '下载 FFmpeg 失败。');
    } finally {
      setBusy(false);
    }
  };

  const handlePickFile = async () => {
    if (demo) return;
    setError('');
    try {
      const path = await api.pickFFmpegFile();
      if (!path) return;
      const next = await api.setFFmpegPath(path);
      setStatus(next);
      setHint(next.ok ? `已启用：${next.path}` : next.detail);
      onChanged?.(next.path);
    } catch (err) {
      setError(err instanceof Error ? err.message : '设置 FFmpeg 失败。');
    }
  };

  const handlePickDir = async () => {
    if (demo) return;
    setError('');
    try {
      const dir = await api.pickFFmpegDirectory();
      if (!dir) return;
      // 选目录时先尝试直接启用（目录里已有 ffmpeg.exe），失败则作为下载目标。
      try {
        const next = await api.setFFmpegPath(dir);
        setStatus(next);
        setHint(`已启用：${next.path}`);
        onChanged?.(next.path);
      } catch {
        setCustomDir(dir);
        setHint('该目录里还没有 ffmpeg.exe，已作为下载目标；点「下载完整版」即可安装到此处。');
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : '选择目录失败。');
    }
  };

  const handleClear = async () => {
    if (demo) return;
    setError('');
    try {
      const next = await api.clearFFmpegPath();
      setStatus(next);
      setHint('已清除 FFmpeg 配置，相关功能将不可用。');
      onChanged?.('');
    } catch (err) {
      setError(err instanceof Error ? err.message : '清除失败。');
    }
  };

  if (loading) {
    return <div className="ffmpeg-panel card">正在读取 FFmpeg 状态…</div>;
  }

  const downloading = busy || Boolean(status?.downloading) || Boolean(progress && !progress.done && !progress.error);
  // 只要未安装或不支持压制，都算「需要处理」。
  const needsAttention = !status?.configured || !status?.ok || !status?.canBurn;

  return (
    <div className={`ffmpeg-panel card ${needsAttention ? 'needs-attention' : ''}`}>
      <div className="ffmpeg-head">
        <div>
          <h2>
            FFmpeg
            {status?.configured && status.ok ? (
              <span className={`ffmpeg-badge ${status.canBurn ? 'ok' : 'warn'}`}>
                {status.canBurn ? '已就绪' : '可用但无法压制'}
              </span>
            ) : (
              <span className="ffmpeg-badge missing">未安装</span>
            )}
          </h2>
          <p>{status?.detail || '音频提取、语音识别与字幕压制都依赖 FFmpeg。'}</p>
        </div>
      </div>

      {status?.configured && status.ok ? (
        <div className="ffmpeg-meta">
          <span>
            路径：<code>{status.path}</code>
          </span>
          {status.version ? <span>版本：{status.version}</span> : null}
          <span>字幕压制：{status.canBurn ? '支持' : '不支持（缺 subtitles 滤镜）'}</span>
        </div>
      ) : null}

      {/* 未安装时的醒目提示 */}
      {!status?.configured ? (
        <NoticeBar tone="warning">
          还没有安装 FFmpeg。请点下面的「下载完整版」自动安装到{' '}
          <code>{status?.defaultDir || '应用目录下的 ffmpeg'}</code>，或手动选择本机已有的 ffmpeg.exe。
          <br />
          下载的是带 libass 的完整构建，可以压制字幕；体积约 115MB，只需下载一次。
        </NoticeBar>
      ) : null}
      {status?.configured && status.ok && !status.canBurn ? (
        <NoticeBar tone="warning">
          当前 FFmpeg 缺少 subtitles 滤镜（未编译 libass），只能提取音频与识别，无法压制字幕。建议下载完整版替换。
        </NoticeBar>
      ) : null}
      {status?.configured && !status.ok ? (
        <NoticeBar tone="warning">配置的 FFmpeg 无法执行，请重新选择或重新下载。</NoticeBar>
      ) : null}

      <ErrorBar message={error} onDismiss={() => setError('')} />
      {hint ? <NoticeBar tone="success">{hint}</NoticeBar> : null}

      {/* 下载进度 */}
      {downloading ? (
        <div className="ffmpeg-download">
          <div className="ffmpeg-download-head">
            <strong>正在下载并安装…</strong>
            <span>{progress?.progress ?? 0}%</span>
          </div>
          <ProgressBar value={progress?.progress ?? 0} />
          <div className="ffmpeg-download-msg">{progress?.message || '正在准备…'}</div>
        </div>
      ) : null}
      {progress?.error ? <NoticeBar tone="warning">下载失败：{progress.error}</NoticeBar> : null}

      {/* 操作区 */}
      <div className="ffmpeg-actions">
        <div className="ffmpeg-dir-row">
          <label>
            <span>安装目录</span>
            <input
              className="text-input"
              value={customDir}
              placeholder="留空则使用默认目录"
              onChange={(event) => setCustomDir(event.target.value)}
              disabled={demo || downloading}
            />
          </label>
          <button type="button" className="button secondary" disabled={demo || downloading} onClick={() => void handlePickDir()}>
            选择目录…
          </button>
        </div>
        <div className="ffmpeg-dir-row">
          <label>
            <span>下载源</span>
            <select
              className="select"
              value={sourceId}
              onChange={(event) => setSourceId(event.target.value)}
              disabled={demo || downloading}
            >
              <option value="">自动（gyan.dev 优先，失败自动换源）</option>
              {(status?.sources ?? []).map((item) => (
                <option key={item.id} value={item.id}>
                  {item.label}
                  {item.note ? ` · ${item.note}` : ''}
                </option>
              ))}
            </select>
          </label>
        </div>
        <div className="ffmpeg-buttons">
          <button type="button" className="button primary" disabled={demo || downloading} onClick={() => void handleDownload()}>
            {downloading ? '下载中…' : status?.configured ? '重新下载完整版' : '下载完整版'}
          </button>
          <button type="button" className="button secondary" disabled={demo || downloading} onClick={() => void handlePickFile()}>
            选择已有 ffmpeg.exe…
          </button>
          {status?.configured ? (
            <button type="button" className="button secondary" disabled={demo || downloading} onClick={() => void handleClear()}>
              清除
            </button>
          ) : null}
        </div>
      </div>
    </div>
  );
}
