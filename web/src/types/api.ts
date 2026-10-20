export type ProjectDTO = { project_id: string; user_id: string; name: string; work_dir: string; created_at: string; updated_at: string };
export type ProjectListDTO = { projects: ProjectDTO[] };
export type SessionDTO = { session_id: string; mode: "code" | "rag"; user_id: string; project_id: string; title: string; work_dir: string; created_at: string; updated_at: string };
export type SessionPageDTO = { sessions: SessionDTO[]; next_before_session_id?: string; has_more: boolean };
export type ChatFileDTO = { path: string; change_type: "created" | "modified" };
export type HistoryMessageDTO = { seq: number; role: "user" | "assistant" | "tool"; content?: string; reasoning_content?: string; tool_summary?: string; chat_id?: string; upsert_files?: ChatFileDTO[]; created_at: string };
export type HistoryPageDTO = { messages: HistoryMessageDTO[]; next_before_seq?: number; has_more: boolean };
export type ModelDTO = { model_name: string; upstream_model?: string; model_ref: string; reasoning_effort?: string; reasoning_efforts?: string[] };
export type ProviderModelsDTO = { provider_name: string; model_list: ModelDTO[] };
export type ProviderListModelDTO = { current_model: string; current_reasoning_effort?: string; providers: ProviderModelsDTO[] };
export type SwitchModelDTO = { model_ref: string; reasoning_effort?: string };
export type TokenStatistics = { token_input: number; token_output: number };
export type ContextData = { window_token: TokenStatistics; context_window: number };
export type DurationStats = { count: number; total_ms: number; avg_ms: number };
export type RequestPerformance = {
  trace_id: string; request_id: string; operation: string; start_time: string; end_time: string;
  status: "completed" | "error"; total_ms: number; first_output_ms: number | null;
  llm_generate: DurationStats; tools: Record<string, DurationStats>;
};
export type ChatPerformance = {
  chat_id: string; total_ms: number; llm_generate: DurationStats;
  tools: Record<string, DurationStats>; requests: RequestPerformance[];
};
export type PerformanceSummary = {
  session_id: string; chats: ChatPerformance[]; partial: boolean;
  invalid_line_count: number; incomplete_trace_count: number;
};
export type EvaluationJobStatus = "queued" | "running" | "succeeded" | "failed";
export type EvaluationJobDTO = { id: string; source_session_id: string; requirement: string; status: EvaluationJobStatus; report_path?: string; error?: string; created_at: string; updated_at: string; completed_at?: string };
export type EvaluationJobListDTO = { jobs: EvaluationJobDTO[] };
export type RetryAction = "resume" | "resend";
export type ErrorPayload = { code: string; message: string; retry_action?: RetryAction };

export type StreamEvent =
  | { type: "prepare"; data: Record<string, never> }
  | { type: "start"; data: { session_id: string } }
  | { type: "reasoning"; data: { delta: string } }
  | { type: "message"; data: { delta: string } }
  | { type: "tool_call"; data: { info: string; window_token?: TokenStatistics; context_window?: number } }
  | { type: "done"; data: { session_id: string; result: string; token_used: TokenStatistics; window_token: TokenStatistics; context_window: number } }
  | { type: "error"; data: ErrorPayload };
