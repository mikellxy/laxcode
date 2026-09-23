import { requestJSON } from "./client";

export type DirectoryPickerResult = { path: string | null };

export const pickDirectory = () => requestJSON<DirectoryPickerResult>("/api/directory-picker", {
  method: "POST",
  body: "{}",
});
