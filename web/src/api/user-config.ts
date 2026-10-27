import { requestJSON } from "./client";

export type UserConfig = { user_guide_version: string; user_guide_seen_version: string | null };
export type UserConfigUpdate = { user_guide_seen_version?: string };

export const getUserConfig = () => requestJSON<UserConfig>("/api/user-config");
export const updateUserConfig = (config: UserConfigUpdate) => requestJSON<void>("/api/user-config", { method: "PUT", body: JSON.stringify(config) });
