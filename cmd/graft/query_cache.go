package main

import (
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/h0rn3t/Graft/internal/graph"
)

// queryCache shares immutable query data within one MCP server.
type queryCache struct {
	mu      sync.Mutex
	entries []queryCacheEntry
}

type queryCacheEntry struct {
	dir                  string
	graphFile, indexFile os.FileInfo
	wiring               *graph.GraphV1
	index                *graph.AskIndex
	indexLoaded          bool
}

// load is safe for concurrent calls; callers must not mutate returned values.
func (cache *queryCache) load(contextDir string) (*graph.GraphV1, *graph.AskIndex, error) {
	return cache.snapshot(contextDir, true)
}

// loadGraph shares the graph without decoding the optional ranked-search index.
func (cache *queryCache) loadGraph(contextDir string) (*graph.GraphV1, error) {
	wiring, _, err := cache.snapshot(contextDir, false)
	return wiring, err
}

func (cache *queryCache) snapshot(contextDir string, withIndex bool) (*graph.GraphV1, *graph.AskIndex, error) {
	if cache == nil {
		wiring, err := graph.Read(graph.WiringPath(contextDir))
		if err != nil {
			return nil, nil, err
		}
		var index *graph.AskIndex
		if withIndex {
			index = readAskIndex(filepath.Join(contextDir, ".cache", "ask-index.json"))
		}
		return wiring, index, nil
	}
	dir, err := filepath.Abs(contextDir)
	if err != nil {
		return nil, nil, err
	}
	// ponytail: serial loads avoid duplicate decodes; use per-directory locks if contention matters.
	cache.mu.Lock()
	defer cache.mu.Unlock()
	graphPath := graph.WiringPath(dir)
	indexPath := filepath.Join(dir, ".cache", "ask-index.json")
	for range 3 {
		graphFile, statErr := os.Stat(graphPath)
		var indexFile os.FileInfo
		if withIndex {
			indexFile, _ = os.Stat(indexPath) // The sidecar is optional, including when unreadable.
		}
		var wiring *graph.GraphV1
		for i, entry := range cache.entries {
			if entry.dir != dir {
				continue
			}
			cache.entries = slices.Delete(cache.entries, i, i+1)
			if statErr == nil && sameQueryFile(entry.graphFile, graphFile) {
				if !withIndex || (entry.indexLoaded && sameQueryFile(entry.indexFile, indexFile)) {
					cache.entries = append(cache.entries, entry)
					return entry.wiring, entry.index, nil
				}
				if !entry.indexLoaded {
					wiring = entry.wiring
				}
			}
			break
		}
		if statErr != nil {
			return nil, nil, statErr
		}
		var readErr error
		if wiring == nil {
			wiring, readErr = graph.Read(graphPath)
		}
		var index *graph.AskIndex
		if withIndex {
			index = readAskIndex(indexPath)
		}
		graphAfter, graphStatErr := os.Stat(graphPath)
		var indexAfter os.FileInfo
		if withIndex {
			indexAfter, _ = os.Stat(indexPath) // An absent sidecar must invalidate its cached value.
		}
		if graphStatErr != nil || !sameQueryFile(graphFile, graphAfter) || !sameQueryFile(indexFile, indexAfter) {
			continue
		}
		if readErr != nil {
			return nil, nil, readErr
		}
		// Retain at most sixteen workspace children, evicting the least recently used.
		if len(cache.entries) == 16 {
			cache.entries = slices.Delete(cache.entries, 0, 1)
		}
		cache.entries = append(cache.entries, queryCacheEntry{
			dir: dir, graphFile: graphFile, indexFile: indexFile, wiring: wiring, index: index, indexLoaded: withIndex,
		})
		return wiring, index, nil
	}
	return nil, nil, errors.New("query files changed during loading; retry the query")
}

func sameQueryFile(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	return os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}
