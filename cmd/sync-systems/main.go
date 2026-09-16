// sync-systems downloads the NeoAssets system catalog from the
// neostation-frontend repo and writes internal/systems/systems.json so it can
// be embedded into the service binary.
//
// Usage (run from the repo root):
//
//	go run ./cmd/sync-systems
//
// Optional env:
//
//	SYSTEMS_REPO    e.g. "misobadev/neostation-frontend" (default)
//	SYSTEMS_BRANCH  e.g. "main" (default)
//	SYSTEMS_OUT        output path (default "internal/systems/systems.json")
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultRepo   = "misobadev/neostation-frontend"
	defaultBranch = "main"
)

// Files that exist in the repo but are not real systems.
var ignore = map[string]bool{
	"all.json":       true,
	"favorites.json": true,
	"android.json":   true,
}

type systemMeta struct {
	System struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		ShortName string `json:"short_name"`
	} `json:"system"`
}

func main() {
	repo := envOr("SYSTEMS_REPO", defaultRepo)
	branch := envOr("SYSTEMS_BRANCH", defaultBranch)
	out := envOr("SYSTEMS_OUT", filepath.Join("internal", "systems", "systems.json"))

	treeURL := fmt.Sprintf("https://api.github.com/repos/%s/git/trees/%s?recursive=1", repo, branch)
	rawURL := fmt.Sprintf("https://raw.githubusercontent.com/%s/%s/", repo, branch)

	tree, err := fetchJSON(treeURL)
	if err != nil {
		fail("fetch tree: %v", err)
	}

	systemFiles := 0
	var entries []treeEntry
	for _, e := range tree["tree"].([]interface{}) {
		obj := e.(map[string]interface{})
		if obj["type"] != "blob" {
			continue
		}
		path, _ := obj["path"].(string)
		if !strings.HasPrefix(path, "assets/systems/") || !strings.HasSuffix(path, ".json") {
			continue
		}
		name := filepath.Base(path)
		if ignore[name] {
			continue
		}
		entries = append(entries, treeEntry{name: name, url: rawURL + path})
		systemFiles++
	}

	if systemFiles == 0 {
		fail("no system files found")
	}
	fmt.Printf("Found %d systems from %s@%s\n", systemFiles, repo, branch)

	var catalog []map[string]interface{}
	for _, e := range entries {
		data, err := fetch(e.url)
		if err != nil {
			fail("fetch %s: %v", e.name, err)
		}
		var meta systemMeta
		if err := json.Unmarshal(data, &meta); err != nil {
			fail("parse %s: %v", e.name, err)
		}
		if meta.System.ID == "" {
			fmt.Printf("  skipping %s (no system.id)\n", e.name)
			continue
		}
		shortName := meta.System.ShortName
		if shortName == "" {
			shortName = meta.System.Name
		}
		catalog = append(catalog, map[string]interface{}{
			"id":         meta.System.ID,
			"name":       meta.System.Name,
			"short_name": shortName,
		})
	}

	sort.Slice(catalog, func(i, j int) bool {
		return catalog[i]["name"].(string) < catalog[j]["name"].(string)
	})

	payload, err := json.MarshalIndent(catalog, "", "  ")
	if err != nil {
		fail("marshal: %v", err)
	}
	payload = append(payload, '\n')

	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		fail("mkdir: %v", err)
	}
	if err := os.WriteFile(out, payload, 0o644); err != nil {
		fail("write %s: %v", out, err)
	}
	fmt.Printf("Wrote %d systems to %s\n", len(catalog), out)
}

type treeEntry struct {
	name string
	url  string
}

func fetchJSON(url string) (map[string]interface{}, error) {
	data, err := fetch(url)
	if err != nil {
		return nil, err
	}
	var out map[string]interface{}
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func fetch(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}
	return io.ReadAll(resp.Body)
}

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func fail(format string, args ...interface{}) {
	fmt.Fprintf(os.Stderr, "sync-systems: "+format+"\n", args...)
	os.Exit(1)
}
