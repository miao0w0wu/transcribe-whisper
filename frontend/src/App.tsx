// 应用外壳：侧栏布局、视图路由与全局事件订阅。
// 具体页面逻辑全部下沉到 src/views 下，这里只负责「选哪个视图」和「事件怎么分发」。
import { useCallback, useEffect, useMemo, useRef, useState } from 'react';
import './App.css';
import * as api from './lib/api';
import { subscribeModelProgress, subscribeTaskProgress } from './lib/events';
import { formatRelative, isRunningStatus, statusLabel } from './lib/format';
import type { ModelProgressEvent, Task, TaskProgressEvent } from './lib/types';
import { ModelsView } from './views/ModelsView';
import { NewTask } from './views/NewTask';
import { SettingsView } from './views/SettingsView';
import { TaskQueue } from './views/TaskQueue';
import { Workspace } from './views/Workspace';

/** 视图路由标识 */
type View = 'workspace' | 'queue' | 'new-task' | 'models' | 'settings';

/** 侧栏导航项 */
const NAV_ITEMS: { id: View; label: string; icon: string }[] = [
  { id: 'new-task', label: '新建任务', icon: '+' },
  { id: 'workspace', label: '工作台', icon: '▦' },
  { id: 'queue', label: '任务队列', icon: '≡' },
  { id: 'models', label: '模型管理', icon: '◇' },
  { id: 'settings', label: '设置', icon: '⚙' },
];

/** 面包屑标题 */
const VIEW_TITLES: Record<View, string> = {
  workspace: '工作台',
  queue: '任务队列',
  'new-task': '新建任务',
  models: '模型管理',
  settings: '翻译设置',
};

/** 侧栏最近项目最多展示的条数 */
const RECENT_LIMIT = 4;

function App() {
  const [view, setView] = useState<View>('workspace');
  const [activeTaskId, setActiveTaskId] = useState<string | null>(null);
  const [tasks, setTasks] = useState<Task[]>([]);
  const [activeTask, setActiveTask] = useState<Task | null>(null);
  const [refreshToken, setRefreshToken] = useState(0);
  const [demo] = useState(() => !api.isWailsRuntime());

  // 最新一次进度事件：用一个自增版本号触发子视图刷新，避免数组变化导致重复订阅
  const [taskEvent, setTaskEvent] = useState<TaskProgressEvent | null>(null);
  const [modelEvent, setModelEvent] = useState<ModelProgressEvent | null>(null);
  const refreshTimer = useRef<number | null>(null);

  /** 全局任务事件：更新侧栏队列计数与最近项目，并转发给视图 */
  const handleTaskProgress = useCallback((event: TaskProgressEvent) => {
    setTaskEvent(event);
    if (!event.taskId) return;
    setTasks((prev) => {
      const index = prev.findIndex((task) => task.id === event.taskId);
      if (index < 0) return prev;
      const next = [...prev];
      next[index] = {
        ...next[index],
        status: event.status || next[index].status,
        stage: event.stage || next[index].stage,
        progress: Number.isFinite(event.progress) ? event.progress : next[index].progress,
        currentStage: event.currentStage || next[index].currentStage,
        message: event.message || next[index].message,
        error: event.error || next[index].error,
      };
      return next;
    });
    setActiveTask((prev) =>
      prev && prev.id === event.taskId
        ? {
            ...prev,
            status: event.status || prev.status,
            progress: Number.isFinite(event.progress) ? event.progress : prev.progress,
            currentStage: event.currentStage || prev.currentStage,
            message: event.message || prev.message,
            error: event.error || prev.error,
          }
        : prev,
    );
  }, []);

  // 订阅全局事件，组件卸载时取消订阅
  useEffect(() => {
    const offTask = subscribeTaskProgress(handleTaskProgress);
    const offModel = subscribeModelProgress((event) => setModelEvent(event));
    return () => {
      offTask();
      offModel();
    };
  }, [handleTaskProgress]);

  /** 拉取任务列表，供侧栏计数与最近项目使用 */
  const loadTasks = useCallback(async () => {
    if (demo) {
      setTasks([]);
      setActiveTask(null);
      return;
    }
    try {
      const dashboard = await api.getDashboard();
      setTasks(dashboard.tasks ?? []);
      setActiveTask(dashboard.activeTask ?? null);
      setActiveTaskId((prev) => prev ?? dashboard.activeTask?.id ?? null);
    } catch {
      // 侧栏属于辅助信息，失败时静默处理，具体错误由各视图展示
    }
  }, [demo]);

  useEffect(() => {
    void loadTasks();
  }, [loadTasks, refreshToken]);

  // 任务结束类事件后稍作延迟重新拉一次列表，保证派生数据（字幕数量等）一致
  useEffect(() => {
    if (!taskEvent) return;
    if (taskEvent.status !== 'completed' && taskEvent.status !== 'failed' && taskEvent.status !== 'canceled') return;
    if (refreshTimer.current !== null) window.clearTimeout(refreshTimer.current);
    refreshTimer.current = window.setTimeout(() => {
      setRefreshToken((token) => token + 1);
    }, 600);
    return () => {
      if (refreshTimer.current !== null) {
        window.clearTimeout(refreshTimer.current);
        refreshTimer.current = null;
      }
    };
  }, [taskEvent]);

  const runningCount = useMemo(() => tasks.filter((task) => isRunningStatus(task.status)).length, [tasks]);
  const recentTasks = useMemo(
    () => [...tasks].sort((a, b) => (b.createdAt || '').localeCompare(a.createdAt || '')).slice(0, RECENT_LIMIT),
    [tasks],
  );
  const currentTask = useMemo(
    () => tasks.find((task) => task.id === activeTaskId) ?? activeTask,
    [tasks, activeTaskId, activeTask],
  );

  const openTask = useCallback((taskOrId: Task | string) => {
    const id = typeof taskOrId === 'string' ? taskOrId : taskOrId.id;
    setActiveTaskId(id);
    setView('workspace');
  }, []);

  const handleCreated = useCallback(
    (taskId: string) => {
      setActiveTaskId(taskId);
      setView('workspace');
      setRefreshToken((token) => token + 1);
    },
    [],
  );

  const requestRefresh = useCallback(() => setRefreshToken((token) => token + 1), []);

  /** 面包屑展示的当前任务名 */
  const currentTitle =
    view === 'workspace' && currentTask ? currentTask.name || '当前任务' : VIEW_TITLES[view];

  return (
    <div className="app-shell">
      <aside className="sidebar">
        <div className="brand">
          <span className="brand-mark">◒</span>
          <span>transcribe</span>
          <b>·</b>
          <span>whisper</span>
        </div>

        <button type="button" className="new-task-button" onClick={() => setView('new-task')}>
          <span>＋</span> 新建任务
        </button>

        <nav className="main-nav">
          {NAV_ITEMS.map((item) => (
            <button
              key={item.id}
              type="button"
              className={view === item.id ? 'nav-item active' : 'nav-item'}
              onClick={() => setView(item.id)}
            >
              <span className="nav-icon">{item.icon}</span>
              {item.label}
              {item.id === 'queue' && tasks.length > 0 ? (
                <span className={runningCount > 0 ? 'nav-count running' : 'nav-count'}>{tasks.length}</span>
              ) : null}
            </button>
          ))}
        </nav>

        <div className="sidebar-section-label">最近任务</div>
        {recentTasks.length === 0 ? (
          <div className="recent-empty">{demo ? '演示模式下暂无任务' : '还没有任务，先新建一个吧'}</div>
        ) : (
          recentTasks.map((task) => (
            <button
              key={task.id}
              type="button"
              className={task.id === activeTaskId ? 'recent-project active' : 'recent-project'}
              onClick={() => openTask(task)}
            >
              <span className={`project-dot ${task.status === 'completed' ? 'green' : task.status === 'failed' ? 'red' : task.status === 'paused' ? 'orange' : 'blue'}`} />
              <div>
                <strong>{task.name || task.id}</strong>
                <small>
                  {statusLabel(task.status)}
                  {isRunningStatus(task.status) ? ` · ${Math.round(task.progress || 0)}%` : ''}
                  <span className="separator">·</span>
                  {formatRelative(task.updatedAt || task.createdAt)}
                </small>
              </div>
            </button>
          ))
        )}

        <div className="sidebar-footer">
          <div className="service-status">
            <span className={demo ? 'status-pulse offline' : 'status-pulse'} />
            <div>
              <strong>{demo ? '未连接本地后端' : '本地后端已连接'}</strong>
              <small>{demo ? '界面处于演示模式' : `${tasks.length} 个任务 · ${runningCount} 个进行中`}</small>
            </div>
          </div>
          <div className="device">{demo ? '浏览器预览模式' : currentTask?.model || '模型信息见模型管理'}</div>
        </div>
      </aside>

      <main className="content">
        <header className="topbar">
          <div className="breadcrumbs">
            项目 <span>/</span> <b>{currentTitle}</b>
          </div>
          <div className="top-actions">
            {runningCount > 0 ? <span className="topbar-running">● {runningCount} 个任务进行中</span> : null}
            <span className={demo ? 'topbar-mode demo' : 'topbar-mode'}>{demo ? '演示模式' : '本地运行'}</span>
            <div className="avatar">Z</div>
          </div>
        </header>

        {/* 各视图内部都会展示演示模式提示，App 层不再重复展示 */}

        {view === 'workspace' ? (
          <Workspace
            taskId={activeTaskId}
            demo={demo}
            progressEvent={taskEvent}
            onNewTask={() => setView('new-task')}
            onOpenQueue={() => setView('queue')}
            onTasksChanged={requestRefresh}
          />
        ) : null}

        {view === 'queue' ? (
          <TaskQueue
            demo={demo}
            activeTaskId={activeTaskId}
            progressEvent={taskEvent}
            onOpenTask={(task) => openTask(task)}
            onNewTask={() => setView('new-task')}
            onTasksChanged={requestRefresh}
          />
        ) : null}

        {view === 'new-task' ? <NewTask demo={demo} onCreated={handleCreated} onCancel={() => setView('workspace')} /> : null}

        {view === 'settings' ? <SettingsView demo={demo} /> : null}

        {view === 'models' ? <ModelsView demo={demo} modelEvent={modelEvent} /> : null}
      </main>
    </div>
  );
}

export default App;