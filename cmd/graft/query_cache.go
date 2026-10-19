package main

import (
	"cmp"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/h0rn3t/Graft/internal/graph"
)

// queryCacheBudget bounds the decoded graphs and ask indexes one server keeps,
// as estimated by queryCacheEntry.size.
const queryCacheBudget = 256 << 20

// queryCache shares immutable query data within one MCP server.
type queryCache struct {
	mu      sync.Mutex
	entries []queryCacheEntry
	budget  int64 // zero means queryCacheBudget
	// memo carries the server's refreshes into each other and hands the graph
	// and ask index a refresh built straight to the next load.
	memo graph.RefreshMemo
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
		if statErr == nil {
			// What this server's last refresh built needs no decoding, and while
			// its files are being written it is newer than any graph cached here.
			if written, writtenIndex, ok := cache.memo.Written(dir, graphFile, indexFile); ok {
				cache.entries = slices.DeleteFunc(cache.entries, func(entry queryCacheEntry) bool { return entry.dir == dir })
				return written, writtenIndex, nil
			}
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
		entry := queryCacheEntry{dir: dir, graphFile: graphFile, indexFile: indexFile, wiring: wiring, index: index, indexLoaded: withIndex}
		// A workspace query loads every child in turn, so evicting the least
		// recently used would miss on every child once they outgrow the budget;
		// a child that does not fit is decoded per query and the cached ones stay.
		retained := entry.size()
		for _, cached := range cache.entries {
			retained += cached.size()
		}
		if len(cache.entries) == 0 || retained <= cmp.Or(cache.budget, queryCacheBudget) {
			cache.entries = append(cache.entries, entry)
		}
		return wiring, index, nil
	}
	return nil, nil, errors.New("query files changed during loading; retry the query")
}

// size estimates the entry's heap from its files: a decoded graph takes less
// than its JSON, while an ask index takes about four times its JSON.
func (entry queryCacheEntry) size() int64 {
	size := entry.graphFile.Size()
	if entry.indexFile != nil {
		size += 4 * entry.indexFile.Size()
	}
	return size
}

func sameQueryFile(before, after os.FileInfo) bool {
	if before == nil || after == nil {
		return before == nil && after == nil
	}
	return os.SameFile(before, after) && before.Size() == after.Size() && before.ModTime().Equal(after.ModTime())
}
