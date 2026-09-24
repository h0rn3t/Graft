// Package savings renders the "[graft] tokens saved ≈ N" line retrieval output
// opens with, and prices it at the rate the repo's current agent session pays
// for input tokens. It mirrors src/context/savings.ts and src/context/price.ts.
package savings

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"unicode/utf16"

	"github.com/h0rn3t/Graft/internal/jsmath"
)

// inputRate holds the float64 bits of the session's $/Mtok input rate; zero
// means no rate is known. Process-level, like the TypeScript module slot: one
// CLI invocation answers one query for one session.
var inputRate atomic.Uint64

// SetInputRate records what an input token costs here. Anything but a positive
// finite number clears it, so a zero-denominator rate renders as "no dollars
// known" rather than as $NaN.
func SetInputRate(usdPerMtok float64) {
	if math.IsNaN(usdPerMtok) || math.IsInf(usdPerMtok, 0) || usdPerMtok <= 0 {
		inputRate.Store(0)
		return
	}
	inputRate.Store(math.Float64bits(usdPerMtok))
}

// ContextDir is the repo's graft directory as the session state sees it:
// GRAFT_DIR when set (relative to root), otherwise root/graft.
func ContextDir(root string) string {
	override := os.Getenv("GRAFT_DIR")
	if override == "" {
		return filepath.Join(root, "graft")
	}
	if filepath.IsAbs(override) {
		return override
	}
	return filepath.Join(root, override)
}

// SessionDir holds one JSON state file per agent session.
func SessionDir(root string) string {
	return filepath.Join(ContextDir(root), ".cache", "session")
}

// LatestSession returns the id and raw JSON of the most recently modified
// session file, or ok=false when none exists.
func LatestSession(root string) (id string, data []byte, ok bool) {
	dir := SessionDir(root)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return "", nil, false
	}
	var bestName string
	var bestTime int64
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		info, err := os.Stat(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		if mtime := info.ModTime().UnixNano(); bestName == "" || mtime > bestTime {
			bestName, bestTime = entry.Name(), mtime
		}
	}
	if bestName == "" {
		return "", nil, false
	}
	data, err = os.ReadFile(filepath.Join(dir, bestName))
	if err != nil {
		data = nil
	}
	return strings.TrimSuffix(bestName, ".json"), data, true
}

// SessionInputRate is what the repo's most recent session has been paying per
// million input tokens, or 0 when no turn has been billed yet.
func SessionInputRate(root string) float64 {
	_, data, ok := LatestSession(root)
	if !ok {
		return 0
	}
	var session struct {
		InputCostMicros   any `json:"inputCostMicros"`
		InputTokensBilled any `json:"inputTokensBilled"`
	}
	if json.Unmarshal(data, &session) != nil {
		return 0
	}
	cost, costOK := session.InputCostMicros.(float64)
	billed, billedOK := session.InputTokensBilled.(float64)
	if !costOK || !billedOK || cost == 0 || billed == 0 {
		return 0
	}
	return cost / billed
}

// DollarsSaved prices savedTokens from a session's billing totals; ok is false
// when nothing has been billed, nothing was saved, or the result is not finite.
func DollarsSaved(savedTokens, costMicros, tokensBilled float64) (float64, bool) {
	if costMicros == 0 || tokensBilled == 0 || math.IsNaN(costMicros) || math.IsNaN(tokensBilled) || !(savedTokens > 0) {
		return 0, false
	}
	usd := savedTokens * (costMicros / tokensBilled) / 1e6
	if math.IsNaN(usd) || math.IsInf(usd, 0) {
		return 0, false
	}
	return usd, true
}

// FormatDollars renders $1.23, or <$0.01 for a real but sub-cent saving.
func FormatDollars(usd float64) string {
	if usd < 0.01 {
		return "<$0.01"
	}
	return "$" + strconv.FormatFloat(usd, 'f', 2, 64)
}

// Tokens estimates tokens for a UTF-16 length at about four chars a token.
func Tokens(chars int) int {
	return int(jsmath.Round(float64(chars) / 4))
}

// Length is a string's JavaScript length in UTF-16 code units.
func Length(text string) int {
	n := 0
	for _, r := range text {
		n += utf16.RuneLen(r)
	}
	return n
}

// Group renders an integer the way en-US toLocaleString does: 12,345.
func Group(value int) string {
	sign := ""
	if value < 0 {
		sign, value = "-", -value
	}
	digits := strconv.Itoa(value)
	for i := len(digits) - 3; i > 0; i -= 3 {
		digits = digits[:i] + "," + digits[i:]
	}
	return sign + digits
}

// TurnNudge asks the agent to report the turn's running total, carrying this
// call's dollar value once a session rate is known.
func TurnNudge(savedTokens int) string {
	const sum = " — sum each such line across your graft calls — e.g. "
	rate := math.Float64frombits(inputRate.Load())
	if rate == 0 || savedTokens <= 0 {
		return " At the end of your reply, tell the user the total graft tokens saved this turn" + sum + `"🌱 graft saved ~N tokens this turn".`
	}
	usd := float64(savedTokens) * rate / 1e6
	return fmt.Sprintf(" This call is worth %s at the rate this session is actually paying for input tokens. At the end of your reply, tell the user the total graft tokens saved this turn and what they were worth%s\"🌱 graft saved ~N tokens (~$X) this turn\".", FormatDollars(usd), sum)
}

// Line is the one-line savings estimate for a rendered body covering files
// whose sources total baselineChars, or "" when there is nothing honest to
// claim.
func Line(body string, files, baselineChars int) string {
	if baselineChars <= 0 {
		return ""
	}
	pack, base := Tokens(Length(body)), Tokens(baselineChars)
	if base <= pack {
		return ""
	}
	delta := base - pack
	return fmt.Sprintf("[graft] tokens saved ≈ %s (%d%%) — this output ≈ %s tok vs reading the %d file(s) it covers whole ≈ %s tok (estimate).", Group(delta), percent(delta, base), Group(pack), files, Group(base)) + TurnNudge(delta)
}

// With renders body with its savings line on top: a header survives the
// head -N and host truncation that would eat a footer.
func With(body string, files, baselineChars int) string {
	if line := Line(body, files, baselineChars); line != "" {
		return line + "\n\n" + body
	}
	return body
}

// AskLine is ask's variant of Line, worded for a retrieval pack.
func AskLine(body string, files, baselineChars int) string {
	if baselineChars <= 0 {
		return ""
	}
	pack, base := Tokens(Length(body)), Tokens(baselineChars)
	if base <= pack {
		return ""
	}
	saved := base - pack
	return fmt.Sprintf("[graft] tokens saved ≈ %s (%d%%) — this pack ≈ %s tok vs reading the %d source file(s) whole ≈ %s tok. Estimate (baseline = those files read in full).", Group(saved), percent(saved, base), Group(pack), files, Group(base)) + TurnNudge(saved)
}

func percent(part, whole int) int {
	return int(jsmath.Round(float64(float64(part) / float64(whole) * 100)))
}
