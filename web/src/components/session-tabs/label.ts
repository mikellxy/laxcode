export function sessionLabel(title: string, newestFirstIndex: number, total: number): string {
  const named = title.trim();
  if (named) return named;
  return `会话 ${String(total - newestFirstIndex).padStart(2, "0")}`;
}

export function contextUsageLabel(used: number, contextWindow: number): string {
  if (contextWindow <= 0) return "0% used";
  const percent = used / contextWindow * 100;
  if (percent > 0 && percent < 0.01) return "<0.01% used";
  return `${new Intl.NumberFormat("en-US", { maximumFractionDigits: 2 }).format(percent)}% used`;
}
