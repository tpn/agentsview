import type { Message } from "../api/types.js";

const SYSTEM_MSG_PREFIXES = [
  "This session is being continued",
  "[Request interrupted",
  "<task-notification>",
  "<subagent_notification>",
  "<command-message>",
  "<command-name>",
  "<local-command-",
  "Stop hook feedback:",
];

/**
 * Returns true if the message is system-injected and should be
 * hidden from the UI. Checks the backend is_system flag first,
 * then falls back to prefix detection for parsers that don't set it.
 */
export function isSystemMessage(m: Message): boolean {
  if (m.is_system) return true;
  if (m.role !== "user") return false;
  const trimmed = m.content.trim();
  return SYSTEM_MSG_PREFIXES.some((p) => trimmed.startsWith(p));
}
