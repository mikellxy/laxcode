type ComposerKeyEvent = {
  key: string;
  shiftKey: boolean;
  isComposing: boolean;
  keyCode: number;
};

// keyCode 229 is kept as a WebKit/IME fallback: some browsers report the
// composition state inconsistently while a candidate is being confirmed.
export function shouldSubmitOnKeyDown(event: ComposerKeyEvent): boolean {
  return event.key === "Enter" && !event.shiftKey && !event.isComposing && event.keyCode !== 229;
}
