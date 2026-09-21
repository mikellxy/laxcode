export function sessionLabel(title: string, newestFirstIndex: number, total: number): string {
  const named = title.trim();
  if (named) return named;
  return `会话 ${String(total - newestFirstIndex).padStart(2, "0")}`;
}
