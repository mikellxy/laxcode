export type ProjectDTO = { project_id: string; user_id: string; name: string; work_dir: string; created_at: string; updated_at: string };
export type ProjectListDTO = { projects: ProjectDTO[] };
export type SessionDTO = { session_id: string; mode: "code" | "rag"; user_id: string; project_id: string; title: string; work_dir: string; created_at: string; updated_at: string };
export type SessionPageDTO = { sessions: SessionDTO[]; next_before_session_id?: string; has_more: boolean };
export type HistoryMessageDTO = { seq: number; role: "user" | "assistant" | "tool"; content?: string; reasoning_content?: string; tool_summary?: string; chat_id?: string; created_at: string };
export type HistoryPageDTO = { messages: HistoryMessageDTO[]; next_before_seq?: number; has_more: boolean };
export type ModelDTO = { model_name: string; display_name?: string; upstream_model?: string; model_ref: string; reasoning_effort?: string; reasoning_efforts?: string[] };
export type ProviderModelsDTO = { auth_type?: string; model_list: ModelDTO[] };
export type ProviderListModelDTO = { current_model: string; current_reasoning_effort?: string; providers: ProviderModelsDTO[] };
export type SwitchModelDTO = { model_ref: string; reasoning_effort?: string };
export type AddModelInput = { provider: string; model: string; api_key: string; base_url: string; context_window: number; max_output_tokens: number; reasoning_effort?: string };
export type ChatGPTLoginDTO = { login_id: string; authorization_url?: string; status: "pending" | "connected" | "failed"; error?: string; expires_at: string };
export type TokenStatistics = { token_input: number; token_output: number };
export type ContextData = { window_token: TokenStatistics; context_window: number };
export type EvaluationJobStatus = "queued" | "running" | "succeeded" | "failed";
export type EvaluationJobDTO = { id: string; source_session_id: string; requirement: string; status: EvaluationJobStatus; report_path?: string; error?: string; created_at: string; updated_at: string; completed_at?: string };
export type EvaluationJobListDTO = { jobs: EvaluationJobDTO[] };
export type ApprovalKind = "bash_command" | "skill_write" | "file_path";
export type ApprovalRequiredData = { approval_id: string; session_id: string; kind: ApprovalKind; content: string; tool?: string; risk?: string; command?: string };
export type RetryAction = "resume" | "resend";
export type ErrorPayload = { code: string; message: string; retry_action?: RetryAction };

export type StreamEvent =
  | { type: "start"; data: { session_id: string } }
  | { type: "reasoning"; data: { delta: string } }
  | { type: "message"; data: { delta: string } }
  | { type: "tool_call"; data: { info: string; window_token?: TokenStatistics; context_window?: number } }
  | { type: "done"; data: { session_id: string; result: string; token_used: TokenStatistics; window_token: TokenStatistics; context_window: number } }
  | { type: "approval_required"; data: ApprovalRequiredData }
  | { type: "error"; data: ErrorPayload };
