package main

import (
	"bytes"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"
	"math"
	"os"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

const hookTranscriptTailBytes = 1 << 20

// hookLenientJSON decodes what hosts send the way JSON.parse does: a lone
// surrogate escape or invalid UTF-8 becomes U+FFFD and a repeated key keeps
// its last value, instead of the whole document being rejected.
var hookLenientJSON = json.JoinOptions(jsontext.AllowInvalidUTF8(true), jsontext.AllowDuplicateNames(true))

var hookSavingsTallyPattern = regexp.MustCompile(`(?i)graft\s+saved\s*[~≈]?\s*[\d,.]+\s*[km]?\s*(tok|tokens)`)

type hookTranscriptEntry struct {
	Type        string                `json:"type"`
	UUID        *string               `json:"uuid"`
	IsSidechain bool                  `json:"isSidechain"`
	IsMeta      bool                  `json:"isMeta"`
	Message     hookTranscriptMessage `json:"message"`
}

type hookTranscriptMessage struct {
	ID      *string        `json:"id"`
	Model   string         `json:"model"`
	Content any            `json:"content"`
	Usage   *hookTurnUsage `json:"usage"`
}

type hookTurnUsage struct {
	Input       any `json:"input_tokens"`
	CacheCreate any `json:"cache_creation_input_tokens"`
	CacheRead   any `json:"cache_read_input_tokens"`
}

type hookAssistantTurn struct {
	UUID string
	Text string
}

type hookTurnBilling struct {
	UUID       string
	CostMicros int
	Tokens     int
}

type hookUsage struct {
	Model       string
	Input       float64
	CacheCreate float64
	CacheRead   float64
}

func hasSavingsTally(text string) bool {
	return hookSavingsTallyPattern.MatchString(text)
}

func inputUSDPerMtok(model string) (float64, bool) {
	switch {
	case strings.HasPrefix(model, "claude-fable-5"), strings.HasPrefix(model, "claude-mythos-5"):
		return 10, true
	// claude-opus-5-5 must match before the claude-opus-5 prefix it shares.
	case strings.HasPrefix(model, "claude-opus-5-5"):
		return 4, true
	case strings.HasPrefix(model, "claude-opus-5"), strings.HasPrefix(model, "claude-opus-4-5"), strings.HasPrefix(model, "claude-opus-4-6"), strings.HasPrefix(model, "claude-opus-4-7"), strings.HasPrefix(model, "claude-opus-4-8"):
		return 5, true
	case strings.HasPrefix(model, "claude-opus-4-1"):
		return 15, true
	case strings.HasPrefix(model, "claude-sonnet-5"):
		return 2, true
	case strings.HasPrefix(model, "claude-sonnet-4-5"), strings.HasPrefix(model, "claude-sonnet-4-6"):
		return 3, true
	case strings.HasPrefix(model, "claude-haiku-4-5"):
		return 1, true
	default:
		return 0, false
	}
}

func turnInputCostMicros(usage hookUsage) (int, bool) {
	price, ok := inputUSDPerMtok(usage.Model)
	if !ok {
		return 0, false
	}
	weighted := usage.Input + usage.CacheCreate*1.25 + usage.CacheRead*0.1
	cost := math.Round(weighted * price)
	if math.IsNaN(cost) || math.IsInf(cost, 0) || cost > float64(int(^uint(0)>>1)) || cost < float64(-int(^uint(0)>>1)-1) {
		return 0, false
	}
	return int(cost), true
}

func turnInputTokens(usage hookUsage) int {
	return int(usage.Input + usage.CacheCreate + usage.CacheRead)
}

func readHookTranscriptTail(path string) string {
	file, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return ""
	}
	length := min(info.Size(), int64(hookTranscriptTailBytes))
	data := make([]byte, length)
	if length > 0 {
		if _, err := file.ReadAt(data, info.Size()-length); err != nil {
			return ""
		}
	}
	if length == info.Size() {
		return string(data)
	}
	tail := string(data)
	if _, after, ok := strings.Cut(tail, "\n"); ok {
		return after
	}
	return tail
}

func hookTranscriptEntries(path string) []hookTranscriptEntry {
	return parseHookTranscript(readHookTranscriptTail(path))
}

// parseHookTranscript decodes transcript JSON lines leniently, skipping lines
// that do not parse.
func parseHookTranscript(data string) []hookTranscriptEntry {
	if data == "" {
		return nil
	}
	var entries []hookTranscriptEntry
	for line := range strings.SplitSeq(data, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		var entry hookTranscriptEntry
		if json.Unmarshal([]byte(line), &entry, hookLenientJSON) == nil {
			entries = append(entries, entry)
		}
	}
	return entries
}

// countHookTranscriptTools adds the tool calls a transcript gained since the
// session last read it to the session's graft and source-read counts. Only
// complete lines are consumed, and a transcript that shrank is read afresh.
func countHookTranscriptTools(root, id, path string) {
	if path == "" {
		return
	}
	file, err := os.Open(path)
	if err != nil {
		return
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return
	}
	_ = updateHookSession(root, id, func(session *sessionState) bool {
		offset := session.TranscriptOffsets[path]
		if offset > info.Size() {
			offset = 0
		}
		data := make([]byte, info.Size()-offset)
		if _, err := file.ReadAt(data, offset); err != nil && !errors.Is(err, io.EOF) {
			return false
		}
		complete := bytes.LastIndexByte(data, '\n') + 1
		if complete == 0 {
			return false
		}
		for _, entry := range parseHookTranscript(string(data[:complete])) {
			blocks, _ := entry.Message.Content.([]any)
			for _, block := range blocks {
				use, _ := block.(map[string]any)
				if use["type"] != "tool_use" {
					continue
				}
				name, _ := use["name"].(string)
				input, _ := use["input"].(map[string]any)
				command, _ := input["command"].(string)
				switch classifyHookToolUse(name, command) {
				case hookToolGraft:
					session.GraftReads++
					session.TurnUsedGraft = new(true)
				case hookToolSource:
					session.SourceReads++
				}
			}
		}
		if session.TranscriptOffsets == nil {
			session.TranscriptOffsets = make(map[string]int64)
		}
		session.TranscriptOffsets[path] = offset + int64(complete)
		return true
	})
}

// seedHookTranscriptOffset starts counting a transcript at its current end, so
// a resumed session does not recount the history it replays.
func seedHookTranscriptOffset(root, id, path string) {
	if path == "" {
		return
	}
	info, err := os.Stat(path)
	if err != nil {
		return
	}
	_ = updateHookSession(root, id, func(session *sessionState) bool {
		if _, ok := session.TranscriptOffsets[path]; ok {
			return false
		}
		if session.TranscriptOffsets == nil {
			session.TranscriptOffsets = make(map[string]int64)
		}
		session.TranscriptOffsets[path] = info.Size()
		return true
	})
}

func isHookUserPrompt(entry hookTranscriptEntry) bool {
	if entry.Type != "user" || entry.IsMeta {
		return false
	}
	switch content := entry.Message.Content.(type) {
	case string:
		return true
	case []any:
		return !slices.ContainsFunc(content, func(part any) bool {
			value, ok := part.(map[string]any)
			return ok && value["type"] == "tool_result"
		})
	default:
		return false
	}
}

func hookAssistantText(content any) string {
	switch value := content.(type) {
	case string:
		return value
	case []any:
		parts := make([]string, 0, len(value))
		for _, item := range value {
			part, ok := item.(map[string]any)
			if !ok || part["type"] != "text" {
				continue
			}
			if text, ok := part["text"].(string); ok {
				parts = append(parts, text)
			}
		}
		return strings.Join(parts, "\n")
	default:
		return ""
	}
}

func lastHookAssistantTurn(entries []hookTranscriptEntry) *hookAssistantTurn {
	var parts []string
	var uuid *string
	for _, entry := range slices.Backward(entries) {
		if entry.IsSidechain {
			continue
		}
		if isHookUserPrompt(entry) {
			break
		}
		if entry.Type != "assistant" {
			continue
		}
		text := hookAssistantText(entry.Message.Content)
		if strings.TrimSpace(text) == "" {
			continue
		}
		if uuid == nil {
			uuid = entry.UUID
		}
		parts = append(parts, text)
	}
	if uuid == nil || len(parts) == 0 {
		return nil
	}
	slices.Reverse(parts)
	return &hookAssistantTurn{UUID: *uuid, Text: strings.Join(parts, "\n")}
}

func hookNumber(value any) float64 {
	switch number := value.(type) {
	case float64:
		return number
	case string:
		parsed, err := strconv.ParseFloat(number, 64)
		if err == nil {
			return parsed
		}
	}
	return 0
}

func lastHookTurnBilling(entries []hookTranscriptEntry) *hookTurnBilling {
	seen := make(map[string]struct{})
	var uuid *string
	costMicros := 0
	tokens := 0
	for _, entry := range slices.Backward(entries) {
		if entry.IsSidechain {
			continue
		}
		if isHookUserPrompt(entry) {
			break
		}
		if entry.Type != "assistant" {
			continue
		}
		if uuid == nil {
			uuid = entry.UUID
		}
		if entry.Message.ID == nil || entry.Message.Usage == nil {
			continue
		}
		if _, ok := seen[*entry.Message.ID]; ok {
			continue
		}
		seen[*entry.Message.ID] = struct{}{}
		usage := hookUsage{
			Model:       entry.Message.Model,
			Input:       hookNumber(entry.Message.Usage.Input),
			CacheCreate: hookNumber(entry.Message.Usage.CacheCreate),
			CacheRead:   hookNumber(entry.Message.Usage.CacheRead),
		}
		cost, ok := turnInputCostMicros(usage)
		if !ok {
			continue
		}
		costMicros += cost
		tokens += turnInputTokens(usage)
	}
	if uuid == nil || tokens == 0 {
		return nil
	}
	return &hookTurnBilling{UUID: *uuid, CostMicros: costMicros, Tokens: tokens}
}
