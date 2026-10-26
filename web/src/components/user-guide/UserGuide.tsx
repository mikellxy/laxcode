import { useEffect, useRef, useState } from "react";
import { Activity, ArrowLeft, ArrowRight, BookOpen, Download, X } from "lucide-react";
import { Logo } from "../logo/Logo";
import performanceButton from "../../assets/user-guide/performance_button.png";
import performance from "../../assets/user-guide/performance.png";
import callSkill from "../../assets/user-guide/call_skill.png";
import artsDownload from "../../assets/user-guide/arts_download.png";
import "./user-guide.css";

export function UserGuide({ onFinish }: { onFinish: () => void }) {
  const [step, setStep] = useState(0);
  const nextRef = useRef<HTMLButtonElement>(null);
  useEffect(() => { nextRef.current?.focus(); }, [step]);
  useEffect(() => {
    const escape = (event: KeyboardEvent) => {
      if (event.key === "Escape") { event.preventDefault(); onFinish(); }
    };
    document.addEventListener("keydown", escape);
    return () => document.removeEventListener("keydown", escape);
  }, [onFinish]);

  return <main className="user-guide" aria-label="新用户引导">
    <div className="guide-shell">
      <header className="guide-topbar">
        <div className="guide-brand"><Logo /><span className="guide-label">快速上手</span></div>
        <button type="button" className="guide-close" onClick={onFinish} aria-label="关闭引导，进入主页面" title="关闭引导"><X size={20} /></button>
      </header>
      <div className="guide-intro" aria-live="polite" aria-atomic="true">
        <span className="guide-eyebrow">{step === 0 ? "01 / 看见任务的执行效率" : "02 / 使用技能，带走成果"}</span>
        <h1>{step === 0 ? "每一轮对话，耗时一目了然" : "调用技能，下载任务成果"}</h1>
        <p>{step === 0 ? "选中会话，点击 Context 下方的「性能监控」，查看模型与工具的耗时。" : "输入 / 选择 Skill 辅助完成任务；还可预览并下载 Agent 创建的产物。"}</p>
      </div>
      {step === 0 ? <section className="guide-performance" aria-label="性能监控使用方法">
        <figure className="guide-figure">
          <figcaption><span className="guide-number">1</span><span>找到「性能监控」按钮</span></figcaption>
          <div className="guide-image-frame guide-sidebar-image"><img src={performanceButton} alt="在会话左侧栏 Context 区块下方点击性能监控按钮" /></div>
        </figure>
        <ArrowRight className="guide-flow-arrow" size={26} aria-hidden="true" />
        <figure className="guide-figure">
          <figcaption><Activity size={16} /><span>查看每轮耗时、首输出和调用明细</span></figcaption>
          <div className="guide-image-frame"><img src={performance} alt="会话性能汇总：Chat 总耗时、首输出、LLM 与各工具的调用统计" /></div>
        </figure>
      </section> : <section className="guide-features" aria-label="技能与产物下载">
        <article className="guide-feature">
          <header><span className="guide-feature-icon"><BookOpen size={21} /></span><div><h2>用 Skill 扩展任务能力</h2><p>输入 <kbd>/</kbd> 搜索技能，用方向键和回车选择，再描述任务。</p></div></header>
          <div className="guide-image-frame"><img src={callSkill} alt="聊天框输入斜杠后弹出技能菜单，可搜索并选择 excalidraw-skill 等技能" /></div>
        </article>
        <article className="guide-feature">
          <header><span className="guide-feature-icon"><Download size={21} /></span><div><h2>下载 Agent 创建的产物</h2><p>在文件清单中预览，点击「打包下载 ZIP」下载当前版本。</p></div></header>
          <div className="guide-image-frame"><img src={artsDownload} alt="任务新增或修改的文件列表，右下角提供打包下载 ZIP 按钮" /></div>
        </article>
      </section>}
      <footer className="guide-footer">
        <button type="button" className="guide-skip" onClick={onFinish}>跳过引导</button>
        <ol className="guide-progress" aria-label="引导进度">
          <li aria-current={step === 0 ? "step" : undefined}><span />性能监控</li>
          <li aria-current={step === 1 ? "step" : undefined}><span />技能与产物</li>
        </ol>
        <div className="guide-actions">
          {step === 1 && <button type="button" className="guide-back" onClick={() => setStep(0)}><ArrowLeft size={16} /><span>上一步</span></button>}
          <button ref={nextRef} type="button" className="guide-next" onClick={step === 0 ? () => setStep(1) : onFinish}>{step === 0 ? "下一步" : "开始体验"}<ArrowRight size={17} /></button>
        </div>
      </footer>
    </div>
  </main>;
}
