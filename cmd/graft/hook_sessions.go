package main

import (
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/NanoNets/context-graph-engine/internal/telemetry"
)

const (
	hookSessionIdle       = 2 * time.Hour
	hookSessionSweepLimit = 20
)

func hookIntValue(value *int) int {
	if value == nil {
		return 0
	}
	return *value
}

func summarizeHookSession(root, id, home, fallbackHost string) int {
	if !telemetry.On(home) {
		return 0
	}
	session := readHookSession(root, id)
	if session.Summarized != nil && *session.Summarized {
		return 0
	}
	host := fallbackHost
	if session.Host != nil && *session.Host != "" {
		host = *session.Host
	}
	telemetry.Track("session_summary", []telemetry.Property{
		{Key: "graft_reads_bucket", Value: telemetry.CountBucket(session.GraftReads)},
		{Key: "source_reads_bucket", Value: telemetry.CountBucket(session.SourceReads)},
		{Key: "saved_tokens_bucket", Value: telemetry.SavedTokensBucket(session.SavedTokens)},
		{Key: "graft_turns_bucket", Value: telemetry.CountBucket(hookIntValue(session.GraftTurns))},
		{Key: "reported_turns_bucket", Value: telemetry.CountBucket(hookIntValue(session.ReportedTurns))},
	}, telemetry.Context{Repo: root, Host: host, Home: home, Version: currentVersion()})
	session.Summarized = new(true)
	if err := writeHookSession(root, id, session); err != nil {
		return 0
	}
	return 1
}

func flushClosedHookSessions(root string, now time.Time, home, fallbackHost string) int {
	if !telemetry.On(home) {
		return 0
	}
	ids := listHookSessionIDs(root)
	slices.Sort(ids)
	queued := 0
	for _, id := range ids {
		if queued >= hookSessionSweepLimit {
			break
		}
		info, err := os.Stat(filepath.Join(hookSessionDir(root), id+".json"))
		if err != nil || now.Sub(info.ModTime()) < hookSessionIdle {
			continue
		}
		queued += summarizeHookSession(root, id, home, fallbackHost)
	}
	return queued
}
