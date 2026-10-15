import { describe, expect, it } from "vitest";
import { searchSkills, taskWithSkills, trailingSkillQuery } from "./skills";

describe("trailingSkillQuery", () => {
  it("opens for a trailing slash or query after whitespace", () => {
    expect(trailingSkillQuery("/")).toEqual({ keyword: "", start: 0 });
    expect(trailingSkillQuery("画图 /xyz")).toEqual({ keyword: "xyz", start: 3 });
    expect(trailingSkillQuery("任务\n/画图")).toEqual({ keyword: "画图", start: 3 });
  });

  it("closes on whitespace and ignores paths and URLs", () => {
    for (const value of ["/xyz ", "/xyz\n", "/xyz\t", "/xyz 继续", "https://example.com/xyz", "docs/xyz", "/tmp/file"]) {
      expect(trailingSkillQuery(value)).toBeNull();
    }
  });
});

describe("searchSkills", () => {
  it("ranks exact, prefix, substring, subsequence, then description matches", () => {
    const skills = [
      { name: "description", description: "X1Y2Z 画流程图" },
      { name: "x111y222z333", description: "subsequence" },
      { name: "a-xyz-tool", description: "substring" },
      { name: "xyz-tool", description: "prefix" },
      { name: "xyz", description: "exact" },
      { name: "no-match", description: "nothing" },
    ];
    expect(searchSkills(skills, "XYZ").map(skill => skill.name)).toEqual([
      "xyz", "xyz-tool", "a-xyz-tool", "x111y222z333", "description",
    ]);
    expect(skills[0].name).toBe("description");
  });

  it("requires ordered characters within one field, including repeated characters", () => {
    expect(searchSkills([{ name: "xyz", description: "zy" }], "zyx")).toEqual([]);
    expect(searchSkills([{ name: "x", description: "yz" }], "xyz")).toEqual([]);
    expect(searchSkills([{ name: "x", description: "" }], "xx")).toEqual([]);
    expect(searchSkills([{ name: "xx", description: "" }], "xx")).toHaveLength(1);
    expect(searchSkills([{ name: "draw", description: "绘制架构示意图" }], "架构图")).toHaveLength(1);
  });

  it("sorts ties by name and shows all skills for an empty query", () => {
    const skills = [{ name: "b", description: "" }, { name: "a", description: "" }];
    expect(searchSkills(skills, "").map(skill => skill.name)).toEqual(["a", "b"]);
    expect(searchSkills([], "")).toEqual([]);
  });
});

describe("taskWithSkills", () => {
  it("keeps ordinary tasks and unselected slash queries as plain text", () => {
    expect(taskWithSkills("  普通任务  ", [])).toBe("普通任务");
    expect(taskWithSkills("/xyz", [])).toBe("/xyz");
  });

  it("serializes selected names as explicit skill instructions before the task", () => {
    expect(taskWithSkills("  画架构图  ", ["excalidraw-skill", "code-review"])).toBe(
      "用户指定使用以下技能：excalidraw-skill、code-review。\n请先读取所选技能各自的 SKILL.md，再按照技能指引完成任务。\n\n用户任务：\n画架构图",
    );
    expect(taskWithSkills("", ["excalidraw-skill"])).toContain("excalidraw-skill");
  });
});
