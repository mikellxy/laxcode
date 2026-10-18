---
name: excalidraw-skill
description: 在 LaxCode cloud 的离线 Bash 沙箱中创建或修改可编辑的 Excalidraw 流程图、架构图，生成 .excalidraw 源文件及 PNG/SVG。适用于要求手绘风格图、Excalidraw 文件或修改现有图的任务。
---

# Excalidraw 离线绘图

本技能使用 Bash 镜像内预装的 `excalidraw-canvas` 2.1.2、Node 和中文字体。不需要 MCP、浏览器或外网。包内 `scripts/render.py` 负责把新建元素数组转换为原生场景，并输出 PNG/SVG；后续以磁盘上的 `.excalidraw` 为唯一来源。参见 `references/elements.md` 的创建格式和布局规则。

## 路径与执行环境

- 先通过 grep/glob/read_file 定位本定义和参考文件。运行脚本时使用本 SKILL.md 所在目录的真实绝对路径，例如 `python3 <技能目录>/scripts/render.py docs/system.excalidraw`。不要使用 `$HOME/.laxcode/skills`：容器 HOME 是临时目录。
- Skill 包只读；最终源文件与图片放在项目中，推荐 `docs/`。临时绘图输入放 `$TMPDIR`，依赖不在运行时安装。
- 每次 Bash 是新容器。不要把画布服务保留到下一次调用，也不要单独运行 start/add 后等待下一次调用 export。辅助脚本会在一次调用中完成需要的工作并停止画布。
- 如果提示 `excalidraw-canvas` 缺失，说明 Bash 镜像未更新。报告需要重新构建镜像，不要执行 npx、npm install 或尝试宿主机的 Mac 路径。

## 创建新图

1. 先规划节点、层级、区域和连线，选用稳定、语义明确的元素 ID。
2. 使用 **write_file** 将非空元素数组写到最终目标路径，例如 `docs/system.excalidraw`。数组格式见参考文件；这份新建输入会被脚本转换为可在 Excalidraw/VS Code 打开的原生场景。
3. 在一次 Bash 调用中运行：

   ```sh
   python3 <技能目录>/scripts/render.py docs/system.excalidraw
   ```

   它生成同目录下的 `system.excalidraw`、`system.png`、`system.svg`，输出文件路径、场景 SHA-256、元素数量和渲染尺寸。复杂图可以设置 Bash `timeout_ms=60000`。默认缩放 1，可传 `--scale 2`；PNG 最长边上限 4096px。
4. 用 read_file 检查最终 JSON，确认标签、绑定与连接关系。需要修改时进入下面的修改流程。

## 修改现有图

- **每轮重新读取磁盘文件**。现有原生场景不能重新生成成一个新数组；保留 IDs、customData、appState、样式、用户位置和连线几何，只修改任务要求的内容。
- 修改前用 Bash 计算 `sha256sum`，并把当前文件复制成同目录的 `<文件名>.bak`，保留一份本轮编辑前的备份。修改期间发现文件被他人更新，应重新读取并合并意图。
- 使用 **edit_file** 做精确修改。修改形状文字时，找 `containerId` 指向该形状的已有 text 元素，更新它的 `text` 和 `originalText`；不要给形状另加一个 `text` 字段。必要时调整文字框和容器尺寸，避免截断。
- 在同一路径运行 render.py。对原生场景，脚本只渲染、不改写源文件，因而不会重置版本、布局和未修改的字段。可用 `--expected-sha256 <修改后文件哈希>` 检查输入版本；脚本也会在发布输出前确认源文件在渲染期间未变。
- 不调用 CLI 的 import/export 往返覆盖原生场景：上游可能重置 appState 和元素版本。要增添原生元素时遵循现有场景格式；不确定字段时阅读已有元素和参考文件。

## 交付与验证边界

- 回复中的项目路径从 `/{project_name}/...` 开始，隐藏宿主机前缀。文档中的图片引用使用相对于文档的 PNG 路径。
- 最终交付文件保留在当前项目目录内。Cloud 模式在本轮 Chat/Resume 结束时扫描项目文件变化，统一登记新增或修改的 `.excalidraw`、PNG/SVG 等文件，供聊天界面预览和下载；不依赖文件由哪个工具写入。用户临时目录和 Skill 目录不属于交付范围，未发生变化的文件不会新增到本轮清单。
- 当前 LaxCode 没有向模型返回图片的工具。渲染成功、尺寸检查和 JSON 检查不等于已经目视检查；不要声称看过截图或保证没有视觉重叠。若宿主产品后续提供看图能力，再执行“看图、修正、重渲染”循环。
- 不使用 share、Mermaid 转换、浏览器 renderer、实时画布 URL 或本机 VS Code 命令。本版不提供浏览器画布和上传后再编辑的交互。
- PNG 使用镜像中文字体；SVG 的中文还可能依赖查看端字体。图过大导致超时或内存不足时，拆图或降低缩放，不反复联网安装工具。
