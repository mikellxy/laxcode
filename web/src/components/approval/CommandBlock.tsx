import { useLayoutEffect, useMemo, useRef, useState } from "react";
import { ChevronDown, ChevronUp } from "lucide-react";
import hljs from "highlight.js/lib/core";
import bash from "highlight.js/lib/languages/bash";

hljs.registerLanguage("bash", bash);

// CommandBlock 展示待审批的风险命令：bash 语法高亮（highlight.js 按需注册，
// 仅打包 bash 一种语言）；超过 5 行默认折叠，点击展开/收起。溢出探测基于
// 实际渲染高度（scrollHeight vs clientHeight），兼容长行折行；判定溢出后
// 保持，避免展开后按钮消失。
export function CommandBlock({ command }: { command: string }) {
  const [expanded, setExpanded] = useState(false);
  const [overflowing, setOverflowing] = useState(false);
  const preRef = useRef<HTMLPreElement>(null);
  const highlighted = useMemo(() => hljs.highlight(command, { language: "bash" }).value, [command]);
  useLayoutEffect(() => {
    const el = preRef.current;
    if (el && el.scrollHeight > el.clientHeight + 1) setOverflowing(true);
  }, [command]);
  return <div className="command-block">
    <pre ref={preRef} className={`command-code${expanded ? "" : " collapsed"}`}><code dangerouslySetInnerHTML={{ __html: highlighted }} /></pre>
    {overflowing && <button className="command-toggle" onClick={() => setExpanded((v) => !v)}>{expanded ? <><ChevronUp size={13} />收起</> : <><ChevronDown size={13} />展开全部</>}</button>}
  </div>;
}
