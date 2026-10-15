import type { Skill } from "../../api/skills";

export function trailingSkillQuery(value: string): { keyword: string; start: number } | null {
  const match = /(?:^|\s)\/([^\s/]*)$/u.exec(value);
  return match && match.index + match[0].length === value.length
    ? { keyword: match[1], start: match.index + match[0].indexOf("/") } : null;
}

function matchesSubsequence(text: string, keyword: string): boolean {
  const chars = [...keyword];
  let matched = 0;
  for (const char of text) {
    if (char === chars[matched]) matched++;
    if (matched === chars.length) return true;
  }
  return chars.length === 0;
}

function matchRank(skill: Skill, keyword: string): number {
  const name = skill.name.toLowerCase();
  if (name === keyword) return 0;
  if (name.startsWith(keyword)) return 1;
  if (name.includes(keyword)) return 2;
  if (matchesSubsequence(name, keyword)) return 3;
  if (matchesSubsequence(skill.description.toLowerCase(), keyword)) return 4;
  return 5;
}

export function searchSkills(skills: Skill[], query: string): Skill[] {
  const keyword = query.toLowerCase();
  return skills.map(skill => ({ skill, rank: matchRank(skill, keyword) }))
    .filter(({ rank }) => rank < 5)
    .sort((a, b) => a.rank - b.rank || a.skill.name.localeCompare(b.skill.name))
    .map(({ skill }) => skill);
}

export function taskWithSkills(value: string, skills: string[]): string {
  const task = value.trim();
  if (!skills.length) return task;
  return `用户指定使用以下技能：${skills.join("、")}。\n请先读取所选技能各自的 SKILL.md，再按照技能指引完成任务。${task ? `\n\n用户任务：\n${task}` : ""}`;
}
