export type ProjectDTO = { project_id: string; user_id: string; name: string; work_dir: string; created_at: string; updated_at: string };
export type ProjectListDTO = { projects: ProjectDTO[] };
export type SessionDTO = { session_id: string; user_id: string; project_id: string; title: string; work_dir: string; created_at: string; updated_at: string };
export type SessionPageDTO = { sessions: SessionDTO[]; next_before_session_id?: string; has_more: boolean };
export type HistoryMessageDTO = { seq: number; role: "user" | "assistant" | "tool"; content?: string; reasoning_content?: string; tool_summary?: string; created_at: string };
export type HistoryPageDTO = { messages: HistoryMessageDTO[]; next_before_seq?: number; has_more: boolean };
export type ModelDTO = { model_name: string; upstream_model?: string; model_ref: string };
export type ProviderModelsDTO = { model_list: ModelDTO[] };
export type ProviderListModelDTO = { current_model: string; providers: ProviderModelsDTO[] };
export type SwitchModelDTO = { model_ref: string };
export type AddModelInput = { provider: string; model: string; api_key: string; base_url: string; context_window: number; max_output_tokens: number };
export type TokenStatistics = { token_input: number; token_output: number };
export type ContextData = { window_token: TokenStatistics; context_window: number };
export type ApprovalRequiredData = { approval_id: string; session_id: string; kind: "token_budget" | "bash_command" | "skill_write"; content: string };
export type RetryAction = "resume" | "resend";
export type ErrorPayload = { code: string; message: string; retry_action?: RetryAction };

export type StreamEvent =
  | { type: "start"; data: { session_id: string } }
  | { type: "reasoning"; data: { delta: string } }
  | { type: "message"; data: { delta: string } }
  | { type: "tool_call"; data: { info: string } }
  | { type: "done"; data: { session_id: string; result: string; token_used: TokenStatistics; window_token: TokenStatistics; context_window: number } }
  | { type: "approval_required"; data: ApprovalRequiredData }
  | { type: "error"; data: ErrorPayload };
