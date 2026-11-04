import { ChangeEvent, useEffect, useMemo, useState } from 'react';
import './App.css';
import { CreateTask, ExportSubtitles, GetDashboard, StartTask } from '../wailsjs/go/main/App';
import { main } from '../wailsjs/go/models';

type View = 'workspace' | 'new-task' | 'settings' | 'models';

type Segment = {
  id: number;
  start: string;
  end: string;
  source: string;
  translation: string;
  status: 'translated' | 'translating' | 'pending';
};

const segments: Segment[] = [
  { id: 1, start: '00:12:35', end: '00:17:82', source: 'Welcome to this tutorial.', translation: '欢迎来到本教程。', status: 'translated' },
  { id: 2, start: '00:18:04', end: '00:23:60', source: 'Today we are going to talk about running large language models on your own machine.', translation: '今天我们来聊聊，如何在自己的电脑上运行大型语言模型。', status: 'translated' },
  { id: 3, start: '00:23:88', end: '00:29:15', source: 'No cloud, no API keys, and nothing ever leaves your computer.', translation: '不需要云端，也不需要 API Key，任何数据都不会离开你的电脑。', status: 'translated' },
  { id: 4, start: '00:29:40', end: '00:35:02', source: 'The first step is to check whether your GPU is supported.', translation: '第一步，是确认你的显卡是否在支持列表里。', status: 'translated' },
  { id: 5, start: '00:35:24', end: '00:41:76', source: 'If you have a modern NVIDIA card, you are in very good shape.', translation: '如果你用的是较新的 NVIDIA 显卡，那基本没问题。', status: 'translated' },
  { id: 6, start: '00:42:02', end: '00:47:31', source: 'Let us start by installing the runtime and pulling a model.', translation: '我们先安装运行时，然后拉取一个模型。', status: 'translating' },
  { id: 7, start: '00:47:58', end: '00:53:10', source: 'This download is about four gigabytes, so grab a coffee.', translation: '', status: 'pending' },
];

const navItems: { id: View; label: string; icon: string }[] = [
  { id: 'new-task', label: '新建任务', icon: '+' },
  { id: 'workspace', label: '工作台', icon: '▦' },
  { id: 'workspace', label: '任务队列', icon: '≡' },
  { id: 'models', label: '模型管理', icon: '◇' },
  { id: 'settings', label: '设置', icon: '⚙' },
];

function App() {
  const [view, setView] = useState<View>('workspace');
  const [fileName, setFileName] = useState('Britney_Speech_2026.mp4');
  const [saved, setSaved] = useState(false);
  const [activeTask, setActiveTask] = useState<main.Task | null>(null);
  const [error, setError] = useState('');

  useEffect(() => {
    GetDashboard()
      .then((dashboard) => setActiveTask(dashboard.activeTask ?? null))
      .catch(() => setError('无法读取任务状态，请重启应用后重试。'));
  }, []);

  const selectFile = (event: ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    if (file) setFileName(file.name);
  };

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand"><span className="brand-mark">◒</span><span>transcribe</span><b>·</b><span>whisper</span></div>
        <button className="new-task-button" onClick={() => setView('new-task')}><span>＋</span> 新建任务</button>
        <nav className="main-nav">
          {navItems.map((item, index) => (
            <button key={`${item.label}-${index}`} className={view === item.id && (item.id !== 'workspace' || index === 1) ? 'nav-item active' : 'nav-item'} onClick={() => setView(item.id)}>
              <span className="nav-icon">{item.icon}</span>{item.label}
              {item.label === '任务队列' && <span className="nav-count">3</span>}
            </button>
          ))}
        </nav>
        <div className="sidebar-section-label">最近项目</div>
        <div className="recent-project active"><span className="project-dot blue" /><div><strong>Britney_Speech_2026</strong><small>翻译中 · 68%</small></div></div>
        <div className="recent-project"><span className="project-dot green" /><div><strong>Podcast_EP12_访谈</strong><small>已完成 · 双语 SRT</small></div></div>
        <div className="recent-project"><span className="project-dot orange" /><div><strong>纪录片_海洋_EP03</strong><small>待校对 · 128 条</small></div></div>
        <div className="sidebar-footer"><div className="service-status"><span className="status-pulse" /><div><strong>本地服务已就绪</strong><small>127.0.0.1:54172 · 令牌已注入</small></div></div><div className="device">RTX 4070 Laptop <span>· 8 GB 显存</span></div></div>
      </aside>

      <main className="content">
        <header className="topbar"><div className="breadcrumbs">项目 <span>/</span> <b>{view === 'workspace' ? 'Britney_Speech_2026' : view === 'new-task' ? '新建任务' : view === 'models' ? '模型管理' : '翻译设置'}</b></div><div className="top-actions"><button className="icon-button">?</button><div className="avatar">Z</div></div></header>
        {error && <div className="app-error">⚠ {error}</div>}
        {view === 'workspace' && <Workspace task={activeTask} onNewTask={() => setView('new-task')} onExport={async () => {
          if (!activeTask) {
            setError('当前没有可导出的任务。');
            return;
          }
          try {
            const content = await ExportSubtitles(activeTask.id, 'srt');
            const blob = new Blob([content], { type: 'text/plain;charset=utf-8' });
            const url = URL.createObjectURL(blob);
            const link = document.createElement('a');
            link.href = url;
            link.download = `${activeTask.name}.srt`;
            link.click();
            URL.revokeObjectURL(url);
          } catch {
            setError('字幕导出失败，请稍后重试。');
          }
        }} />}
        {view === 'new-task' && <NewTask fileName={fileName} onFileChange={selectFile} onCancel={() => setView('workspace')} onCreate={async () => {
          try {
            const task = await CreateTask({ sourceFile: fileName, sourceLanguage: 'auto', targetLanguage: 'zh-CN', outputFormat: 'srt' });
            const started = await StartTask(task.id);
            setActiveTask(started);
            setView('workspace');
          } catch {
            setError('创建任务失败，请检查输入文件后重试。');
          }
        }} />}
        {view === 'settings' && <Settings saved={saved} onSave={() => setSaved(true)} />}
        {view === 'models' && <Models />}
      </main>
    </div>
  );
}

function Workspace({ task, onNewTask, onExport }: { task: main.Task | null; onNewTask: () => void; onExport: () => void }) {
  const translated = useMemo(() => segments.filter((segment) => segment.status === 'translated').length, []);
  const taskName = task?.name || 'Britney_Speech_2026';
  return <div className="page workspace-page">
    <div className="page-heading"><div><div className="eyebrow">当前任务 · {task ? task.currentStage : '演示数据'}</div><h1>{taskName}</h1><p className="muted">{task?.sourceFile || 'Britney_Speech_2026.mp4'} <span className="separator">·</span> 42:18 <span className="separator">·</span> 英语 → 简体中文</p></div><div className="heading-actions"><button className="button secondary">重新翻译</button><button className="button primary" onClick={onExport}>导出字幕 <span>↓</span></button></div></div>
    <div className="progress-card"><div className="progress-top"><div><strong>阶段 4 / 6</strong><span>翻译中</span></div><strong className="progress-value">68%</strong></div><div className="progress-track"><div style={{ width: '68%' }} /></div><div className="progress-bottom"><span>分段 4 / 7 · 已返回 46 条</span><span>剩余约 1 分 12 秒</span><div className="progress-controls"><button>Ⅱ 暂停</button><button>× 取消</button></div></div></div>
    <div className="workspace-grid"><section className="subtitle-card"><div className="card-header"><div><h2>字幕</h2><span className="card-subtitle">已加载 7 / 186 条 · 点击行可跳转播放</span></div><div className="subtitle-tools"><label className="search"><span>⌕</span><input placeholder="搜索字幕文本" /></label><button className="filter active">全部 186</button><button className="filter">已译 {translated + 150}</button><button className="filter">待译 32</button></div></div><div className="subtitle-table"><div className="table-head"><span>时间轴</span><span>原文 · 英语</span><span>译文 · 简体中文</span><span>状态</span></div>{segments.map((segment) => <div className="subtitle-row" key={segment.id}><div className="timecode"><span>{segment.start}</span><span>{segment.end}</span></div><div className="source-text">{segment.source}</div><div className={segment.translation ? 'translation-text' : 'translation-text empty'}>{segment.translation || '等待翻译…'}</div><div className={`segment-status ${segment.status}`}>{segment.status === 'translated' ? '已译' : segment.status === 'translating' ? '翻译中' : '待译'}</div></div>)}</div><div className="card-footer"><div className="audio-player"><button className="play">▶</button><div className="waveform">{Array.from({ length: 42 }, (_, i) => <i key={i} style={{ height: `${10 + ((i * 17) % 25)}px` }} />)}</div><span>00:12:35 <em>/ 00:42:18</em></span><button className="speed">1.0×</button></div></div></section><aside className="task-details card"><div className="card-header"><div><h2>任务信息</h2><span className="card-subtitle">详情</span></div><button className="more">•••</button></div><div className="preview"><div className="preview-screen"><span>▶</span></div><div className="preview-caption"><strong>预 览</strong><span>1080p · H.264</span></div></div><Detail label="识别模型" value="cohere-transcribe-03" /><Detail label="翻译服务" value="Ollama · qwen2.5:7b" /><Detail label="内容模式" value="标准 standard" /><Detail label="音频分段" value="4 / 7 段 · 每段 10 分钟" /><div className="validation"><span>✓</span><div><strong>批量翻译校验通过</strong><small>46 / 46 条返回 · ID 无重复 · JSON 可解析</small></div></div><div className="detail-stats"><div><strong>失败</strong><span>0 条</span></div><div><strong>自动重试</strong><span>1 次</span></div><div><strong>缓存命中</strong><span>32 条</span></div></div></aside></div>
  </div>;
}

function Detail({ label, value }: { label: string; value: string }) { return <div className="detail-row"><span>{label}</span><strong>{value}</strong></div>; }

function NewTask({ fileName, onFileChange, onCancel, onCreate }: { fileName: string; onFileChange: (event: ChangeEvent<HTMLInputElement>) => void; onCancel: () => void; onCreate: () => void }) {
  return <div className="page narrow-page"><div className="page-heading"><div><div className="eyebrow">创建工作流</div><h1>新建任务</h1><p className="muted">导入本地音视频 · 全程本地处理，文件不会离开这台电脑</p></div></div><section className="form-card card"><div className="dropzone"><div className="upload-icon">↑</div><strong>拖入音视频文件，或</strong><label className="button secondary">选择文件<input type="file" accept="audio/*,video/*" onChange={onFileChange} /></label><small>MP4 · MKV · AVI · MOV · WebM · MP3 · WAV · M4A · FLAC</small></div><div className="selected-file"><span className="file-icon">▣</span><div><strong>{fileName}</strong><small>42:18 · 1920×1080 · 24 fps · 48 kHz · 1 条音轨</small></div><button>移除</button></div><div className="form-section"><h3>识别设置</h3><div className="form-grid"><Field label="识别模型" value="cohere-transcribe-03" /><Field label="源语言" value="自动检测 · English" /><Field label="运行设备" value="CUDA · RTX 4070 Laptop" /><Field label="分段时长" value="10 分钟 / 段" /></div></div><div className="form-section"><h3>输出</h3><div className="form-grid"><Field label="输出目录" value="D:\\Projects\\Subtitles" /><Field label="字幕格式" value="SRT" /></div></div><div className="estimate"><div><span>预计耗时</span><strong>约 4 分 30 秒</strong></div><div><span>显存需求</span><strong>≈ 6.1 GB / 8 GB</strong></div><div><span>音频时长</span><strong>42:18</strong></div><div><span>分段数</span><strong>5 段</strong></div></div><div className="privacy-note">ⓘ 音视频与字幕全程在本机处理；翻译默认使用本机 Ollama，选择云端服务时会先征求确认。</div><div className="form-actions"><button className="button secondary" onClick={onCancel}>取消</button><button className="button primary" onClick={onCreate}>开始识别 <span>→</span></button></div></section></div>;
}

function Field({ label, value }: { label: string; value: string }) { return <label className="field"><span>{label}</span><div>{value}<b>⌄</b></div></label>; }

function Settings({ saved, onSave }: { saved: boolean; onSave: () => void }) {
  return <div className="page narrow-page"><div className="page-heading"><div><div className="eyebrow">应用配置</div><h1>翻译设置</h1><p className="muted">翻译服务 · 语言与风格 · 术语表 · 内容模式</p></div><button className="button primary" onClick={onSave}>{saved ? '✓ 已保存' : '保存更改'}</button></div><section className="settings-card card"><div className="settings-section"><h3>翻译服务</h3><div className="provider selected"><span className="provider-logo">O</span><div><strong>Ollama <small>本地</small></strong><span>已连接 · http://127.0.0.1:11434</span></div><i>✓</i></div><label className="setting-field"><span>模型</span><div>qwen2.5:7b <b>⌄</b></div></label><div className="provider muted-provider"><span className="provider-logo">◈</span><div><strong>OpenAI-compatible <small>云端</small></strong><span>DeepSeek · api.deepseek.com/v1</span></div><i>›</i></div></div><div className="settings-section"><h3>语言与风格</h3><div className="form-grid"><Field label="源语言" value="自动检测 · English" /><Field label="目标语言" value="简体中文" /><Field label="翻译风格" value="影视字幕 · 自然口语" /></div></div><div className="settings-section"><h3>内容模式</h3><div className="mode-row"><div><strong>标准 <code>standard</code></strong><span>默认关闭。保持原文语气，不做无依据的扩写或删减。</span></div><button className="toggle"><i /></button></div></div><div className="advanced"><strong>高级参数</strong><span>批次 8 条 · 上下文 4k · 并发 2 · 超时 60 秒 · 流式输出 开启 · 翻译缓存 开启</span><button>编辑</button></div></section></div>;
}

function Models() {
  const models = [['cohere-transcribe-03-2026', 'CohereLabs · 项目默认识别模型', '2.4 GB', '≈6.1 GB', '默认'], ['faster-whisper large-v3', 'Systran · 高质量识别', '3.1 GB', '≈4.7 GB', '已就绪'], ['faster-whisper distil-large-v3', 'Systran · 速度与质量平衡', '1.7 GB', '≈2.6 GB', '已就绪'], ['FunASR paraformer-zh', 'FunASR', '正在下载', '≈1.8 GB', '下载中']];
  return <div className="page"><div className="page-heading"><div><div className="eyebrow">本地资源</div><h1>模型管理</h1><p className="muted">Hugging Face 模型源 · 已安装模型 · 下载与缓存</p></div><div className="heading-actions"><button className="button secondary">打开缓存目录</button><button className="button primary">下载模型 <span>↓</span></button></div></div><div className="model-source card"><div><strong>模型源</strong><span>Hugging Face</span><small>hf-mirror.com · 国内镜像已启用</small></div><div className="gpu-badge"><span className="status-pulse" /><div><strong>GPU 已就绪</strong><small>RTX 4070 Laptop · 8 GB</small></div></div></div><section className="model-list card"><div className="card-header"><div><h2>已安装模型</h2><span className="card-subtitle">4 个模型 · 缓存目录 D:\\AI\\Models</span></div></div><div className="model-table-head"><span>模型</span><span>大小</span><span>显存</span><span>状态</span></div>{models.map(([name, desc, size, memory, status]) => <div className="model-row" key={name}><div><strong>{name}</strong><small>{desc}</small></div><span>{size}</span><span>{memory}</span><span className={status === '默认' ? 'model-status default' : status === '下载中' ? 'model-status downloading' : 'model-status'}>{status}</span></div>)}</section><div className="device-card card"><div><h2>设备检测</h2><p>推荐配置：<strong>large-v3 · float16</strong></p><span>当前显存 10 GB 以上时可启用 float16，识别质量最高；显存不足时自动回退 int8。</span></div><button className="button secondary">应用到当前项目</button></div></div>;
}

export default App;
