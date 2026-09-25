package main

import (
	"io"

	"github.com/h0rn3t/Graft/internal/savings"
)

// countingWriter counts the bytes a query command prints, so its saving
// against reading the covered files whole can be recorded.
type countingWriter struct {
	io.Writer
	n int
}

func (w *countingWriter) Write(p []byte) (int, error) {
	n, err := w.Writer.Write(p)
	w.n += n
	return n, err
}

// recordQuerySavings leaves what a query saved in the pending ledger, which the
// agent hook credits to the session statusline at Stop.
func recordQuerySavings(contextDir string, printed, baselineChars int) {
	savings.RecordPending(contextDir, savings.Tokens(baselineChars)-savings.Tokens(printed))
}
