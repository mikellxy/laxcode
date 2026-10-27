import { useEffect, useRef, useState } from "react";
import { Activity, ArrowLeft, ArrowRight, X } from "lucide-react";
import { Logo } from "../logo/Logo";
import performanceButton from "../../assets/user-guide/performance_button.png";
import performance from "../../assets/user-guide/performance.png";
import code1 from "../../assets/user-guide/yd_code_1.png";
import code2 from "../../assets/user-guide/yd_code_2.png";
import code3 from "../../assets/user-guide/yd_code_3.png";
import skill1 from "../../assets/user-guide/yd_skill_1.png";
import skill2 from "../../assets/user-guide/yd_skill_2.png";
import skill3 from "../../assets/user-guide/yd_skill_3.png";
import skill4 from "../../assets/user-guide/yd_skill_4.png";
import "./user-guide.css";

const steps = [
  { label: "云端编程", eyebrow: "01 / 随时开始，随处交付", title: "随时随地发指令，云端写代码", description: "云端沙箱运行，手机、电脑都能下载产物。" },
  { label: "使用技能", eyebrow: "02 / 技能助力，拓展能力", title: "内置 Skill，让工作更轻松", description: "输入 / 选择内置技能；想要专属 Skill，直接让 LaxCode 帮你创建。" },
  { label: "性能监控", eyebrow: "03 / 看见任务的执行效率", title: "每一轮对话，耗时一目了然", description: "选中会话，点击 Context 下方的「性能监控」，查看模型与工具的耗时。" },
];
const codeImages = [
  { src: code1, alt: "发送编程指令，LaxCode 在云端沙箱检查环境并开始任务", width: 1626, height: 1330 },
  { src: code2, alt: "LaxCode 完成 Go 服务并展示实现结构与测试结果", width: 1684, height: 1330 },
  { src: code3, alt: "查看任务产物，在手机或电脑上打包下载文件", width: 1548, height: 1248 },
];
const skillImages = [
  { src: skill1, alt: "选择内置 excalidraw-skill，描述架构图绘制需求", width: 1604, height: 414 },
  { src: skill2, alt: "LaxCode 读取技能说明并执行任务", width: 1682, height: 746 },
  { src: skill3, alt: "LaxCode 汇总技能执行结果和生成的架构文档", width: 1618, height: 1336 },
  { src: skill4, alt: "预览技能生成的架构图，并下载任务产物", width: 1654, height: 1256 },
];

export function UserGuide({ onFinish }: { onFinish: () => void }) {
  const [step, setStep] = useState(0);
  const current = steps[step];
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
        <span className="guide-eyebrow">{current.eyebrow}</span>
        <h1>{current.title}</h1>
        <p>{current.description}</p>
      </div>
      {step === 2 ? <section className="guide-performance" aria-label="性能监控使用方法">
        <figure className="guide-figure">
          <figcaption><span className="guide-number">1</span><span>找到「性能监控」按钮</span></figcaption>
          <div className="guide-image-frame guide-sidebar-image"><img src={performanceButton} alt="在会话左侧栏 Context 区块下方点击性能监控按钮" /></div>
        </figure>
        <ArrowRight className="guide-flow-arrow" size={26} aria-hidden="true" />
        <figure className="guide-figure">
          <figcaption><Activity size={16} /><span>查看每轮耗时、首输出和调用明细</span></figcaption>
          <div className="guide-image-frame"><img src={performance} alt="会话性能汇总：Chat 总耗时、首输出、LLM 与各工具的调用统计" /></div>
        </figure>
      </section> : <section key={step} className="guide-examples" aria-label={`${current.label}示例`} tabIndex={0}>
        {(step === 0 ? codeImages : skillImages).map((image, index) => <figure className="guide-example" key={image.src}>
          <img {...image} loading={index === 0 ? "eager" : "lazy"} />
        </figure>)}
      </section>}
      <footer className="guide-footer">
        <button type="button" className="guide-skip" onClick={onFinish}>跳过引导</button>
        <ol className="guide-progress" aria-label="引导进度">
          {steps.map((item, index) => <li key={item.label} aria-current={step === index ? "step" : undefined}><span />{item.label}</li>)}
        </ol>
        <div className="guide-actions">
          {step > 0 && <button type="button" className="guide-back" onClick={() => setStep(step - 1)}><ArrowLeft size={16} /><span>上一步</span></button>}
          <button ref={nextRef} type="button" className="guide-next" onClick={step < steps.length - 1 ? () => setStep(step + 1) : onFinish}>{step < steps.length - 1 ? "下一步" : "开始体验"}<ArrowRight size={17} /></button>
        </div>
      </footer>
    </div>
  </main>;
}
