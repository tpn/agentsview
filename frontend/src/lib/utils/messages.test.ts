import { describe, it, expect } from "vitest";
import { isSystemMessage } from "./messages.js";
import type { Message } from "../api/types.js";

function msg(overrides: Partial<Message>): Message {
  return {
    id: 1,
    session_id: "s1",
    ordinal: 0,
    role: "user",
    content: "hello",
    timestamp: "2025-01-01T00:00:00Z",
    has_thinking: false,
    has_tool_use: false,
    content_length: 5,
    model: "",
    token_usage: null,
    context_tokens: 0,
    output_tokens: 0,
    is_system: false,
    ...overrides,
  };
}

describe("isSystemMessage", () => {
  it("returns true when is_system flag is set", () => {
    expect(isSystemMessage(msg({ is_system: true }))).toBe(true);
  });

  it("returns true for is_system regardless of role", () => {
    expect(
      isSystemMessage(msg({ is_system: true, role: "assistant" })),
    ).toBe(true);
  });

  it("returns false for normal user message", () => {
    expect(
      isSystemMessage(msg({ role: "user", content: "fix the bug" })),
    ).toBe(false);
  });

  it("returns false for assistant message without is_system", () => {
    expect(
      isSystemMessage(msg({ role: "assistant", content: "sure" })),
    ).toBe(false);
  });

  it.each([
    ["continuation", "This session is being continued from a previous..."],
    ["interrupted", "[Request interrupted by user]"],
    ["task-notification", "<task-notification>done</task-notification>"],
    ["subagent-notification", "<subagent_notification>{\"agent_id\":\"abc\"}</subagent_notification>"],
    ["command-message", "<command-message>commit</command-message>"],
    ["command-name", "<command-name>/commit</command-name>"],
    ["local-command", "<local-command-output>ok</local-command-output>"],
    ["stop hook", "Stop hook feedback: blocked"],
  ])("detects prefix-based system message: %s", (_label, content) => {
    expect(isSystemMessage(msg({ content }))).toBe(true);
  });

  it("does not match partial prefix", () => {
    expect(
      isSystemMessage(msg({ content: "This session is great" })),
    ).toBe(false);
  });
});
