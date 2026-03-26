package parser

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/tidwall/gjson"
)

// Codex JSONL entry types.
const (
	codexTypeSessionMeta  = "session_meta"
	codexTypeResponseItem = "response_item"
	codexTypeTurnContext  = "turn_context"
	codexOriginatorExec   = "codex_exec"
)

var errCodexIncrementalNeedsFullParse = errors.New(
	"codex subagent event requires full parse",
)

// codexSessionBuilder accumulates state while scanning a Codex
// JSONL session file line by line.
type codexSessionBuilder struct {
	messages            []ParsedMessage
	firstMessage        string
	startedAt           time.Time
	endedAt             time.Time
	sessionID           string
	project             string
	ordinal             int
	includeExec         bool
	currentModel        string
	callNames           map[string]string
	subagentMap         map[string]string
	agentNames          map[string]string
	agentCalls          map[string]string
	agentResults        map[string]bool
	pendingAgentResults map[string]codexPendingResult
	callResults         map[string]map[string]codexPendingResult
	callResultIndex     map[string]int
}

type codexPendingResult struct {
	text      string
	timestamp time.Time
	ordinal   int
}

func newCodexSessionBuilder(
	includeExec bool,
) *codexSessionBuilder {
	return &codexSessionBuilder{
		project:             "unknown",
		includeExec:         includeExec,
		callNames:           make(map[string]string),
		subagentMap:         make(map[string]string),
		agentNames:          make(map[string]string),
		agentCalls:          make(map[string]string),
		agentResults:        make(map[string]bool),
		pendingAgentResults: make(map[string]codexPendingResult),
		callResults:         make(map[string]map[string]codexPendingResult),
		callResultIndex:     make(map[string]int),
	}
}

// processLine handles a single non-empty, valid JSON line.
// Returns (skip=true) if the session should be discarded
// (e.g. non-interactive codex_exec).
func (b *codexSessionBuilder) processLine(
	line string,
) (skip bool) {
	tsStr := gjson.Get(line, "timestamp").Str
	ts := parseTimestamp(tsStr)
	if ts.IsZero() {
		if tsStr != "" {
			logParseError(tsStr)
		}
	} else {
		if b.startedAt.IsZero() {
			b.startedAt = ts
		}
		b.endedAt = ts
	}

	payload := gjson.Get(line, "payload")

	switch gjson.Get(line, "type").Str {
	case codexTypeSessionMeta:
		return b.handleSessionMeta(payload)
	case codexTypeTurnContext:
		b.currentModel = payload.Get("model").Str
	case codexTypeResponseItem:
		b.handleResponseItem(payload, ts)
	}
	return false
}

func (b *codexSessionBuilder) handleSessionMeta(
	payload gjson.Result,
) (skip bool) {
	b.sessionID = payload.Get("id").Str

	if cwd := payload.Get("cwd").Str; cwd != "" {
		branch := payload.Get("git.branch").Str
		if proj := ExtractProjectFromCwdWithBranch(cwd, branch); proj != "" {
			b.project = proj
		} else {
			b.project = "unknown"
		}
	}

	if !b.includeExec &&
		payload.Get("originator").Str == codexOriginatorExec {
		return true
	}
	return false
}

func (b *codexSessionBuilder) handleResponseItem(
	payload gjson.Result, ts time.Time,
) {
	switch payload.Get("type").Str {
	case "function_call":
		b.handleFunctionCall(payload, ts)
		return
	case "function_call_output":
		b.handleFunctionCallOutput(payload, ts)
		return
	}

	role := payload.Get("role").Str
	if role != "user" && role != "assistant" {
		return
	}

	content := extractCodexContent(payload)
	if strings.TrimSpace(content) == "" {
		return
	}

	if role == "user" && b.handleSubagentNotification(content, ts) {
		return
	}

	if role == "user" && isCodexSystemMessage(content) {
		return
	}

	if role == "user" && b.firstMessage == "" {
		b.firstMessage = truncate(
			strings.ReplaceAll(content, "\n", " "), 300,
		)
	}

	b.messages = append(b.messages, ParsedMessage{
		Ordinal:       b.ordinal,
		Role:          RoleType(role),
		Content:       content,
		Timestamp:     ts,
		ContentLength: len(content),
		Model:         b.currentModel,
	})
	b.ordinal++
}

func (b *codexSessionBuilder) handleFunctionCall(
	payload gjson.Result, ts time.Time,
) {
	name := payload.Get("name").Str
	if name == "" {
		return
	}
	callID := payload.Get("call_id").Str
	if callID != "" {
		b.callNames[callID] = name
	}

	content := formatCodexFunctionCall(name, payload)
	inputJSON := extractCodexInputJSON(payload)
	waitAgentIDs := []string(nil)
	if name == "wait" && callID != "" {
		args, _ := parseCodexFunctionArgs(payload)
		waitAgentIDs = codexWaitAgentIDs(args)
	}

	b.messages = append(b.messages, ParsedMessage{
		Ordinal:       b.ordinal,
		Role:          RoleAssistant,
		Content:       content,
		Timestamp:     ts,
		HasToolUse:    true,
		ContentLength: len(content),
		Model:         b.currentModel,
		ToolCalls: []ParsedToolCall{{
			ToolUseID: callID,
			ToolName:  name,
			Category:  NormalizeToolCategory(name),
			InputJSON: inputJSON,
		}},
	})
	b.ordinal++

	if name == "wait" && callID != "" {
		for _, agentID := range waitAgentIDs {
			b.agentCalls[agentID] = callID
			if pending, ok := b.pendingAgentResults[agentID]; ok && !b.agentResults[agentID] {
				b.setCallResult(callID, agentID, pending.text, pending.timestamp)
				b.agentResults[agentID] = true
				delete(b.pendingAgentResults, agentID)
			}
		}
	}
}

func (b *codexSessionBuilder) handleFunctionCallOutput(
	payload gjson.Result, ts time.Time,
) {
	callID := payload.Get("call_id").Str
	if callID == "" {
		return
	}

	output, _ := parseCodexFunctionOutput(payload)
	if !output.Exists() {
		return
	}

	switch b.callNames[callID] {
	case "spawn_agent":
		agentID := strings.TrimSpace(output.Get("agent_id").Str)
		if agentID == "" {
			return
		}
		b.subagentMap[callID] = "codex:" + agentID
		b.agentCalls[agentID] = callID
		if nickname := strings.TrimSpace(output.Get("nickname").Str); nickname != "" {
			b.agentNames[agentID] = nickname
		}
	case "wait":
		status := output.Get("status")
		if !status.Exists() || !status.IsObject() {
			return
		}
		for agentID, entry := range status.Map() {
			if b.agentResults[agentID] {
				continue
			}
			text := codexTerminalSubagentStatus(entry)
			if text == "" {
				continue
			}
			b.setCallResult(callID, agentID, text, ts)
			b.agentResults[agentID] = true
		}
	}
}

func (b *codexSessionBuilder) handleSubagentNotification(
	content string, ts time.Time,
) bool {
	agentID, text := parseCodexSubagentNotification(content)
	if agentID == "" || text == "" {
		return false
	}
	if b.agentResults[agentID] {
		return true
	}
	callID := b.agentCalls[agentID]
	if callID != "" && b.callNames[callID] == "wait" {
		b.setCallResult(callID, agentID, text, ts)
		b.agentResults[agentID] = true
		return true
	}
	b.pendingAgentResults[agentID] = codexPendingResult{
		text:      text,
		timestamp: ts,
		ordinal:   b.ordinal,
	}
	b.ordinal++
	return true
}

func (b *codexSessionBuilder) setCallResult(
	callID, agentID, text string, ts time.Time,
) {
	b.setCallResultAt(callID, agentID, text, ts, -1)
}

func (b *codexSessionBuilder) setCallResultAt(
	callID, agentID, text string, ts time.Time, ordinal int,
) {
	if callID == "" {
		return
	}
	if b.callResults[callID] == nil {
		b.callResults[callID] = make(map[string]codexPendingResult)
	}
	b.callResults[callID][agentID] = codexPendingResult{
		text:      text,
		timestamp: ts,
	}

	formatted := formatCodexCallResults(
		b.callResults[callID], b.agentNames,
	)
	if idx, ok := b.callResultIndex[callID]; ok {
		tr := &b.messages[idx].ToolResults[0]
		tr.ContentLength = len(formatted)
		tr.ContentRaw = strconv.Quote(formatted)
		if ts.After(b.messages[idx].Timestamp) {
			b.messages[idx].Timestamp = ts
		}
		return
	}

	if ordinal < 0 {
		ordinal = b.ordinal
		b.ordinal++
	}

	msg := ParsedMessage{
		Ordinal:   ordinal,
		Role:      RoleUser,
		Content:   "",
		Timestamp: ts,
		Model:     b.currentModel,
		ToolResults: []ParsedToolResult{{
			ToolUseID:     callID,
			ContentLength: len(formatted),
			ContentRaw:    strconv.Quote(formatted),
		}},
	}
	idx := b.insertMessage(msg)
	b.callResultIndex[callID] = idx
}

func (b *codexSessionBuilder) flushPendingAgentResults() {
	if len(b.pendingAgentResults) == 0 {
		return
	}
	agentIDs := make([]string, 0, len(b.pendingAgentResults))
	for agentID := range b.pendingAgentResults {
		agentIDs = append(agentIDs, agentID)
	}
	sort.Slice(agentIDs, func(i, j int) bool {
		pi := b.pendingAgentResults[agentIDs[i]]
		pj := b.pendingAgentResults[agentIDs[j]]
		if pi.ordinal == pj.ordinal {
			return agentIDs[i] < agentIDs[j]
		}
		return pi.ordinal < pj.ordinal
	})

	for _, agentID := range agentIDs {
		if b.agentResults[agentID] {
			continue
		}
		pending := b.pendingAgentResults[agentID]
		callID := b.agentCalls[agentID]
		if callID != "" {
			b.setCallResultAt(
				callID, agentID,
				pending.text, pending.timestamp,
				pending.ordinal,
			)
		} else {
			b.insertMessage(ParsedMessage{
				Ordinal:       pending.ordinal,
				Role:          RoleUser,
				Content:       pending.text,
				Timestamp:     pending.timestamp,
				Model:         b.currentModel,
				ContentLength: len(pending.text),
			})
		}
		b.agentResults[agentID] = true
	}
}

func (b *codexSessionBuilder) insertMessage(msg ParsedMessage) int {
	idx := len(b.messages)
	for i, existing := range b.messages {
		if existing.Ordinal > msg.Ordinal {
			idx = i
			break
		}
	}
	b.messages = append(b.messages, ParsedMessage{})
	copy(b.messages[idx+1:], b.messages[idx:])
	b.messages[idx] = msg
	for callID, cur := range b.callResultIndex {
		if cur >= idx {
			b.callResultIndex[callID] = cur + 1
		}
	}
	return idx
}

func formatCodexFunctionCall(
	name string, payload gjson.Result,
) string {
	summary := sanitizeToolLabel(payload.Get("summary").Str)
	args, rawArgs := parseCodexFunctionArgs(payload)

	switch name {
	case "exec_command", "shell_command", "shell":
		return formatCodexBashCall(summary, args, rawArgs)
	case "write_stdin":
		return formatCodexWriteStdinCall(summary, args, rawArgs)
	case "apply_patch":
		return formatCodexApplyPatchCall(summary, args, rawArgs)
	case "spawn_agent":
		return formatCodexSpawnAgentCall(summary, args, rawArgs)
	}

	category := NormalizeToolCategory(name)
	if category == "Other" {
		header := formatToolHeader("Tool", name)
		if summary != "" {
			return header + "\n" + summary
		}
		if preview := codexArgPreview(args, rawArgs); preview != "" {
			return header + "\n" + preview
		}
		return header
	}

	detail := firstNonEmpty(summary,
		codexCategoryDetail(category, args))
	header := formatToolHeader(category, detail)
	if preview := codexArgPreview(args, rawArgs); preview != "" {
		return header + "\n" + preview
	}
	return header
}

func parseCodexFunctionArgs(
	payload gjson.Result,
) (gjson.Result, string) {
	for _, key := range []string{"arguments", "input"} {
		arg := payload.Get(key)
		if !arg.Exists() {
			continue
		}

		switch arg.Type {
		case gjson.String:
			s := strings.TrimSpace(arg.Str)
			if s == "" {
				continue
			}
			if gjson.Valid(s) {
				return gjson.Parse(s), ""
			}
			return gjson.Result{}, s
		default:
			if arg.IsObject() {
				if len(arg.Map()) == 0 {
					continue
				}
				return arg, ""
			}
			if arg.IsArray() {
				if len(arg.Array()) == 0 {
					continue
				}
				return arg, ""
			}
			raw := strings.TrimSpace(arg.Raw)
			if raw == "" {
				continue
			}
			if gjson.Valid(raw) {
				return gjson.Parse(raw), ""
			}
			return gjson.Result{}, raw
		}
	}
	return gjson.Result{}, ""
}

// extractCodexInputJSON returns the raw JSON string of the
// function call arguments from the payload. It checks
// "arguments" then "input", normalizing string-encoded JSON
// to an object string.
func extractCodexInputJSON(payload gjson.Result) string {
	for _, key := range []string{"arguments", "input"} {
		arg := payload.Get(key)
		if !arg.Exists() {
			continue
		}

		switch arg.Type {
		case gjson.String:
			s := strings.TrimSpace(arg.Str)
			if s == "" {
				continue
			}
			if gjson.Valid(s) {
				if s == "{}" || s == "[]" {
					continue
				}
				return s
			}
			return arg.Str
		default:
			raw := strings.TrimSpace(arg.Raw)
			if raw == "" || raw == "{}" || raw == "[]" {
				continue
			}
			return arg.Raw
		}
	}
	return ""
}

func formatCodexBashCall(
	summary string, args gjson.Result, rawArgs string,
) string {
	cmd := codexArgValue(args, "cmd", "command")
	if cmd == "" && rawArgs != "" && !gjson.Valid(rawArgs) {
		cmd = rawArgs
	}
	if cmd == "" && args.Type == gjson.String {
		cmd = strings.TrimSpace(args.Str)
	}

	header := formatToolHeader("Bash", summary)
	if cmd != "" {
		firstLine, _, hasMore := strings.Cut(cmd, "\n")
		if hasMore {
			return header + "\n$ " + firstLine
		}
		return header + "\n$ " + cmd
	}
	if preview := codexArgPreview(args, rawArgs); preview != "" {
		return header + "\n" + preview
	}
	return header
}

func formatCodexWriteStdinCall(
	summary string, args gjson.Result, rawArgs string,
) string {
	if summary == "" {
		if sid := codexArgValue(args, "session_id"); sid != "" {
			summary = "stdin -> " + sid
		} else {
			summary = "stdin"
		}
	}

	header := formatToolHeader("Bash", summary)
	chars := codexArgString(args, "chars")
	if chars != "" {
		quoted := strings.Trim(
			strconv.QuoteToASCII(chars), "\"",
		)
		return header + "\n" + truncate(quoted, 220)
	}

	if preview := codexArgPreview(args, rawArgs); preview != "" {
		return header + "\n" + preview
	}
	return header
}

func formatCodexApplyPatchCall(
	summary string, args gjson.Result, rawArgs string,
) string {
	patch := codexArgString(args, "patch")
	if patch == "" && strings.Contains(rawArgs, "*** Begin Patch") {
		patch = rawArgs
	}

	files := extractPatchedFiles(patch)
	if summary == "" {
		summary = summarizePatchedFiles(files)
	}

	header := formatToolHeader("Edit", summary)
	if len(files) > 1 {
		limit := min(len(files), 6)
		body := strings.Join(files[:limit], "\n")
		if len(files) > limit {
			body += fmt.Sprintf("\n+%d more files", len(files)-limit)
		}
		return header + "\n" + body
	}
	if preview := codexArgPreview(args, rawArgs); preview != "" &&
		len(files) == 0 {
		return header + "\n" + preview
	}
	return header
}

func formatCodexSpawnAgentCall(
	summary string, args gjson.Result, rawArgs string,
) string {
	if summary == "" {
		summary = firstNonEmpty(
			codexArgValue(args, "agent_type"),
			codexArgValue(args, "subagent_type"),
			"spawn_agent",
		)
	}

	header := formatToolHeader("Task", summary)
	prompt := firstNonEmpty(
		codexArgValue(args, "description"),
		codexArgValue(args, "message"),
		codexArgValue(args, "prompt"),
	)
	if prompt != "" {
		firstLine, _, _ := strings.Cut(prompt, "\n")
		return header + "\n" + truncate(firstLine, 220)
	}
	if preview := codexArgPreview(args, rawArgs); preview != "" {
		return header + "\n" + preview
	}
	return header
}

func extractPatchedFiles(patch string) []string {
	if patch == "" {
		return nil
	}

	var files []string
	seen := make(map[string]struct{})
	for line := range strings.SplitSeq(patch, "\n") {
		for _, prefix := range []string{
			"*** Add File: ",
			"*** Update File: ",
			"*** Delete File: ",
			"*** Move to: ",
		} {
			if !strings.HasPrefix(line, prefix) {
				continue
			}
			file := strings.TrimSpace(
				strings.TrimPrefix(line, prefix),
			)
			if file == "" {
				continue
			}
			if _, ok := seen[file]; ok {
				continue
			}
			seen[file] = struct{}{}
			files = append(files, file)
			break
		}
	}
	return files
}

func summarizePatchedFiles(files []string) string {
	switch len(files) {
	case 0:
		return ""
	case 1:
		return files[0]
	default:
		return fmt.Sprintf(
			"%s (+%d more)",
			files[0], len(files)-1,
		)
	}
}

func codexCategoryDetail(
	category string, args gjson.Result,
) string {
	switch category {
	case "Read", "Write", "Edit":
		return codexArgValue(args, "file_path", "path")
	case "Grep":
		return codexArgValue(args, "pattern")
	case "Glob":
		pattern := codexArgValue(args, "pattern")
		path := codexArgValue(args, "path")
		if pattern != "" && path != "" {
			return fmt.Sprintf("%s in %s", pattern, path)
		}
		return firstNonEmpty(pattern, path)
	case "Task", "Agent":
		desc := codexArgValue(args, "description")
		agent := codexArgValue(args, "subagent_type")
		if desc != "" && agent != "" {
			return fmt.Sprintf("%s (%s)", desc, agent)
		}
		return firstNonEmpty(desc, agent)
	default:
		return ""
	}
}

func codexArgString(
	args gjson.Result, path string,
) string {
	v := args.Get(path)
	if !v.Exists() {
		return ""
	}
	if v.Type == gjson.String {
		return v.Str
	}
	raw := strings.TrimSpace(v.Raw)
	if raw == "" || raw == "null" {
		return ""
	}
	return raw
}

func codexArgValue(
	args gjson.Result, paths ...string,
) string {
	for _, path := range paths {
		v := strings.TrimSpace(codexArgString(args, path))
		if v != "" {
			return v
		}
	}
	return ""
}

func codexArgPreview(
	args gjson.Result, rawArgs string,
) string {
	if rawArgs != "" {
		flat := strings.Join(
			strings.Fields(rawArgs), " ",
		)
		return truncate(flat, 220)
	}
	if args.Exists() {
		flat := strings.Join(
			strings.Fields(args.Raw), " ",
		)
		if flat != "" {
			return truncate(flat, 220)
		}
	}
	return ""
}

func formatToolHeader(
	label, detail string,
) string {
	label = sanitizeToolLabel(label)
	if label == "" {
		label = "Tool"
	}
	detail = sanitizeToolLabel(detail)
	if detail != "" {
		return fmt.Sprintf("[%s: %s]", label, detail)
	}
	return fmt.Sprintf("[%s]", label)
}

func sanitizeToolLabel(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "]", ")")
	return strings.Join(strings.Fields(s), " ")
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		v = strings.TrimSpace(v)
		if v != "" {
			return v
		}
	}
	return ""
}

func parseCodexFunctionOutput(
	payload gjson.Result,
) (gjson.Result, string) {
	out := payload.Get("output")
	if !out.Exists() {
		return gjson.Result{}, ""
	}

	switch out.Type {
	case gjson.String:
		s := strings.TrimSpace(out.Str)
		if s == "" {
			return gjson.Result{}, ""
		}
		if gjson.Valid(s) {
			return gjson.Parse(s), s
		}
		return gjson.Result{}, s
	default:
		raw := strings.TrimSpace(out.Raw)
		if raw == "" {
			return gjson.Result{}, ""
		}
		if gjson.Valid(raw) {
			return gjson.Parse(raw), raw
		}
		return gjson.Result{}, raw
	}
}

func formatCodexCallResults(
	entries map[string]codexPendingResult,
	agentNames map[string]string,
) string {
	if len(entries) == 0 {
		return ""
	}

	parts := make([]string, 0, len(entries))
	ids := make([]string, 0, len(entries))
	for agentID := range entries {
		ids = append(ids, agentID)
	}
	sort.Strings(ids)
	multi := len(entries) > 1
	for _, agentID := range ids {
		text := entries[agentID].text
		if !multi {
			parts = append(parts, text)
			continue
		}
		label := agentID
		if name := strings.TrimSpace(agentNames[agentID]); name != "" {
			label = fmt.Sprintf("%s (%s)", name, agentID)
		}
		parts = append(parts, label+":\n"+text)
	}

	return strings.Join(parts, "\n\n")
}

func codexWaitAgentIDs(args gjson.Result) []string {
	if !args.Exists() {
		return nil
	}
	ids := args.Get("ids")
	if !ids.Exists() || !ids.IsArray() {
		return nil
	}

	var out []string
	for _, item := range ids.Array() {
		id := strings.TrimSpace(item.Str)
		if id == "" {
			continue
		}
		out = append(out, id)
	}
	return out
}

func parseCodexSubagentNotification(
	content string,
) (agentID, text string) {
	if !isCodexSubagentNotification(content) {
		return "", ""
	}
	body := strings.TrimSpace(content)
	body = strings.TrimPrefix(body, "<subagent_notification>")
	body = strings.TrimSuffix(body, "</subagent_notification>")
	body = strings.TrimSpace(body)
	if !gjson.Valid(body) {
		return "", ""
	}
	parsed := gjson.Parse(body)
	agentID = strings.TrimSpace(parsed.Get("agent_id").Str)
	status := parsed.Get("status")
	text = codexTerminalSubagentStatus(status)
	return agentID, text
}

func codexTerminalSubagentStatus(status gjson.Result) string {
	return firstNonEmpty(
		status.Get("completed").Str,
		status.Get("errored").Str,
	)
}

// extractCodexContent joins all text blocks from a Codex
// response item's content array.
func extractCodexContent(payload gjson.Result) string {
	var texts []string
	payload.Get("content").ForEach(
		func(_, block gjson.Result) bool {
			switch block.Get("type").Str {
			case "input_text", "output_text", "text":
				if t := block.Get("text").Str; t != "" {
					texts = append(texts, t)
				}
			}
			return true
		},
	)
	return strings.Join(texts, "\n")
}

// ParseCodexSession parses a Codex JSONL session file.
// Returns nil session if the session is non-interactive and
// includeExec is false.
func ParseCodexSession(
	path, machine string, includeExec bool,
) (*ParsedSession, []ParsedMessage, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, fmt.Errorf("stat %s: %w", path, err)
	}

	f, err := os.Open(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	lr := newLineReader(f, maxLineSize)
	b := newCodexSessionBuilder(includeExec)

	for {
		line, ok := lr.next()
		if !ok {
			break
		}
		if !gjson.Valid(line) {
			continue
		}
		if b.processLine(line) {
			return nil, nil, nil
		}
	}

	if err := lr.Err(); err != nil {
		return nil, nil,
			fmt.Errorf("reading codex %s: %w", path, err)
	}

	b.flushPendingAgentResults()
	annotateSubagentSessions(b.messages, b.subagentMap)

	sessionID := b.sessionID
	if sessionID == "" {
		sessionID = strings.TrimSuffix(
			filepath.Base(path), ".jsonl",
		)
	}
	sessionID = "codex:" + sessionID

	userCount := 0
	for _, m := range b.messages {
		if m.Role == RoleUser && m.Content != "" {
			userCount++
		}
	}

	sess := &ParsedSession{
		ID:               sessionID,
		Project:          b.project,
		Machine:          machine,
		Agent:            AgentCodex,
		FirstMessage:     b.firstMessage,
		StartedAt:        b.startedAt,
		EndedAt:          b.endedAt,
		MessageCount:     len(b.messages),
		UserMessageCount: userCount,
		File: FileInfo{
			Path:  path,
			Size:  info.Size(),
			Mtime: info.ModTime().UnixNano(),
		},
	}

	return sess, b.messages, nil
}

// ParseCodexSessionFrom parses only new lines from a Codex
// JSONL file starting at the given byte offset. Returns only
// the newly parsed messages (with ordinals starting at
// startOrdinal) and the latest timestamp seen. Used for
// incremental re-parsing of large append-only session files.
func ParseCodexSessionFrom(
	path string,
	offset int64,
	startOrdinal int,
	includeExec bool,
) ([]ParsedMessage, time.Time, int64, error) {
	b := newCodexSessionBuilder(includeExec)
	b.ordinal = startOrdinal
	var fallbackErr error

	consumed, err := readJSONLFrom(
		path, offset, func(line string) {
			if fallbackErr != nil {
				return
			}
			// Skip session_meta — already processed in
			// the initial full parse.
			if gjson.Get(line, "type").Str ==
				codexTypeSessionMeta {
				return
			}
			if codexIncrementalNeedsFullParse(line) {
				fallbackErr = errCodexIncrementalNeedsFullParse
				return
			}
			b.processLine(line)
		},
	)
	if err != nil {
		return nil, time.Time{}, 0, fmt.Errorf(
			"reading codex %s from offset %d: %w",
			path, offset, err,
		)
	}
	if fallbackErr != nil {
		return nil, time.Time{}, 0, fallbackErr
	}

	b.flushPendingAgentResults()
	annotateSubagentSessions(b.messages, b.subagentMap)

	return b.messages, b.endedAt, consumed, nil
}

func isCodexSystemMessage(content string) bool {
	return strings.HasPrefix(content, "# AGENTS.md") ||
		strings.HasPrefix(content, "<environment_context>") ||
		strings.HasPrefix(content, "<INSTRUCTIONS>") ||
		isCodexSubagentNotification(content)
}

func isCodexSubagentNotification(content string) bool {
	return strings.HasPrefix(
		strings.TrimSpace(content),
		"<subagent_notification>",
	)
}

func codexIncrementalNeedsFullParse(line string) bool {
	if gjson.Get(line, "type").Str != codexTypeResponseItem {
		return false
	}

	payload := gjson.Get(line, "payload")
	switch payload.Get("type").Str {
	case "function_call_output":
		output, _ := parseCodexFunctionOutput(payload)
		if !output.Exists() {
			return false
		}
		return strings.TrimSpace(output.Get("agent_id").Str) != "" ||
			output.Get("status").Exists()
	default:
		role := payload.Get("role").Str
		if role != "user" {
			return false
		}
		return isCodexSubagentNotification(
			extractCodexContent(payload),
		)
	}
}
