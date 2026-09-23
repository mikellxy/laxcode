import type { ChatMessage } from "../../types/chat";

// MessageBlock 是消息列表的渲染单元：单条 user/assistant 消息，或同属一个
// assistant 轮次的 tool 消息分组。后端在单个 LLM 轮次里发出的多次工具调用
// 对应流式/历史消息里连续的 tool 消息，因此以「连续」作为归属同一 assistant
// 消息的判据。
export type MessageBlock =
  | { kind: "single"; message: ChatMessage }
  | { kind: "tools"; messages: ChatMessage[] };

export const groupMessages = (messages: ChatMessage[]): MessageBlock[] => {
  const blocks: MessageBlock[] = [];
  for (let i = 0; i < messages.length; i++) {
    if (messages[i].role !== "tool") {
      blocks.push({ kind: "single", message: messages[i] });
      continue;
    }
    const group: ChatMessage[] = [];
    while (i < messages.length && messages[i].role === "tool") group.push(messages[i++]);
    i--;
    blocks.push({ kind: "tools", messages: group });
  }
  return blocks;
};
