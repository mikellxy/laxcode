import { ArrowRight, FolderPlus, LoaderCircle } from "lucide-react";

type Props = {
  creating: boolean;
  onCreate: () => void;
};

export function ProjectOnboarding({ creating, onCreate }: Props) {
  return <main className="project-onboarding">
    <section className="project-onboarding-card">
      <span className="project-onboarding-icon"><FolderPlus size={27} strokeWidth={1.8} /></span>
      <span className="project-onboarding-eyebrow">WELCOME TO LAXCODE</span>
      <h1>创建你的第一个工作空间</h1>
      <p>选择一个本地代码目录。LaxCode 会创建项目，并自动准备好第一个会话。</p>
      <button className="project-onboarding-action" onClick={onCreate} disabled={creating}>
        {creating ? <LoaderCircle className="spin" size={18} /> : <FolderPlus size={18} />}
        <span>{creating ? "正在创建工作空间…" : "选择项目目录"}</span>
        {!creating && <ArrowRight size={17} />}
      </button>
      <small>项目数据仅保存在当前 LaxCode 服务中</small>
    </section>
  </main>;
}
