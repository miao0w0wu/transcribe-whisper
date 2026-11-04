// 新建任务视图：选择本地音视频、配置识别与翻译参数，创建并启动任务。
//
// 文件路径说明：浏览器 <input type="file"> 出于安全限制只能拿到文件名，而后端需要本机绝对路径。
// 因此这里提供三条路：
// 1. 直接粘贴 / 输入绝对路径（最可靠）；
// 2. 「浏览…」：运行时存在 window.runtime.OpenFileDialog 时优先用它拿到绝对路径；
// 3. 不存在时退回 <input type="file">，把文件名回填到输入框并提示用户补全目录。
import { useCallback, useEffect, useState } from 'react';
import * as api from '../lib/api';
import { baseName, formatBytes } from '../lib/format';
import type { CreateTaskInput, EnvironmentStatus, ModelInfo, SettingsSnapshot } from '../lib/types';
import {
  ASR_DEVICES,
  ASR_ENGINES,
  COMPUTE_TYPES,
  EXPORT_FORMATS,
  SOURCE_LANGUAGES,
  TARGET_LANGUAGES,
  TRANSLATION_PROVIDERS,
} from '../lib/types';
import { DemoNotice, ErrorBar, FormField, NoticeBar } from './components/primitives';

/** window.runtime 上可能存在的文件对话框，使用前必须探测 */
interface NativeRuntime {
  OpenFileDialog?: (options?: Record<string, unknown>) => Promise<string | string[]>;
}

function getNativeOpenFileDialog(): ((options?: Record<string, unknown>) => Promise<string | string[]>) | undefined {
  if (typeof window === 'undefined') return undefined;
  const runtime = (window as unknown as { runtime?: NativeRuntime }).runtime;
  const dialog = runtime?.OpenFileDialog;
  return typeof dialog === 'function' ? dialog : undefined;
}

export interface NewTaskProps {
  demo: boolean;
  onCreated: (taskId: string) => void;
  onCancel: () => void;
}

interface FormState {
  sourceFile: string;
  sourceLanguage: string;
  targetLanguage: string;
  outputFormat: string;
  engine: string;
  model: string;
  translate: boolean;
  provider: string;
}

const INITIAL_FORM: FormState = {
  sourceFile: '',
  sourceLanguage: 'auto',
  targetLanguage: 'zh-CN',
  outputFormat: 'srt',
  engine: 'faster-whisper',
  model: '',
  translate: true,
  provider: 'ollama',
};

/** 校验表单，返回中文错误提示列表 */
function validate(form: FormState, models: ModelInfo[]): string[] {
  const errors: string[] = [];
  const path = form.sourceFile.trim();
  if (!path) {
    errors.push('请填写源文件的绝对路径，或点击「浏览…」选择文件。');
  } else if (!/[\\/]/.test(path)) {
    errors.push('源文件需要是本机绝对路径，例如 D:\\Videos\\demo.mp4。');
  }
  if (!form.model) {
    errors.push('请选择一个已安装的识别模型。');
  } else {
    const target = models.find((model) => model.id === form.model);
    if (target && !target.installed) {
      errors.push(`模型「${target.name}」尚未安装，请先到模型管理下载。`);
    }
  }
  if (form.translate && !form.provider) {
    errors.push('已开启翻译，请选择翻译服务提供方。');
  }
  return errors;
}

export function NewTask({ demo, onCreated, onCancel }: NewTaskProps) {
  const [form, setForm] = useState<FormState>(INITIAL_FORM);
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [environment, setEnvironment] = useState<EnvironmentStatus | null>(null);
  const [settings, setSettings] = useState<SettingsSnapshot | null>(null);
  const [loading, setLoading] = useState(!demo);
  const [pickedFile, setPickedFile] = useState<{ name: string; size: number } | null>(null);
  const [error, setError] = useState('');
  const [fieldErrors, setFieldErrors] = useState<string[]>([]);
  const [submitting, setSubmitting] = useState(false);

  const loadOptions = useCallback(async () => {
    if (demo) {
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      // 三个接口互不依赖，单个失败不阻塞表单
      const [modelList, env, snapshot] = await Promise.all([
        api.listModels().catch(() => [] as ModelInfo[]),
        api.getEnvironment().catch(() => null),
        api.getSettings().catch(() => null),
      ]);
      setModels(modelList);
      setEnvironment(env);
      setSettings(snapshot);
      const installed = modelList.filter((model) => model.installed);
      setForm((prev) => ({
        ...prev,
        model: prev.model || snapshot?.defaultModel || installed[0]?.id || '',
        engine: snapshot?.asrEngine || prev.engine,
        provider: snapshot?.translationProvider || prev.provider,
      }));
      if (modelList.length === 0) {
        setError('还没有可用的识别模型，请先到「模型管理」下载模型。');
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : '读取模型与环境信息失败。');
    } finally {
      setLoading(false);
    }
  }, [demo]);

  useEffect(() => {
    void loadOptions();
  }, [loadOptions]);

  const installedModels = models.filter((model) => model.installed);

  /** 用原生对话框（存在时）或 file input 兜底选择文件 */
  const handleBrowse = async () => {
    // 桌面端优先走后端 PickFile（Wails 的系统对话框），能直接拿到绝对路径。
    if (!demo) {
      try {
        const picked = await api.pickFile();
        if (picked) {
          setForm((prev) => ({ ...prev, sourceFile: picked }));
          setPickedFile({ name: baseName(picked), size: 0 });
          setFieldErrors([]);
          setError('');
        }
        return;
      } catch {
        // 后端不可用或不支持对话框时，继续尝试浏览器侧方案。
      }
    }
    const nativeDialog = getNativeOpenFileDialog();
    if (!nativeDialog) {
      const input = document.getElementById('new-task-file-input') as HTMLInputElement | null;
      input?.click();
      return;
    }
    try {
      const picked = await nativeDialog({
        Title: '选择音视频文件',
        Filters: [{ DisplayName: '音视频文件', Pattern: '*.mp4;*.mkv;*.avi;*.mov;*.webm;*.mp3;*.wav;*.m4a;*.flac' }],
      });
      const filePath = Array.isArray(picked) ? picked[0] : picked;
      if (filePath) {
        setForm((prev) => ({ ...prev, sourceFile: filePath }));
        setPickedFile({ name: baseName(filePath), size: 0 });
        setFieldErrors([]);
        setError('');
      }
    } catch (err) {
      setError(err instanceof Error ? err.message : '打开文件对话框失败，请改为手动填写路径。');
    }
  };

  const handleFilePicked = (fileList: FileList | null) => {
    const file = fileList?.[0];
    if (!file) return;
    setPickedFile({ name: file.name, size: file.size });
    const relative = (file as File & { webkitRelativePath?: string }).webkitRelativePath;
    // 浏览器拿不到绝对路径：把能拿到的信息回填，并提示用户补全目录
    setForm((prev) => ({ ...prev, sourceFile: relative || file.name }));
    setFieldErrors([]);
    setError('');
  };

  const handleSubmit = async () => {
    const validationErrors = validate(form, installedModels);
    setFieldErrors(validationErrors);
    if (validationErrors.length > 0) return;
    if (demo) {
      setError('演示模式无法创建任务，请通过桌面客户端打开。');
      return;
    }
    setSubmitting(true);
    setError('');
    try {
      const payload: CreateTaskInput = {
        sourceFile: form.sourceFile.trim(),
        sourceLanguage: form.sourceLanguage,
        targetLanguage: form.targetLanguage,
        outputFormat: form.outputFormat,
        engine: form.engine,
        model: form.model,
        translate: form.translate,
        provider: form.translate ? form.provider : '',
      };
      const created = await api.createTask(payload);
      const started = await api.startTask(created.id);
      onCreated(started.id);
    } catch (err) {
      setError(err instanceof Error ? err.message : '创建任务失败，请检查输入后重试。');
    } finally {
      setSubmitting(false);
    }
  };

  const modelHint =
    installedModels.length > 0
      ? `仅列出已安装模型（共 ${installedModels.length} 个）。`
      : demo
        ? '演示模式无法读取已安装模型。'
        : '还没有已安装的模型，请先到「模型管理」下载。';

  return (
    <div className="page narrow-page">
      <DemoNotice show={demo} />
      <div className="page-heading">
        <div>
          <div className="eyebrow">创建工作流</div>
          <h1>新建任务</h1>
          <p className="muted">导入本地音视频 · 全程本地处理，文件不会离开这台电脑</p>
        </div>
      </div>

      <ErrorBar message={error} onDismiss={() => setError('')} />
      {fieldErrors.length > 0 ? (
        <NoticeBar tone="warning">
          <ul className="validation-list">
            {fieldErrors.map((item) => (
              <li key={item}>{item}</li>
            ))}
          </ul>
        </NoticeBar>
      ) : null}

      <section className="form-card card">
        <div className="dropzone">
          <div className="upload-icon">↑</div>
          <strong>选择要识别的音视频文件</strong>
          <button type="button" className="button secondary" onClick={() => void handleBrowse()}>
            浏览…
          </button>
          <input
            id="new-task-file-input"
            type="file"
            accept="audio/*,video/*"
            onChange={(event) => handleFilePicked(event.target.files)}
          />
          <small>MP4 · MKV · AVI · MOV · WebM · MP3 · WAV · M4A · FLAC</small>
        </div>

        <FormField
          label="源文件绝对路径"
          hint={
            pickedFile
              ? `已选择 ${pickedFile.name}${pickedFile.size ? `（${formatBytes(pickedFile.size)}）` : ''}：浏览器只能拿到文件名，请补全目录后得到本机绝对路径。`
              : '请填写本机绝对路径，例如 D:\\Videos\\lecture.mp4。'
          }
        >
          <input
            className="text-input"
            value={form.sourceFile}
            placeholder="D:\\Videos\\lecture.mp4"
            onChange={(event) => setForm((prev) => ({ ...prev, sourceFile: event.target.value }))}
          />
        </FormField>

        <div className="form-section">
          <h3>识别设置</h3>
          <div className="form-grid">
            <FormField label="识别模型" hint={modelHint}>
              <select
                className="select"
                value={form.model}
                disabled={demo || loading}
                onChange={(event) => setForm((prev) => ({ ...prev, model: event.target.value }))}
              >
                <option value="">{loading ? '加载中…' : '请选择已安装模型'}</option>
                {installedModels.map((model) => (
                  <option key={model.id} value={model.id}>
                    {model.name || model.id} · {formatBytes(model.sizeBytes)}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="识别引擎">
              <select className="select" value={form.engine} onChange={(event) => setForm((prev) => ({ ...prev, engine: event.target.value }))}>
                {ASR_ENGINES.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="源语言">
              <select
                className="select"
                value={form.sourceLanguage}
                onChange={(event) => setForm((prev) => ({ ...prev, sourceLanguage: event.target.value }))}
              >
                {SOURCE_LANGUAGES.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="目标语言">
              <select
                className="select"
                value={form.targetLanguage}
                onChange={(event) => setForm((prev) => ({ ...prev, targetLanguage: event.target.value }))}
              >
                {TARGET_LANGUAGES.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="运行设备">
              <select className="select" value={settings?.asrDevice || 'auto'} disabled>
                {ASR_DEVICES.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="计算精度">
              <select className="select" value={settings?.computeType || 'auto'} disabled>
                {COMPUTE_TYPES.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </FormField>
          </div>
          <p className="form-footnote">运行设备与计算精度跟随「翻译设置」页的全局配置，可在那里修改。</p>
        </div>

        <div className="form-section">
          <h3>翻译与输出</h3>
          <div className="mode-row">
            <div>
              <strong>识别后自动翻译</strong>
              <span>关闭时只生成原文字幕，之后可在工作台手动翻译。</span>
            </div>
            <button
              type="button"
              className={`toggle ${form.translate ? 'on' : ''}`}
              aria-pressed={form.translate}
              onClick={() => setForm((prev) => ({ ...prev, translate: !prev.translate }))}
            >
              <i />
            </button>
          </div>
          <div className="form-grid form-grid-spaced">
            <FormField label="翻译服务">
              <select
                className="select"
                value={form.provider}
                disabled={!form.translate}
                onChange={(event) => setForm((prev) => ({ ...prev, provider: event.target.value }))}
              >
                {TRANSLATION_PROVIDERS.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label} — {item.hint}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="输出格式">
              <select
                className="select"
                value={form.outputFormat}
                onChange={(event) => setForm((prev) => ({ ...prev, outputFormat: event.target.value }))}
              >
                {EXPORT_FORMATS.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </FormField>
          </div>
        </div>

        {environment ? (
          <div className="estimate">
            <div>
              <span>FFmpeg</span>
              <strong className={environment.ffmpegOk ? '' : 'estimate-bad'}>{environment.ffmpegOk ? '已就绪' : '未检测到'}</strong>
            </div>
            <div>
              <span>faster-whisper</span>
              <strong className={environment.fasterWhisper ? '' : 'estimate-bad'}>{environment.fasterWhisper ? '已就绪' : '未安装'}</strong>
            </div>
            <div>
              <span>运行设备</span>
              <strong className="estimate-ellipsis" title={environment.gpu || '未检测到 GPU'}>
                {environment.gpu || '未检测到 GPU'}
              </strong>
            </div>
            <div>
              <span>模型目录</span>
              <strong className="estimate-ellipsis" title={environment.modelDir}>
                {environment.modelDir || '默认目录'}
              </strong>
            </div>
          </div>
        ) : null}

        <div className="privacy-note">ⓘ 音视频与字幕全程在本机处理；翻译选择云端服务时，请先在「翻译设置」中确认云隐私开关。</div>

        <div className="form-actions">
          <button type="button" className="button secondary" onClick={onCancel}>
            取消
          </button>
          <button type="button" className="button primary" disabled={submitting || demo} onClick={() => void handleSubmit()}>
            {submitting ? '正在创建…' : '开始识别 →'}
          </button>
        </div>
      </section>
    </div>
  );
}
