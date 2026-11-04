// 翻译设置视图：读写 GetSettings / UpdateSettings，包含翻译服务、识别引擎与模型目录。
import { useCallback, useEffect, useState } from 'react';
import * as api from '../lib/api';
import type { ModelInfo, SettingsPatch, SettingsSnapshot } from '../lib/types';
import { ASR_DEVICES, ASR_ENGINES, COMPUTE_TYPES, TRANSLATION_PROVIDERS, TRANSLATION_STYLES } from '../lib/types';
import { DemoNotice, ErrorBar, FormField, LoadingState, NoticeBar } from './components/primitives';

export interface SettingsViewProps {
  demo: boolean;
}

/** 表单编辑态：API Key 单独维护，留空表示不修改 */
interface SettingsForm {
  ffmpegPath: string;
  pythonPath: string;
  modelDir: string;
  mirror: string;
  huggingFaceEndpoint: string;
  asrEngine: string;
  asrDevice: string;
  computeType: string;
  defaultModel: string;
  translationProvider: string;
  translationEndpoint: string;
  translationModel: string;
  cloudPrivacyConfirm: boolean;
  translationStyle: string;
  glossaryText: string;
}

function toForm(snapshot: SettingsSnapshot): SettingsForm {
  return {
    ffmpegPath: snapshot.ffmpegPath ?? '',
    pythonPath: snapshot.pythonPath ?? '',
    modelDir: snapshot.modelDir ?? '',
    mirror: snapshot.mirror ?? '',
    huggingFaceEndpoint: snapshot.huggingFaceEndpoint ?? '',
    asrEngine: snapshot.asrEngine ?? 'faster-whisper',
    asrDevice: snapshot.asrDevice ?? 'auto',
    computeType: snapshot.computeType ?? 'auto',
    defaultModel: snapshot.defaultModel ?? '',
    translationProvider: snapshot.translationProvider ?? 'ollama',
    translationEndpoint: snapshot.translationEndpoint ?? '',
    translationModel: snapshot.translationModel ?? '',
    cloudPrivacyConfirm: Boolean(snapshot.cloudPrivacyConfirm),
    translationStyle: snapshot.translationStyle || 'subtitle',
    glossaryText: snapshot.glossaryText ?? '',
  };
}

/** 比较表单与快照，生成只包含改动字段的 Patch */
function buildPatch(form: SettingsForm, snapshot: SettingsSnapshot): SettingsPatch {
  const patch: SettingsPatch = {};
  const base = toForm(snapshot);
  if (form.ffmpegPath !== base.ffmpegPath) patch.ffmpegPath = form.ffmpegPath.trim();
  if (form.pythonPath !== base.pythonPath) patch.pythonPath = form.pythonPath.trim();
  if (form.modelDir !== base.modelDir) patch.modelDir = form.modelDir.trim();
  if (form.mirror !== base.mirror) patch.mirror = form.mirror;
  if (form.huggingFaceEndpoint !== base.huggingFaceEndpoint) patch.huggingFaceEndpoint = form.huggingFaceEndpoint.trim();
  if (form.asrEngine !== base.asrEngine) patch.asrEngine = form.asrEngine;
  if (form.asrDevice !== base.asrDevice) patch.asrDevice = form.asrDevice;
  if (form.computeType !== base.computeType) patch.computeType = form.computeType;
  if (form.defaultModel !== base.defaultModel) patch.defaultModel = form.defaultModel;
  if (form.translationProvider !== base.translationProvider) patch.translationProvider = form.translationProvider;
  if (form.translationEndpoint !== base.translationEndpoint) patch.translationEndpoint = form.translationEndpoint.trim();
  if (form.translationModel !== base.translationModel) patch.translationModel = form.translationModel.trim();
  if (form.cloudPrivacyConfirm !== base.cloudPrivacyConfirm) patch.cloudPrivacyConfirm = form.cloudPrivacyConfirm;
  if (form.translationStyle !== base.translationStyle) patch.translationStyle = form.translationStyle;
  if (form.glossaryText !== base.glossaryText) patch.glossaryText = form.glossaryText;
  return patch;
}

export function SettingsView({ demo }: SettingsViewProps) {
  const [snapshot, setSnapshot] = useState<SettingsSnapshot | null>(null);
  const [form, setForm] = useState<SettingsForm | null>(null);
  const [models, setModels] = useState<ModelInfo[]>([]);
  const [loading, setLoading] = useState(!demo);
  const [saving, setSaving] = useState(false);
  const [testing, setTesting] = useState(false);
  const [error, setError] = useState('');
  const [hint, setHint] = useState('');
  const [apiKeyInput, setApiKeyInput] = useState('');
  const [testResult, setTestResult] = useState<{ ok: boolean; message: string } | null>(null);

  const load = useCallback(async () => {
    if (demo) {
      setLoading(false);
      return;
    }
    setLoading(true);
    try {
      const [loaded, modelList] = await Promise.all([
        api.getSettings(),
        api.listModels().catch(() => [] as ModelInfo[]),
      ]);
      setSnapshot(loaded);
      setForm(toForm(loaded));
      setModels(modelList);
      setError('');
    } catch (err) {
      setError(err instanceof Error ? err.message : '读取设置失败，请稍后重试。');
    } finally {
      setLoading(false);
    }
  }, [demo]);

  useEffect(() => {
    void load();
  }, [load]);

  const patchForm = (partial: Partial<SettingsForm>) => {
    setForm((prev) => (prev ? { ...prev, ...partial } : prev));
    setHint('');
  };

  const handleSave = async () => {
    if (!form || !snapshot || demo) return;
    // 先保存云端隐私开关，再校验：这样「勾选后立即保存」也能一次成功
    const patch = buildPatch(form, snapshot);
    if (apiKeyInput.trim()) patch.translationApiKey = apiKeyInput.trim();
    if (form.translationProvider === 'openai' && !form.cloudPrivacyConfirm) {
      setError('使用云端翻译前，请先勾选「我理解字幕文本会上传到云端服务」并保存。');
      return;
    }
    if (Object.keys(patch).length === 0) {
      setHint('没有需要保存的修改。');
      return;
    }
    setSaving(true);
    setError('');
    setHint('');
    try {
      const updated = await api.updateSettings(patch);
      setSnapshot(updated);
      setForm(toForm(updated));
      setApiKeyInput('');
      setHint('设置已保存。');
    } catch (err) {
      setError(err instanceof Error ? err.message : '保存设置失败，请稍后重试。');
    } finally {
      setSaving(false);
    }
  };

  const handleClearApiKey = async () => {
    if (demo) return;
    setSaving(true);
    setError('');
    try {
      const updated = await api.updateSettings({ translationApiKey: '' });
      setSnapshot(updated);
      setForm(toForm(updated));
      setApiKeyInput('');
      setHint('已清除已保存的 API Key。');
    } catch (err) {
      setError(err instanceof Error ? err.message : '清除 API Key 失败，请稍后重试。');
    } finally {
      setSaving(false);
    }
  };

  const handleTest = async () => {
    if (demo) return;
    setTesting(true);
    setTestResult(null);
    setError('');
    try {
      const message = await api.testTranslationProvider();
      setTestResult({ ok: true, message: message || '翻译服务连接成功。' });
    } catch (err) {
      setTestResult({ ok: false, message: err instanceof Error ? err.message : '测试翻译服务失败。' });
    } finally {
      setTesting(false);
    }
  };

  if (loading) {
    return (
      <div className="page narrow-page">
        <LoadingState text="正在读取设置…" />
      </div>
    );
  }

  if (!form) {
    return (
      <div className="page narrow-page">
        <DemoNotice show={demo} />
        <div className="page-heading">
          <div>
            <div className="eyebrow">应用配置</div>
            <h1>翻译设置</h1>
          </div>
        </div>
        <ErrorBar message={error} onDismiss={() => setError('')} />
        <NoticeBar tone="warning">
          演示模式下无法读取真实设置。请通过 <code>wails dev</code> 或桌面客户端打开以编辑配置。
        </NoticeBar>
      </div>
    );
  }

  const isCloud = form.translationProvider === 'openai';
  const providerMeta = TRANSLATION_PROVIDERS.find((item) => item.value === form.translationProvider);

  return (
    <div className="page narrow-page">
      <DemoNotice show={demo} />
      <div className="page-heading">
        <div>
          <div className="eyebrow">应用配置</div>
          <h1>翻译设置</h1>
          <p className="muted">翻译服务 · 识别引擎 · 模型与镜像</p>
        </div>
        <div className="heading-actions">
          <button type="button" className="button secondary" disabled={demo || testing} onClick={() => void handleTest()}>
            {testing ? '测试中…' : '测试连接'}
          </button>
          <button type="button" className="button primary" disabled={demo || saving} onClick={() => void handleSave()}>
            {saving ? '保存中…' : '保存更改'}
          </button>
        </div>
      </div>

      <ErrorBar message={error} onDismiss={() => setError('')} />
      {hint ? <NoticeBar tone="success">{hint}</NoticeBar> : null}
      {testResult ? (
        <NoticeBar tone={testResult.ok ? 'success' : 'warning'}>
          {testResult.ok ? '✓ ' : '✕ '}
          {testResult.message}
        </NoticeBar>
      ) : null}

      <section className="settings-card card">
        <div className="settings-section">
          <h3>翻译服务</h3>
          <div className="provider-picker">
            {TRANSLATION_PROVIDERS.map((item) => (
              <button
                key={item.value}
                type="button"
                className={form.translationProvider === item.value ? 'provider selected' : 'provider muted-provider'}
                onClick={() => patchForm({ translationProvider: item.value })}
              >
                <span className="provider-logo">{item.value === 'ollama' ? 'O' : '◈'}</span>
                <div>
                  <strong>
                    {item.label}
                    <small>{item.value === 'ollama' ? '本地' : '云端'}</small>
                  </strong>
                  <span>{item.hint}</span>
                </div>
                <i>{form.translationProvider === item.value ? '✓' : '›'}</i>
              </button>
            ))}
          </div>

          <div className="form-grid form-grid-spaced">
            <FormField label="服务端点" hint={isCloud ? '例如 https://api.deepseek.com/v1' : '例如 http://127.0.0.1:11434'}>
              <input
                className="text-input"
                value={form.translationEndpoint}
                placeholder={isCloud ? 'https://api.example.com/v1' : 'http://127.0.0.1:11434'}
                onChange={(event) => patchForm({ translationEndpoint: event.target.value })}
              />
            </FormField>
            <FormField label="翻译模型" hint={isCloud ? '例如 deepseek-chat' : '例如 qwen2.5:7b'}>
              <input
                className="text-input"
                value={form.translationModel}
                placeholder={isCloud ? 'deepseek-chat' : 'qwen2.5:7b'}
                onChange={(event) => patchForm({ translationModel: event.target.value })}
              />
            </FormField>
            <FormField
              label="API Key"
              hint={
                snapshot?.translationApiKeySet
                  ? '已配置 API Key：输入新值才会覆盖，留空表示保持不变。'
                  : isCloud
                    ? '尚未配置 API Key。'
                    : 'Ollama 本地服务通常不需要 API Key。'
              }
            >
              <div className="input-with-action">
                <input
                  className="text-input"
                  type="password"
                  value={apiKeyInput}
                  autoComplete="off"
                  placeholder={snapshot?.translationApiKeySet ? '••••••••（已配置）' : '留空表示不修改'}
                  onChange={(event) => setApiKeyInput(event.target.value)}
                />
                <button
                  type="button"
                  className="button secondary small"
                  disabled={demo || saving || !snapshot?.translationApiKeySet}
                  onClick={() => void handleClearApiKey()}
                >
                  清除
                </button>
              </div>
            </FormField>
            <FormField label="当前配置概览">
              <div className="static-value">
                {providerMeta?.label ?? '未选择'}
                <span className="separator">·</span>
                {form.translationModel || '未指定模型'}
              </div>
            </FormField>
          </div>

          <div className="form-grid form-grid-spaced">
            <FormField label="翻译语气" hint="影响译文的正式程度，字幕场景建议用「影视字幕」。">
              <select
                className="select"
                value={form.translationStyle}
                onChange={(event) => patchForm({ translationStyle: event.target.value })}
              >
                {TRANSLATION_STYLES.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </FormField>
          </div>

          <FormField
            label="术语表"
            hint="每行一条，格式：源术语=目标译法。例如 model=模型。以 # 开头的行会被忽略。"
          >
            <textarea
              className="text-input text-area"
              rows={6}
              spellCheck={false}
              value={form.glossaryText}
              placeholder={'API=接口\nLLM=大语言模型'}
              onChange={(event) => patchForm({ glossaryText: event.target.value })}
            />
          </FormField>

          <div className="mode-row mode-row-spaced">
            <div>
              <strong>我理解字幕文本会上传到云端服务</strong>
              <span>开启云端 Provider 时必须确认，否则不会发起任何网络请求。</span>
            </div>
            <button
              type="button"
              className={`toggle ${form.cloudPrivacyConfirm ? 'on' : ''}`}
              aria-pressed={form.cloudPrivacyConfirm}
              onClick={() => patchForm({ cloudPrivacyConfirm: !form.cloudPrivacyConfirm })}
            >
              <i />
            </button>
          </div>
        </div>

        <div className="settings-section">
          <h3>识别引擎</h3>
          <div className="form-grid">
            <FormField label="识别引擎">
              <select className="select" value={form.asrEngine} onChange={(event) => patchForm({ asrEngine: event.target.value })}>
                {ASR_ENGINES.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="运行设备">
              <select className="select" value={form.asrDevice} onChange={(event) => patchForm({ asrDevice: event.target.value })}>
                {ASR_DEVICES.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="计算精度">
              <select className="select" value={form.computeType} onChange={(event) => patchForm({ computeType: event.target.value })}>
                {COMPUTE_TYPES.map((item) => (
                  <option key={item.value} value={item.value}>
                    {item.label}
                  </option>
                ))}
              </select>
            </FormField>
            <FormField label="默认识别模型" hint="新建任务时默认选中的模型">
              <select className="select" value={form.defaultModel} onChange={(event) => patchForm({ defaultModel: event.target.value })}>
                <option value="">跟随模型管理中的首个已安装模型</option>
                {models.map((model) => (
                  <option key={model.id} value={model.id} disabled={!model.installed}>
                    {model.name || model.id}
                    {model.installed ? '' : '（未安装）'}
                  </option>
                ))}
              </select>
            </FormField>
          </div>
        </div>

        <div className="settings-section">
          <h3>运行环境与模型目录</h3>
          <div className="form-grid">
            <FormField label="FFmpeg 路径" hint="留空则在系统 PATH 中查找">
              <input
                className="text-input"
                value={form.ffmpegPath}
                placeholder="C:\\ffmpeg\\bin\\ffmpeg.exe"
                onChange={(event) => patchForm({ ffmpegPath: event.target.value })}
              />
            </FormField>
            <FormField label="Python 路径" hint="faster-whisper 所需的 Python 解释器">
              <input
                className="text-input"
                value={form.pythonPath}
                placeholder="C:\\Python311\\python.exe"
                onChange={(event) => patchForm({ pythonPath: event.target.value })}
              />
            </FormField>
            <FormField label="模型目录" hint="下载的模型缓存位置">
              <input
                className="text-input"
                value={form.modelDir}
                placeholder="D:\\AI\\Models"
                onChange={(event) => patchForm({ modelDir: event.target.value })}
              />
            </FormField>
            <FormField label="下载镜像">
              <select className="select" value={form.mirror} onChange={(event) => patchForm({ mirror: event.target.value })}>
                <option value="">自动（跟随网络情况）</option>
                <option value="hf-mirror">hf-mirror.com 国内镜像</option>
                <option value="huggingface">Hugging Face 官方源</option>
              </select>
            </FormField>
            <FormField label="Hugging Face Endpoint" hint="自定义下载端点，留空使用默认值">
              <input
                className="text-input"
                value={form.huggingFaceEndpoint}
                placeholder="https://hf-mirror.com"
                onChange={(event) => patchForm({ huggingFaceEndpoint: event.target.value })}
              />
            </FormField>
          </div>
        </div>

        <div className="form-actions form-actions-padded">
          <button type="button" className="button secondary" disabled={demo || loading} onClick={() => void load()}>
            重新读取
          </button>
          <button type="button" className="button primary" disabled={demo || saving} onClick={() => void handleSave()}>
            {saving ? '保存中…' : '保存更改'}
          </button>
        </div>
      </section>
    </div>
  );
}
