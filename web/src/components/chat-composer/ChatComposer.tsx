import { useEffect, useId, useRef, useState } from "react";
import { useQuery } from "@tanstack/react-query";
import { ArrowUp, BookOpen, Square, X } from "lucide-react";
import { listSkills } from "../../api/skills";
import { shouldSubmitOnKeyDown } from "./keyboard";
import { searchSkills, taskWithSkills, trailingSkillQuery } from "./skills";

export function ChatComposer({ running, disabled, locked, onSend, onCancel }: { running: boolean; disabled: boolean; locked: boolean; onSend: (value: string) => void; onCancel: () => void }) {
  const [value, setValue] = useState("");
  const [selectedSkills, setSelectedSkills] = useState<string[]>([]);
  const [activeIndex, setActiveIndex] = useState(0);
  const [dismissed, setDismissed] = useState(false);
  const ref = useRef<HTMLTextAreaElement>(null);
  const menuRef = useRef<HTMLUListElement>(null);
  const menuID = useId();
  const composing = useRef(false);
  const slash = trailingSkillQuery(value);
  const keyword = slash?.keyword ?? "";
  const canSearch = slash !== null && !dismissed && !disabled && !locked;
  const skills = useQuery({ queryKey: ["skills"], queryFn: listSkills, enabled: canSearch });
  const matches = canSearch ? searchSkills(skills.data?.skills ?? [], keyword) : [];
  const menuOpen = matches.length > 0;
  const activeOption = Math.min(activeIndex, matches.length - 1);
  const hasTask = Boolean(value.trim() || selectedSkills.length);
  useEffect(() => { const el = ref.current; if (el) { el.style.height = "0"; el.style.height = `${Math.min(el.scrollHeight, 160)}px`; } }, [value]);
  useEffect(() => {
    if (menuOpen) menuRef.current?.children.item(activeOption)?.scrollIntoView({ block: "nearest" });
  }, [menuOpen, activeOption, keyword]);
  const selectSkill = (name: string) => {
    if (!slash) return;
    const remaining = value.slice(0, slash.start);
    setValue(remaining);
    setSelectedSkills(current => current.includes(name) ? current : [...current, name]);
    setDismissed(true);
    requestAnimationFrame(() => {
      ref.current?.focus();
      ref.current?.setSelectionRange(remaining.length, remaining.length);
    });
  };
  const submit = () => {
    if (!hasTask || running || disabled || locked) return;
    const task = taskWithSkills(value, selectedSkills);
    setValue("");
    setSelectedSkills([]);
    setDismissed(false);
    onSend(task);
  };
  const hint = locked ? "配置模型后即可开始对话"
    : menuOpen ? "↑ ↓ 选择技能 · Enter 确认 · 空格关闭"
    : canSearch && skills.isPending ? "正在加载技能…"
    : canSearch && skills.isError ? "技能加载失败，请重新输入 / 重试"
    : canSearch ? "没有匹配的技能" : "输入 / 选择技能 · Enter 发送 · Shift + Enter 换行";
  return <div className="composer-wrap"><div className={`composer ${running ? "running" : ""} ${locked ? "locked" : ""}`}>
    {menuOpen && <ul ref={menuRef} id={menuID} role="listbox" aria-label="可用技能" className="skill-menu">
      {matches.map((skill, index) => <li
        key={skill.name}
        id={`${menuID}-${index}`}
        role="option"
        aria-selected={index === activeOption}
        className={index === activeOption ? "active" : ""}
        onMouseDown={event => event.preventDefault()}
        onMouseMove={() => setActiveIndex(index)}
        onClick={() => selectSkill(skill.name)}
      ><BookOpen size={18} aria-hidden="true" /><div><strong>{skill.name}</strong><span>{skill.description}</span></div></li>)}
    </ul>}
    {selectedSkills.length > 0 && <div className="selected-skills" aria-label="已选择的技能">
      {selectedSkills.map(name => <span className="skill-chip" key={name}><BookOpen size={14} aria-hidden="true" />{name}<button
        type="button" aria-label={`移除技能 ${name}`} disabled={disabled || locked}
        onClick={() => { setSelectedSkills(current => current.filter(skill => skill !== name)); ref.current?.focus(); }}
      ><X size={13} /></button></span>)}
    </div>}
    <textarea
    ref={ref}
    rows={1}
    value={value}
    aria-label="聊天任务"
    role="combobox"
    aria-autocomplete="list"
    aria-haspopup="listbox"
    aria-expanded={menuOpen}
    aria-controls={menuOpen ? menuID : undefined}
    aria-activedescendant={menuOpen ? `${menuID}-${activeOption}` : undefined}
    disabled={disabled && !locked}
    readOnly={locked}
    placeholder={locked ? "暂无可用模型，请联系管理员…" : "给 LaxCode 发送任务…"}
    onChange={(event) => { setValue(event.target.value); setActiveIndex(0); setDismissed(false); }}
    onFocus={() => setDismissed(false)}
    onBlur={() => setDismissed(true)}
    onCompositionStart={() => { composing.current = true; }}
    onCompositionEnd={() => { composing.current = false; }}
    onKeyDown={(event) => {
      if (disabled || locked || composing.current || event.nativeEvent.isComposing || event.nativeEvent.keyCode === 229) return;
      if (menuOpen) {
        if (event.key === "ArrowDown" || event.key === "ArrowUp") {
          event.preventDefault();
          setActiveIndex((activeOption + (event.key === "ArrowDown" ? 1 : -1) + matches.length) % matches.length);
          return;
        }
        if (event.key === "Enter" && !event.shiftKey) {
          event.preventDefault();
          selectSkill(matches[activeOption].name);
          return;
        }
        if (event.key === "Escape") { event.preventDefault(); setDismissed(true); return; }
      }
      if (event.key === "Backspace" && !value && selectedSkills.length) {
        event.preventDefault();
        setSelectedSkills(current => current.slice(0, -1));
        return;
      }
      if (!shouldSubmitOnKeyDown({
        key: event.key,
        shiftKey: event.shiftKey,
        isComposing: composing.current || event.nativeEvent.isComposing,
        keyCode: event.nativeEvent.keyCode,
      })) return;
      event.preventDefault();
      submit();
    }}
  /><div className="composer-actions"><span role="status">{hint}</span>{running ? <button className="stop-button" onClick={onCancel} aria-label="取消生成"><Square size={14} fill="currentColor" /></button> : <button className="send-button" onClick={submit} disabled={!hasTask || disabled || locked} aria-label="发送"><ArrowUp size={18} /></button>}</div></div><p>LaxCode 可能会犯错，请检查重要结果。</p></div>;
}
