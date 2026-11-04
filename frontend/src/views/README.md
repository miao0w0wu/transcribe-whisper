# 视图与组件目录说明

本目录存放拆分后的页面视图，`App.tsx` 只保留布局、路由状态与全局事件订阅。

| 文件 | 作用 |
|---|---|
| `Workspace.tsx` | 工作台：真实任务数据、进度、字幕校对、导出 |
| `NewTask.tsx` | 新建任务：文件选择、识别与翻译参数、创建并启动 |
| `TaskQueue.tsx` | 任务队列：列表、状态、进度、行内暂停/恢复/取消/重试/删除 |
| `SettingsView.tsx` | 翻译设置：Provider、端点、API Key、识别引擎、模型目录与镜像 |
| `ModelsView.tsx` | 模型管理：模型下载/删除、`model:progress` 进度、环境检测 |
| `components/primitives.tsx` | 跨视图复用的徽章、进度条、空状态、错误条、表单字段等 |
| `workspace/ProgressCard.tsx` | 工作台进度卡与任务操作按钮 |
| `workspace/SubtitleTable.tsx` | 字幕表格：搜索、筛选、行内编辑、保存、翻译待译条目 |
| `workspace/TaskDetail.tsx` | 任务详情侧栏 |

共享逻辑位于 `src/lib`：

| 文件 | 作用 |
|---|---|
| `types.ts` | 与后端 JSON 字段一致的领域类型与下拉选项常量 |
| `runtime.ts` | Wails 运行时探测 `isWailsRuntime()` 与统一的中文错误封装 |
| `api.ts` | 全部后端方法的封装，页面只从这里调用 |
| `events.ts` | `task:progress` / `model:progress` 订阅封装，返回取消函数 |
| `format.ts` | 时长、时间轴、字节、状态、语言等格式化工具 |
