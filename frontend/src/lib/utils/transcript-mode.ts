import type {
  DisplayItem,
  MessageItem,
} from "./display-items.js";
import type { Message } from "../api/types.js";

export function filterDisplayItemsByTranscriptMode(
  items: DisplayItem[],
  mode: "normal" | "focused",
): DisplayItem[] {
  if (mode === "normal") return items;

  const filtered: DisplayItem[] = [];
  let pendingAssistant: MessageItem | null = null;
  let toolAfterPendingAssistant = false;

  for (const item of items) {
    if (item.kind === "tool-group") {
      if (pendingAssistant) {
        toolAfterPendingAssistant = true;
      }
      continue;
    }

    if (item.message.role === "user") {
      if (pendingAssistant && !toolAfterPendingAssistant) {
        filtered.push(pendingAssistant);
      }
      pendingAssistant = null;
      toolAfterPendingAssistant = false;
      filtered.push(item);
      continue;
    }

    pendingAssistant = item;
    toolAfterPendingAssistant = false;
  }

  if (pendingAssistant && !toolAfterPendingAssistant) {
    filtered.push(pendingAssistant);
  }

  return filtered;
}

export function shouldAutoSwitchTranscriptModeToNormal(
  mode: "normal" | "focused",
  ordinal: number | null,
  loadedMessages: Message[],
  visibleItems: DisplayItem[],
): boolean {
  if (mode !== "focused" || ordinal === null) return false;

  const visible = visibleItems.some((item) =>
    item.ordinals.includes(ordinal),
  );
  if (visible) return false;

  return loadedMessages.some((msg) => msg.ordinal === ordinal);
}
