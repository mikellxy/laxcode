import { requestJSON } from "./client";

export type Skill = { name: string; description: string };

export const listSkills = () => requestJSON<{ skills: Skill[] }>("/skills");
