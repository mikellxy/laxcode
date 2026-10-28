export type ProjectDTO = { project_id: string; user_id: string; name: string; work_dir: string; created_at: string; updated_at: string };
export type ProjectListDTO = { projects: ProjectDTO[] };
export type SessionDTO = { session_id: string; mode: "code" | "rag"; user_id: string; project_id: string; title: string; work_dir: string; created_at: string; updated_at: string };
export type SessionPageDTO = { sessions: SessionDTO[]; next_before_session_id?: string; has_more: boolean };
export type ChatFileDTO = { path: string; change_type: "created" | "modified" };
export type HistoryMessageDTO = { seq: number; role: "user" | "assistant" | "tool"; content?: string; reasoning_content?: string; tool_summary?: string; chat_id?: string; upsert_files?: ChatFileDTO[]; created_at: string };
export type TaskStatus = "waiting" | "running" | "success" | "fail";
export type TaskDTO = { task_id: string; task_status: TaskStatus };
export type HistoryPageDTO = { task_id?: string | null; task_status?: TaskStatus | null; task_error_code?: string; task_error_message?: string; messages: HistoryMessageDTO[]; next_before_seq?: number; has_more: boolean; can_resume: boolean };
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
export type ErrorPayload = { code: string; message: string; retry_action?: RetryAction; refresh_after_ms?: number };

export type EventIdentity = { task_id?: string; event_id?: number; message_seq?: number };
export type StreamEvent =
  | { type: "task_state"; data: TaskDTO & { error_code?: string; error_message?: string } }
  | { type: "refresh_required"; data: { code: string; refresh_after_ms?: number } }
  | { type: "prepare"; data: Record<string, never> }
  | { type: "start"; data: EventIdentity & { session_id: string } }
  | { type: "reasoning"; data: EventIdentity & { delta: string } }
  | { type: "message"; data: EventIdentity & { delta: string } }
  | { type: "tool_call"; data: EventIdentity & { info: string; window_token?: TokenStatistics; context_window?: number } }
  | { type: "done"; data: { session_id: string; result: string; token_used: TokenStatistics; window_token: TokenStatistics; context_window: number } }
  | { type: "error"; data: ErrorPayload };
