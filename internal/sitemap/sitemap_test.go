package sitemap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"neoassets/internal/repository"
)

type fakeStore struct {
	games int
	packs []repository.SitemapPack
}

func (f fakeStore) CountGames(context.Context) (int, error) { return f.games, nil }

func (f fakeStore) SitemapGames(_ context.Context, limit, offset int) ([]repository.SitemapGame, error) {
	var out []repository.SitemapGame
	for i := offset; i < f.games && len(out) < limit; i++ {
		out = append(out, repository.SitemapGame{ID: "g", SystemID: "snes"})
	}
	return out, nil
}

func (f fakeStore) SitemapSystems(context.Context) ([]string, error) { return []string{"snes"}, nil }

func (f fakeStore) SitemapPacks(context.Context) ([]repository.SitemapPack, error) {
	return f.packs, nil
}

func get(t *testing.T, store Store, path string) (int, string) {
	t.Helper()
	r := chi.NewRouter()
	NewHandler(store, "https://neoassets.dev/").Routes(r)
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Host = "api.neoassets.dev"
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec.Code, rec.Body.String()
}

func TestIndexSplitsGames(t *testing.T) {
	_, body := get(t, fakeStore{games: GamesPerSitemap + 1}, "/sitemap.xml")
	for _, want := range []string{
		"https://api.neoassets.dev/sitemaps/pages.xml",
		"https://api.neoassets.dev/sitemaps/games-1.xml",
		"https://api.neoassets.dev/sitemaps/games-2.xml",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("index missing %s:\n%s", want, body)
		}
	}
	if strings.Contains(body, "games-3.xml") {
		t.Errorf("index lists an empty games sitemap:\n%s", body)
	}
}

func TestPagesListsPacksOnTheWebHost(t *testing.T) {
	store := fakeStore{packs: []repository.SitemapPack{{PackID: "neo pack", UpdatedAt: time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)}}}
	code, body := get(t, store, "/sitemaps/pages.xml")
	if code != http.StatusOK {
		t.Fatalf("status %d", code)
	}
	for _, want := range []string{
		"<loc>https://neoassets.dev/app/home</loc>",
		"<loc>https://neoassets.dev/app/metadata/snes</loc>",
		"<loc>https://neoassets.dev/app/sap/neo%20pack</loc>",
		"<lastmod>2026-01-02</lastmod>",
	} {
		if !strings.Contains(body, want) {
			t.Errorf("pages missing %s:\n%s", want, body)
		}
	}
}

func TestGamesPages(t *testing.T) {
	store := fakeStore{games: GamesPerSitemap + 1}
	if _, body := get(t, store, "/sitemaps/games-2.xml"); strings.Count(body, "<url>") != 1 {
		t.Errorf("second games sitemap should hold the last game:\n%s", body)
	}
	if code, _ := get(t, store, "/sitemaps/games-3.xml"); code != http.StatusNotFound {
		t.Errorf("past the end: status %d, want 404", code)
	}
}
