# 创建格式与布局

新图先用 write_file 保存元素数组，再交给 scripts/render.py 转换成原生 `.excalidraw`。下面的格式仅用于**新建**，不要用它覆盖有人工编辑的原生场景。

```json
[
  {"id":"client","type":"rectangle","x":80,"y":80,"width":200,"height":80,"text":"用户请求","fontSize":20,"fontFamily":"helvetica","fillStyle":"solid","backgroundColor":"#d0ebff","strokeColor":"#1971c2"},
  {"id":"agent","type":"rectangle","x":400,"y":80,"width":200,"height":80,"text":"Agent 执行","fontSize":20,"fontFamily":"helvetica","fillStyle":"solid","backgroundColor":"#d3f9d8","strokeColor":"#2f9e44"},
  {"id":"request","type":"arrow","x":280,"y":120,"startElementId":"client","endElementId":"agent","strokeColor":"#495057"}
]
```

- 坐标向右、向下增加；正文至少 16px，标题至少 20px。节点间距至少 40–60px，跨层间距 80–120px，带箭头标签的间距至少 120px。
- 中文节点宽度按中文字符数约 `fontSize × 字数 + 左右留白` 估算，不要套用拉丁字母的窄字宽。宁可增加宽度或手动换行。
- 形状的 `text` 会转换成绑定文字。独立文字用 `type: "text"`；`fontFamily` 可以用 `"helvetica"`、`"excalifont"`、`"cascadia"` 或字符串数字。
- 箭头使用 `startElementId`、`endElementId` 绑定节点。需要精确路径时，`points` 使用相对起点坐标，如 `[[0,0],[0,-60],[280,-60],[280,0]]`；`elbowed:true` 为直角线。
- 大背景区域不要使用 `text`，否则标签会出现在区域中央。另放一个独立 text 元素作为区域标题。
- 少用箭头标签，避免跨过无关节点；远距离连线沿区域边缘绕行。先画背景和节点，再画箭头与注释。
- 颜色可选：蓝 `#1971c2/#d0ebff`、绿 `#2f9e44/#d3f9d8`、紫 `#9c36b5/#eebefa`、橙 `#e8590c/#ffe8cc`（描边/填充）。`fillStyle:"solid"` 得到干净平面填充，边界可用 `strokeStyle:"dashed"`。

## 原生场景修改

原生场景含 `type:"excalidraw"`、`version:2`、`elements`、`appState`、`files` 等字段。不要删掉未知字段。元素可能包含 seed、versionNonce、groupIds、boundElements、startBinding/endBinding 和 customData。

绑定文字是单独元素：

```json
{"id":"client-label","type":"text","containerId":"client","text":"新文字","originalText":"新文字"}
```

根据实际文件里的 ID 修改；更新 text 与 originalText，并保留字体、位置及容器关系。新增或移动节点时也检查绑定文字、连线端点和文字框，渲染并检查源数据。保存路径保持不变。
