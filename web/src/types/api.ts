export type SessionDTO = { session_id: string; user_id: string; title: string; created_at: string; updated_at: string };
export type SessionPageDTO = { sessions: SessionDTO[]; next_before_session_id?: string; has_more: boolean };
export type HistoryMessageDTO = { seq: number; role: "user" | "assistant" | "tool"; content?: string; reasoning_content?: string; tool_summary?: string; created_at: string };
export type HistoryPageDTO = { messages: HistoryMessageDTO[]; next_before_seq?: number; has_more: boolean };
export type ModelDTO = { model_name: string; upstream_model?: string; model_ref: string };
export type ProviderModelsDTO = { model_list: ModelDTO[] };
export type ProviderListModelDTO = { current_model: string; providers: ProviderModelsDTO[] };
export type SwitchModelDTO = { model_ref: string };
export type TokenStatistics = { token_input: number; token_output: number };
export type RetryAction = "resume" | "resend";
export type ErrorPayload = { code: string; message: string; retry_action?: RetryAction };

export type StreamEvent =
  | { type: "start"; data: { session_id: string } }
  | { type: "reasoning"; data: { delta: string } }
  | { type: "message"; data: { delta: string } }
  | { type: "tool_call"; data: { info: string } }
  | { type: "done"; data: { session_id: string; result: string; token_used: TokenStatistics; window_token: TokenStatistics } }
  | { type: "error"; data: ErrorPayload };
