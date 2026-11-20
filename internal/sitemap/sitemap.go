// Package sitemap serves the XML sitemaps that list the public web pages
// (static pages, systems, games and approved art packs) for search engines.
//
// The sitemaps live on the API host and list URLs on the web host; the web's
// robots.txt references /sitemap.xml here, which is how search engines accept
// a sitemap hosted on another host.
package sitemap

import (
	"context"
	"encoding/xml"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/rs/zerolog/log"

	"neoassets/internal/repository"
)

// GamesPerSitemap keeps each games sitemap under the 50,000 URL limit.
const GamesPerSitemap = 40000

// Store is what the sitemaps read.
type Store interface {
	CountGames(ctx context.Context) (int, error)
	SitemapGames(ctx context.Context, limit, offset int) ([]repository.SitemapGame, error)
	SitemapSystems(ctx context.Context) ([]string, error)
	SitemapPacks(ctx context.Context) ([]repository.SitemapPack, error)
}

// staticPages are the public, indexable web pages.
var staticPages = []string{
	"/app/home",
	"/app/metadata",
	"/app/sap",
	"/app/dashboard",
	"/app/guide",
	"/app/docs",
}

// Handler serves /sitemap.xml (an index) and the sitemaps under /sitemaps/.
type Handler struct {
	store   Store
	siteURL string
}

// NewHandler builds the handler; siteURL is the public web origin
// (e.g. https://neoassets.dev).
func NewHandler(store Store, siteURL string) *Handler {
	return &Handler{store: store, siteURL: strings.TrimRight(siteURL, "/")}
}

// Routes mounts the sitemap endpoints on r.
func (h *Handler) Routes(r chi.Router) {
	r.Get("/sitemap.xml", h.Index)
	r.Get("/sitemaps/pages.xml", h.Pages)
	r.Get("/sitemaps/games-{n}.xml", h.Games)
}

type urlEntry struct {
	Loc     string `xml:"loc"`
	LastMod string `xml:"lastmod,omitempty"`
}

type urlSet struct {
	XMLName xml.Name   `xml:"urlset"`
	NS      string     `xml:"xmlns,attr"`
	URLs    []urlEntry `xml:"url"`
}

type sitemapEntry struct {
	Loc string `xml:"loc"`
}

type sitemapIndex struct {
	XMLName  xml.Name       `xml:"sitemapindex"`
	NS       string         `xml:"xmlns,attr"`
	Sitemaps []sitemapEntry `xml:"sitemap"`
}

const ns = "http://www.sitemaps.org/schemas/sitemap/0.9"

// Index lists the pages sitemap and one sitemap per GamesPerSitemap games.
func (h *Handler) Index(w http.ResponseWriter, r *http.Request) {
	total, err := h.store.CountGames(r.Context())
	if err != nil {
		h.fail(w, err)
		return
	}
	base := selfBase(r)
	idx := sitemapIndex{NS: ns, Sitemaps: []sitemapEntry{{Loc: base + "/sitemaps/pages.xml"}}}
	for n := 1; (n-1)*GamesPerSitemap < total; n++ {
		idx.Sitemaps = append(idx.Sitemaps, sitemapEntry{Loc: fmt.Sprintf("%s/sitemaps/games-%d.xml", base, n)})
	}
	write(w, idx)
}

// Pages lists the static pages, every system with games and every approved
// art pack.
func (h *Handler) Pages(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	systems, err := h.store.SitemapSystems(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	packs, err := h.store.SitemapPacks(ctx)
	if err != nil {
		h.fail(w, err)
		return
	}
	set := urlSet{NS: ns}
	for _, p := range staticPages {
		set.URLs = append(set.URLs, urlEntry{Loc: h.siteURL + p})
	}
	for _, id := range systems {
		set.URLs = append(set.URLs, urlEntry{Loc: h.siteURL + "/app/metadata/" + url.PathEscape(id)})
	}
	for _, p := range packs {
		set.URLs = append(set.URLs, urlEntry{Loc: h.siteURL + "/app/sap/" + url.PathEscape(p.PackID), LastMod: day(p.UpdatedAt)})
	}
	write(w, set)
}

// Games lists one page of game pages.
func (h *Handler) Games(w http.ResponseWriter, r *http.Request) {
	n, err := strconv.Atoi(chi.URLParam(r, "n"))
	if err != nil || n < 1 {
		http.NotFound(w, r)
		return
	}
	games, err := h.store.SitemapGames(r.Context(), GamesPerSitemap, (n-1)*GamesPerSitemap)
	if err != nil {
		h.fail(w, err)
		return
	}
	if len(games) == 0 {
		http.NotFound(w, r)
		return
	}
	set := urlSet{NS: ns, URLs: make([]urlEntry, 0, len(games))}
	for _, g := range games {
		set.URLs = append(set.URLs, urlEntry{
			Loc:     fmt.Sprintf("%s/app/metadata/%s/game/%s", h.siteURL, url.PathEscape(g.SystemID), g.ID),
			LastMod: day(g.UpdatedAt),
		})
	}
	write(w, set)
}

func (h *Handler) fail(w http.ResponseWriter, err error) {
	log.Error().Err(err).Msg("sitemap")
	http.Error(w, "internal server error", http.StatusInternalServerError)
}

func day(t time.Time) string {
	if t.IsZero() {
		return ""
	}
	return t.UTC().Format("2006-01-02")
}

// selfBase is this API's public origin, as the client reached it.
func selfBase(r *http.Request) string {
	scheme := "https"
	if p := r.Header.Get("X-Forwarded-Proto"); p != "" {
		scheme = p
	} else if r.TLS == nil && strings.HasPrefix(r.Host, "localhost") {
		scheme = "http"
	}
	return scheme + "://" + r.Host
}

func write(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	// Search engines fetch these rarely; an hour of caching is plenty.
	w.Header().Set("Cache-Control", "public, max-age=3600")
	_, _ = w.Write([]byte(xml.Header))
	enc := xml.NewEncoder(w)
	enc.Indent("", "  ")
	_ = enc.Encode(v)
}
