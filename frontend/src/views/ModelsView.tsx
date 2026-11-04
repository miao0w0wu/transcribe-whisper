// 模型管理视图：真实 ListModels 列表、下载/取消/删除、model:progress 实时进度，
// 以及顶部 GetEnvironment 的 FFmpeg / Python / faster-whisper / GPU 检测结果。
import { useCallback, useEffect, useMemo, useState } from 'react';
import * as api from '../lib/api';
import { subscribeModelProgress } from '../lib/events';
import { clampProgress, formatBytes } from '../lib/format';
import type { EnvironmentStatus, ModelInfo, ModelProgressEvent } from '../lib/types';
import { DemoNotice, EmptyState, ErrorBar, LoadingState, NoticeBar, ProgressBar } from './components/primitives';

export interface ModelsViewProps {
  demo: boolean;
  /** App 全局订阅到的模型进度事件，转发进来用于进度合并（视图内部也会自行订阅） */
  modelEvent: ModelProgressEvent | null;
}

/** 环境检测项 */
interface EnvItem {
  key: string;
  label: string;
  ok: boolean;
  detail: string;
}

function buildEnvItems(env: EnvironmentStatus | null): EnvItem[] {
  if (!env) return [];
  return [
    { key: 'ffmpeg', label: 'FFmpeg', ok: env.ffmpegOk, detail: env.ffmpegDetail || env.ffmpegPath || '未检测到' },
    { key: 'subtitle', label: 'FFmpeg 字幕滤镜', ok: env.ffmpegSubtitle, detail: env.ffmpegSubtitle ? '已编译 subtitles 滤镜' : '缺少 subtitles 滤镜，无法压制字幕' },
    { key: 'python', label: 'Python', ok: env.pythonOk, detail: env.pythonDetail || env.pythonPath || '未检测到' },
    { key: 'whisper', label: 'faster-whisper', ok: env.fasterWhisper, detail: env.fasterWhisper ? '已安装，可以进行识别' : '未安装，请先 pip install faster-whisper' },
    { key: 'gpu', label: 'GPU', ok: Boolean(env.gpu), detail: env.gpu || '未检测到可用显卡，将使用 CPU 推理' },
  ];
}

export function ModelsView({ demo, modelEvent }: ModelsViewProps) {
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [environment, setEnvironment] = useState<EnvironmentStatus | null>(null);
  const [envError, setEnvError] = useState('');
  const [loading, setLoading] = useState(!demo);
  const [error, setError] = useState('');
  const [hint, setHint] = useState('');
  const [busyId, setBusyId] = useState<string | null>(null);
  // model:progress 推送的临时进度，优先级高于 ListModels 的快照
  const [liveProgress, setLiveProgress] = useState<Record<string, ModelProgressEvent>>({});

  const loadModels = useCallback(async () => {
    if (demo) {
      setLoading(false);
      return;
    }
    try {
      const list = await api.listModels();
      setModels(list ?? []);
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : '读取模型列表失败，请稍后重试。');
    } finally {
      setLoading(false);
    }
  }, [demo]);

  const loadEnvironment = useCallback(async () => {
    if (demo) return;
    try {
      const env = await api.getEnvironment();
      setEnvironment(env);
      setEnvError('');
    } catch (err) {
      setEnvError(err instanceof Error ? err.message : '检测运行环境失败。');
    }
  }, [demo]);

  useEffect(() => {
    void loadModels();
    void loadEnvironment();
  }, [loadModels, loadEnvironment]);

  // 订阅模型下载进度，卸载时取消订阅
  useEffect(() => {
    if (demo) return undefined;
    const off = subscribeModelProgress((event) => {
      if (!event.modelId) return;
      setLiveProgress((prev) => ({ ...prev, [event.modelId]: event }));
      if (event.done || event.error) {
        // 结束或失败时以后端快照为准，同时清理临时进度
        void loadModels();
        setLiveProgress((prev) => {
          const next = { ...prev };
          delete next[event.modelId];
          return next;
        });
      }
    });
    return off;
  }, [demo, loadModels]);

  // App 层也订阅了同一事件，这里兜底合并，保证即使视图内部订阅失效也能刷新进度
  useEffect(() => {
    if (!modelEvent || !modelEvent.modelId) return;
    const { modelId } = modelEvent;
    setLiveProgress((prev) => ({ ...prev, [modelId]: modelEvent }));
    if (modelEvent.done || modelEvent.error) {
      void loadModels();
      setLiveProgress((prev) => {
        const next = { ...prev };
        delete next[modelId];
        return next;
      });
    }
  }, [modelEvent, loadModels]);

  const installedCount = useMemo(() => models.filter((model) => model.installed).length, [models]);
  const downloadingCount = useMemo(
    () => models.filter((model) => model.installing || Boolean(liveProgress[model.id])).length,
    [models, liveProgress],
  );

  const handleDownload = async (model: ModelInfo) => {
    if (demo) return;
    setBusyId(model.id);
    setError('');
    setHint('');
    try {
      await api.downloadModel(model.id);
      setHint(`已开始下载「${model.name || model.id}」，进度会实时更新。`);
      await loadModels();
    } catch (err) {
      setError(err instanceof Error ? err.message : '下载模型失败，请稍后重试。');
    } finally {
      setBusyId(null);
    }
  };

  const handleCancel = async (model: ModelInfo) => {
    if (demo) return;
    setBusyId(model.id);
    setError('');
    try {
      await api.cancelModelDownload(model.id);
      setHint(`已取消「${model.name || model.id}」的下载。`);
      await loadModels();
    } catch (err) {
      setError(err instanceof Error ? err.message : '取消下载失败，请稍后重试。');
    } finally {
      setBusyId(null);
    }
  };

  const handleRemove = async (model: ModelInfo) => {
    if (demo) return;
    if (!window.confirm(`确定删除本地模型「${model.name || model.id}」吗？删除后需要重新下载。`)) return;
    setBusyId(model.id);
    setError('');
    try {
      await api.removeModel(model.id);
      setHint(`已删除「${model.name || model.id}」。`);
      await loadModels();
    } catch (err) {
      setError(err instanceof Error ? err.message : '删除模型失败，请稍后重试。');
    } finally {
      setBusyId(null);
    }
  };

  const handleOpenFolder = async () => {
    if (demo) return;
    setError('');
    try {
      await api.openModelFolder();
    } catch (err) {
      setError(err instanceof Error ? err.message : '打开缓存目录失败，请稍后重试。');
    }
  };

  const envItems = buildEnvItems(environment);
  const envHealthy = envItems.every((item) => item.ok);

  return (
    <div className="page">
      <DemoNotice show={demo} />
      <div className="page-heading">
        <div>
          <div className="eyebrow">本地资源</div>
          <h1>模型管理</h1>
          <p className="muted">
            已安装 {installedCount} 个 · 下载中 {downloadingCount} 个 · 缓存目录 {environment?.modelDir || '默认目录'}
          </p>
        </div>
        <div className="heading-actions">
          <button type="button" className="button secondary" disabled={demo} onClick={() => void handleOpenFolder()}>
            打开缓存目录
          </button>
          <button
            type="button"
            className="button secondary"
            disabled={demo}
            onClick={() => {
              void loadModels();
              void loadEnvironment();
            }}
          >
            刷新
          </button>
        </div>
      </div>

      <ErrorBar message={error} onDismiss={() => setError('')} />
      {hint ? <NoticeBar tone="success">{hint}</NoticeBar> : null}

      <section className={`model-source card ${envHealthy ? '' : 'has-warning'}`}>
        <div className="env-grid">
          {envItems.length === 0 ? (
            <div className="env-item">
              <span className="env-label">运行环境</span>
              <strong className={envError ? 'env-bad' : ''}>{envError || (demo ? '演示模式下不进行检测' : '正在检测…')}</strong>
            </div>
          ) : (
            envItems.map((item) => (
              <div className="env-item" key={item.key}>
                <span className="env-label">
                  <i className={item.ok ? 'env-dot ok' : 'env-dot bad'} />
                  {item.label}
                </span>
                <strong className={item.ok ? 'env-good' : 'env-bad'} title={item.detail}>
                  {item.ok ? '正常' : '异常'} · {item.detail}
                </strong>
              </div>
            ))
          )}
        </div>
      </section>

      {loading ? (
        <LoadingState text="正在读取模型列表…" />
      ) : models.length === 0 ? (
        <EmptyState
          icon="◇"
          title="没有可用的模型"
          description={
            demo
              ? '演示模式不会显示虚构模型。请通过桌面客户端打开查看真实模型列表。'
              : '后端暂未返回可下载模型，请检查网络或镜像源设置。'
          }
          actions={
            <button type="button" className="button secondary" disabled={demo} onClick={() => void loadModels()}>
              重新读取
            </button>
          }
        />
      ) : (
        <section className="model-list card">
          <div className="card-header">
            <div>
              <h2>识别模型</h2>
              <span className="card-subtitle">下载进度通过 model:progress 事件实时更新</span>
            </div>
          </div>
          <div className="model-table-head">
            <span>模型</span>
            <span>大小</span>
            <span>状态</span>
            <span>操作</span>
          </div>
          {models.map((model) => {
            const live = liveProgress[model.id];
            const downloading = model.installing || Boolean(live) || (live ? !live.done : false);
            const progress = clampProgress(live?.progress ?? model.progress);
            const busy = busyId === model.id;
            return (
              <div className="model-row model-row-extended" key={model.id}>
                <div className="model-name-cell">
                  <strong>{model.name || model.id}</strong>
                  <small>{model.repo || model.id}</small>
                  {model.description ? <small className="model-desc">{model.description}</small> : null}
                  {live?.message ? <small className="model-desc">{live.message}</small> : null}
                </div>
                <span>{formatBytes(model.sizeBytes)}</span>
                <div className="model-status-cell">
                  {downloading ? (
                    <>
                      <span className="model-status downloading">下载中 {progress}%</span>
                      <ProgressBar value={progress} compact />
                    </>
                  ) : (
                    <span className={model.installed ? 'model-status default' : 'model-status missing'}>
                      {model.installed ? '已安装' : '未安装'}
                    </span>
                  )}
                </div>
                <div className="model-actions">
                  {downloading ? (
                    <button type="button" className="queue-action danger" disabled={demo || busy} onClick={() => void handleCancel(model)}>
                      取消
                    </button>
                  ) : (
                    <button type="button" className="queue-action" disabled={demo || busy} onClick={() => void handleDownload(model)}>
                      {model.installed ? '重新下载' : '下载'}
                    </button>
                  )}
                  <button
                    type="button"
                    className="queue-action danger"
                    disabled={demo || busy || (!model.installed && !downloading)}
                    onClick={() => void handleRemove(model)}
                  >
                    删除
                  </button>
                </div>
              </div>
            );
          })}
        </section>
      )}

      {environment?.modelDir ? (
        <div className="device-card card">
          <div>
            <h2>模型缓存</h2>
            <p>
              模型会保存在 <strong>{environment.modelDir}</strong>，删除前请确认没有任务正在使用。
            </p>
            <span>镜像源与 Hugging Face 端点可在「翻译设置」中调整。</span>
          </div>
          <button type="button" className="button secondary" disabled={demo} onClick={() => void handleOpenFolder()}>
            打开目录
          </button>
        </div>
      ) : null}
    </div>
  );
}
