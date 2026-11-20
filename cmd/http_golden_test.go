package main

// Characterization tests for the HTTP API.
//
// They run the production wiring (buildApp + setupRouter) over a migrated test
// database and an in-memory bucket, call the API like a client would, and
// compare each response (status + JSON body) with a golden file under
// testdata/http. They pin today's behavior so refactors can't change response
// shapes, status codes or error messages by accident.
//
// Run with a test database:
//
//	NEOASSETS_TEST_DATABASE_URL=postgres://...neoassets_test... go test ./cmd -run HTTP
//
// After an intended API change, regenerate the files and review the diff:
//
//	go test ./cmd -run HTTP -update

import (
	"bytes"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"neoassets/internal/handlers"
	"neoassets/internal/testdb"
	"neoassets/pkg/auth"
	"neoassets/pkg/r2/r2test"
)

var updateGolden = flag.Bool("update", false, "rewrite the HTTP golden files")

const (
	testPassword     = "correct horse battery staple"
	testClientID     = "golden-client-id"
	testClientSecret = "golden-client-secret-value"
	gameMarioID      = "00000000-0000-4000-8000-0000000000a1"
	gameZeldaID      = "00000000-0000-4000-8000-0000000000a2"
)

// apiHarness is one wired service plus the clients' cookies.
type apiHarness struct {
	t      *testing.T
	db     *sql.DB
	bucket *r2test.Memory
	server http.Handler
}

func newAPIHarness(t *testing.T) *apiHarness {
	t.Helper()
	db := testdb.Open(t, "cmd_http")
	t.Setenv("JWT_SECRET", strings.Repeat("golden-test-secret-", 3))
	t.Setenv("R2_PUBLIC_BASE_URL", "https://cdn.test")
	cfg := loadConfig()
	cfg.R2PublicBase = "https://cdn.test"
	if err := handlers.ConfigureCDN(cfg.CDNIPRanges, cfg.CDNClientIPHeader); err != nil {
		t.Fatal(err)
	}
	configureEconomy(cfg)

	resetData(t, db)
	seedCatalog(t, db)

	bucket := r2test.NewMemory()
	a, err := buildApp(cfg, db, bucket, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &apiHarness{t: t, db: db, bucket: bucket, server: a.router}
}

// resetData empties the tables the tests write to, so every test starts from
// the same seed regardless of order.
func resetData(t *testing.T, db *sql.DB) {
	t.Helper()
	var tables []string
	rows, err := db.Query(`SELECT tablename FROM pg_tables WHERE schemaname = 'public'
		AND tablename NOT IN ('schema_migrations', 'genres', 'regions')`)
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			t.Fatal(err)
		}
		tables = append(tables, `"`+name+`"`)
	}
	rows.Close()
	if _, err := db.Exec(`TRUNCATE ` + strings.Join(tables, ", ") + ` RESTART IDENTITY CASCADE`); err != nil {
		t.Fatal(err)
	}
}

func seedCatalog(t *testing.T, db *sql.DB) {
	t.Helper()
	mustExec(t, db, `INSERT INTO metadata_systems (id, name, short_name, family, description)
		VALUES ('snes', 'Super Nintendo Entertainment System', 'SNES', 'console', 'A 16-bit home console.')`)
	mustExec(t, db, `INSERT INTO games (id, system_id, name, description, release_year, publisher, developer, genre, type)
		VALUES ($1, 'snes', 'Super Mario World', 'A platform game.', 1990, 'Nintendo', 'Nintendo EAD', 'Platform', 'base'),
		       ($2, 'snes', 'The Legend of Zelda: A Link to the Past', 'An action adventure.', 1991, 'Nintendo', 'Nintendo EAD', 'Adventure', 'base')`,
		gameMarioID, gameZeldaID)
	mustExec(t, db, `INSERT INTO roms (game_id, name, size, crc, md5, sha1)
		VALUES ($1, 'Super Mario World (USA).sfc', 524288, 'b19ed489', '', ''),
		       ($2, 'Legend of Zelda, The - A Link to the Past (USA).sfc', 1048576, '777aac2f', '', '')`,
		gameMarioID, gameZeldaID)
}

func mustExec(t *testing.T, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatalf("%v\n%s", err, query)
	}
}

// seedUser creates a verified account with testPassword and the given role.
func (h *apiHarness) seedUser(username, role string) {
	h.t.Helper()
	hash, err := auth.HashPassword(testPassword)
	if err != nil {
		h.t.Fatal(err)
	}
	mustExec(h.t, h.db, `INSERT INTO users (username, email, password_hash, email_verified, role)
		VALUES ($1, $2, $3, TRUE, $4)`, username, username+"@example.com", hash, role)
}

// seedDeveloperApp registers scrape credentials owned by username.
func (h *apiHarness) seedDeveloperApp(username string) {
	h.t.Helper()
	mustExec(h.t, h.db, `INSERT INTO developer_apps (user_id, name, client_id, client_secret_hash)
		SELECT id, 'Golden App', $2, $3 FROM users WHERE username = $1`,
		username, testClientID, auth.HashToken(testClientSecret))
}

// client sends requests with its own cookie jar (one per signed-in user).
type client struct {
	h       *apiHarness
	cookies map[string]*http.Cookie
	headers map[string]string
}

func (h *apiHarness) client() *client {
	return &client{h: h, cookies: map[string]*http.Cookie{}, headers: map[string]string{}}
}

type response struct {
	status int
	body   []byte
}

func (c *client) do(method, path string, body any) response {
	c.h.t.Helper()
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			c.h.t.Fatal(err)
		}
		reader = bytes.NewReader(data)
	}
	req := httptest.NewRequest(method, path, reader)
	req.RemoteAddr = "192.0.2.10:40000"
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for k, v := range c.headers {
		req.Header.Set(k, v)
	}
	for _, ck := range c.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	c.h.server.ServeHTTP(rec, req)
	for _, ck := range rec.Result().Cookies() {
		if ck.MaxAge < 0 || ck.Value == "" {
			delete(c.cookies, ck.Name)
		} else {
			c.cookies[ck.Name] = ck
		}
	}
	return response{status: rec.Code, body: rec.Body.Bytes()}
}

func (c *client) login(username string) {
	c.h.t.Helper()
	res := c.do(http.MethodPost, "/api/v1/login", map[string]string{
		"email": username + "@example.com", "password": testPassword,
	})
	if res.status != http.StatusOK {
		c.h.t.Fatalf("login %s: %d %s", username, res.status, res.body)
	}
}

// field decodes one top-level string field of a JSON response.
func (r response) field(t *testing.T, name string) string {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(r.body, &m); err != nil {
		t.Fatalf("decode %s: %v", r.body, err)
	}
	v, _ := m[name].(string)
	return v
}

var (
	uuidRe = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	timeRe = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?(Z|[+-]\d{2}:\d{2})`)
	dateRe = regexp.MustCompile(`"\d{4}-\d{2}-\d{2}"`)
	// Upload keys carry a random 8-hex segment (e.g. packs/x/review/ab12cd34/).
	keyRandRe = regexp.MustCompile(`/[0-9a-f]{8}/`)
)

// stableIDs are the seeded ids kept verbatim in golden files.
var stableIDs = map[string]bool{gameMarioID: true, gameZeldaID: true}

// normalize pretty-prints a JSON body and masks values that change per run
// (generated ids and timestamps).
func normalize(body []byte) string {
	var v any
	out := string(body)
	if json.Unmarshal(body, &v) == nil {
		pretty, _ := json.MarshalIndent(v, "", "  ")
		out = string(pretty)
	}
	out = uuidRe.ReplaceAllStringFunc(out, func(id string) string {
		if stableIDs[id] {
			return id
		}
		return "<uuid>"
	})
	out = timeRe.ReplaceAllString(out, "<time>")
	out = dateRe.ReplaceAllString(out, `"<date>"`)
	out = keyRandRe.ReplaceAllString(out, "/<rand>/")
	return out
}

// golden compares a response with testdata/http/<name>.golden.
func golden(t *testing.T, name string, res response) {
	t.Helper()
	got := fmt.Sprintf("%d\n%s\n", res.status, normalize(res.body))
	path := filepath.Join("testdata", "http", name+".golden")
	if *updateGolden {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to create it)", err)
	}
	if got != string(want) {
		t.Errorf("%s: response changed\n--- want\n%s\n--- got\n%s", name, want, got)
	}
}

func TestHTTPPublicEndpoints(t *testing.T) {
	h := newAPIHarness(t)
	c := h.client()
	for _, tc := range []struct{ name, path string }{
		{"health", "/health"},
		{"config", "/api/v1/config"},
		{"systems", "/api/v1/systems"},
		{"packs_empty", "/api/v1/packs"},
		{"pack_bad_id", "/api/v1/packs/not-a-pack"},
		{"pack_missing", "/api/v1/packs/missing-pack"},
		{"metadata_systems", "/api/v1/metadata/systems"},
		{"metadata_system_games", "/api/v1/metadata/systems/snes/games"},
		{"metadata_search", "/api/v1/metadata/games?q=mario"},
		{"metadata_search_none", "/api/v1/metadata/games?q=zzzzqqq"},
		{"metadata_game", "/api/v1/metadata/games/" + gameZeldaID},
		{"metadata_game_bad_id", "/api/v1/metadata/games/not-a-uuid"},
		{"metadata_game_missing", "/api/v1/metadata/games/00000000-0000-4000-8000-0000000000ff"},
		{"metadata_languages", "/api/v1/metadata/languages"},
		{"metadata_genres", "/api/v1/metadata/genres"},
		{"metadata_regions", "/api/v1/metadata/regions"},
		{"dashboard", "/api/v1/dashboard"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			golden(t, "public_"+tc.name, c.do(http.MethodGet, tc.path, nil))
		})
	}
}

func TestHTTPAuthBoundaries(t *testing.T) {
	h := newAPIHarness(t)
	anon := h.client()
	golden(t, "auth_me_anonymous", anon.do(http.MethodGet, "/api/v1/auth/me", nil))
	golden(t, "auth_admin_anonymous", anon.do(http.MethodGet, "/api/v1/admin/submissions", nil))
	golden(t, "auth_login_wrong_password", anon.do(http.MethodPost, "/api/v1/login",
		map[string]string{"email": "nobody@example.com", "password": "wrong"}))

	h.seedUser("alice", "user")
	alice := h.client()
	alice.login("alice")
	golden(t, "auth_me", alice.do(http.MethodGet, "/api/v1/auth/me", nil))
	golden(t, "auth_admin_as_user", alice.do(http.MethodGet, "/api/v1/admin/submissions", nil))
	golden(t, "auth_rewards", alice.do(http.MethodGet, "/api/v1/rewards", nil))
	golden(t, "auth_logout", alice.do(http.MethodPost, "/api/v1/logout", nil))
	golden(t, "auth_me_after_logout", alice.do(http.MethodGet, "/api/v1/auth/me", nil))
}

func TestHTTPScrape(t *testing.T) {
	h := newAPIHarness(t)
	h.seedUser("dev", "user")
	h.seedDeveloperApp("dev")

	anon := h.client()
	golden(t, "scrape_no_credentials", anon.do(http.MethodGet, "/api/v1/scrape/systems", nil))

	bad := h.client()
	bad.headers["X-Client-Id"] = testClientID
	bad.headers["X-Client-Secret"] = "wrong-secret"
	golden(t, "scrape_bad_secret", bad.do(http.MethodGet, "/api/v1/scrape/systems", nil))

	app := h.client()
	app.headers["X-Client-Id"] = testClientID
	app.headers["X-Client-Secret"] = testClientSecret
	for _, tc := range []struct{ name, path string }{
		{"systems", "/api/v1/scrape/systems"},
		{"families", "/api/v1/scrape/families"},
		{"groups", "/api/v1/scrape/groups"},
		{"game_by_crc", "/api/v1/scrape/games?system_id=snes&crc=b19ed489"},
		{"game_by_name", "/api/v1/scrape/games?system_id=snes&name=Legend%20of%20Zelda%2C%20The%20-%20A%20Link%20to%20the%20Past%20(USA).sfc"},
		{"game_with_roms", "/api/v1/scrape/games?system_id=snes&crc=b19ed489&roms=true"},
		{"game_no_match", "/api/v1/scrape/games?system_id=snes&name=zzzzqqq"},
		{"game_no_system", "/api/v1/scrape/games?crc=b19ed489"},
		{"game_family", "/api/v1/scrape/games?family=console&crc=777aac2f"},
		{"popular", "/api/v1/scrape/popular"},
		{"account", "/api/v1/scrape/account"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			golden(t, "scrape_"+tc.name, app.do(http.MethodGet, tc.path, nil))
		})
	}
}

// TestHTTPPackLifecycle walks a pack through the submission state machine:
// upload, create, submit, review (reject or approve), trash and the public
// listing.
func TestHTTPPackLifecycle(t *testing.T) {
	h := newAPIHarness(t)
	h.seedUser("artist", "user")
	h.seedUser("reviewer", "admin")
	artist := h.client()
	artist.login("artist")
	admin := h.client()
	admin.login("reviewer")

	// newPack uploads one SNES background and creates the pack.
	newPack := func(name string) string {
		t.Helper()
		up := artist.do(http.MethodPost, "/api/v1/submissions/upload-url", map[string]any{
			"name": name, "file_name": "snes.png", "system_id": "snes", "kind": "background",
			"mime_type": "image/png", "size": len(testPNG(t)),
		})
		if up.status != http.StatusOK {
			t.Fatalf("upload-url: %d %s", up.status, up.body)
		}
		key := up.field(t, "object_key")
		h.bucket.Put(key, "image/png", testPNG(t))
		res := artist.do(http.MethodPost, "/api/v1/submissions", map[string]any{
			"name": name, "author": "Artist", "description": "A test pack.",
			"donation_url": "https://ko-fi.com/artist",
			"files": []map[string]any{{
				"object_key": key, "file_name": "snes.png", "system_id": "snes", "kind": "background",
				"mime_type": "image/png", "size": len(testPNG(t)),
			}},
		})
		golden(t, "pack_create_"+slug(name), res)
		return res.field(t, "id")
	}

	rejected := newPack("Rejected Pack")
	golden(t, "pack_my_list", artist.do(http.MethodGet, "/api/v1/auth/submissions", nil))
	golden(t, "pack_my_detail", artist.do(http.MethodGet, "/api/v1/auth/submissions/"+rejected, nil))
	golden(t, "pack_update_draft", artist.do(http.MethodPut, "/api/v1/submissions/"+rejected, map[string]any{
		"name": "Rejected Pack", "author": "Artist", "description": "Edited.",
		"donation_url": "https://ko-fi.com/artist",
	}))
	golden(t, "pack_submit", artist.do(http.MethodPost, "/api/v1/submissions/"+rejected+"/submit", nil))
	golden(t, "pack_update_pending", artist.do(http.MethodPut, "/api/v1/submissions/"+rejected, map[string]any{
		"name": "Rejected Pack", "author": "Artist", "description": "Too late.",
	}))
	golden(t, "pack_trash_pending", artist.do(http.MethodPost, "/api/v1/submissions/"+rejected+"/trash", nil))
	golden(t, "pack_admin_pending", admin.do(http.MethodGet, "/api/v1/admin/submissions?status=pending", nil))
	golden(t, "pack_admin_detail", admin.do(http.MethodGet, "/api/v1/admin/submissions/"+rejected, nil))
	golden(t, "pack_reject", admin.do(http.MethodPost, "/api/v1/admin/submissions/"+rejected+"/reject",
		map[string]string{"comment": "Not ready."}))
	golden(t, "pack_reject_again", admin.do(http.MethodPost, "/api/v1/admin/submissions/"+rejected+"/reject",
		map[string]string{"comment": "Twice."}))
	golden(t, "pack_trash_rejected", artist.do(http.MethodPost, "/api/v1/submissions/"+rejected+"/trash", nil))
	golden(t, "pack_my_list_after_trash", artist.do(http.MethodGet, "/api/v1/auth/submissions", nil))

	if !ffmpegHasWebP() {
		t.Log("ffmpeg with libwebp not installed: skipping the approve path (approval normalizes images to WebP)")
		return
	}
	approved := newPack("Approved Pack")
	artist.do(http.MethodPost, "/api/v1/submissions/"+approved+"/submit", nil)
	golden(t, "pack_approve", admin.do(http.MethodPost, "/api/v1/admin/submissions/"+approved+"/approve",
		map[string]string{"version": "1.0", "comment": "Nice."}))
	golden(t, "pack_approve_again", admin.do(http.MethodPost, "/api/v1/admin/submissions/"+approved+"/approve",
		map[string]string{"version": "1.1"}))
	golden(t, "pack_public_list", h.client().do(http.MethodGet, "/api/v1/packs", nil))
	golden(t, "pack_public_detail", h.client().do(http.MethodGet, "/api/v1/packs/approved-pack", nil))
	golden(t, "pack_public_download", h.client().do(http.MethodGet, "/api/v1/packs/approved-pack/download", nil))
	golden(t, "pack_trash_approved", artist.do(http.MethodPost, "/api/v1/submissions/"+approved+"/trash", nil))
	golden(t, "pack_artist_rewards", artist.do(http.MethodGet, "/api/v1/rewards", nil))
}

// ffmpegHasWebP reports whether ffmpeg can encode WebP, which approval needs.
func ffmpegHasWebP() bool {
	out, err := exec.Command("ffmpeg", "-hide_banner", "-encoders").Output()
	return err == nil && strings.Contains(string(out), "libwebp")
}

func slug(s string) string {
	return strings.ReplaceAll(strings.ToLower(s), " ", "_")
}

// testPNGBase64 is a valid 64x64 gradient PNG. It is stored as bytes (not
// encoded at test time) because image/png output differs between Go
// versions, and the size is part of the golden responses.
const testPNGBase64 = "iVBORw0KGgoAAAANSUhEUgAAAEAAAABACAIAAAAlC+aJAAAAU0lEQVR4nOzPMQ3AQBTFsAwPeKEXxQ1fchQCXn2ry6/TAQAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAALwH/AMAs0UDeeqndHMAAAAASUVORK5CYII="

// testPNG returns the test image.
func testPNG(t *testing.T) []byte {
	t.Helper()
	data, err := base64.StdEncoding.DecodeString(testPNGBase64)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// Concurrent reviews of one submission apply once: exactly one reject
// succeeds and the others are refused (in progress, or no longer pending).
func TestHTTPConcurrentRejectAppliesOnce(t *testing.T) {
	h := newAPIHarness(t)
	h.seedUser("artist", "user")
	h.seedUser("reviewer", "admin")
	artist := h.client()
	artist.login("artist")
	admin := h.client()
	admin.login("reviewer")

	up := artist.do(http.MethodPost, "/api/v1/submissions/upload-url", map[string]any{
		"name": "Race Pack", "file_name": "snes.png", "system_id": "snes", "kind": "background",
		"mime_type": "image/png", "size": len(testPNG(t)),
	})
	key := up.field(t, "object_key")
	h.bucket.Put(key, "image/png", testPNG(t))
	created := artist.do(http.MethodPost, "/api/v1/submissions", map[string]any{
		"name": "Race Pack", "author": "Artist",
		"files": []map[string]any{{
			"object_key": key, "file_name": "snes.png", "system_id": "snes", "kind": "background",
			"mime_type": "image/png", "size": len(testPNG(t)),
		}},
	})
	id := created.field(t, "id")

	const n = 8
	results := make(chan response, n)
	var wg sync.WaitGroup
	for i := 0; i < n; i++ {
		wg.Add(1)
		// Each request gets its own client (cookie jar) carrying the admin session.
		reviewer := h.client()
		for k, v := range admin.cookies {
			reviewer.cookies[k] = v
		}
		go func() {
			defer wg.Done()
			results <- reviewer.do(http.MethodPost, "/api/v1/admin/submissions/"+id+"/reject",
				map[string]string{"comment": "race"})
		}()
	}
	wg.Wait()
	close(results)
	ok := 0
	for res := range results {
		switch {
		case res.status == http.StatusOK:
			ok++
		case res.status == http.StatusBadRequest &&
			(strings.Contains(string(res.body), "already being reviewed") || strings.Contains(string(res.body), "not pending")):
		default:
			t.Errorf("unexpected response %d %s", res.status, res.body)
		}
	}
	if ok != 1 {
		t.Fatalf("%d rejects succeeded, want exactly 1", ok)
	}
	var logs int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM submission_logs WHERE submission_id = $1 AND action = 'rejected'`, id).Scan(&logs); err != nil {
		t.Fatal(err)
	}
	if logs != 1 {
		t.Fatalf("%d rejection log rows, want 1", logs)
	}
}

// A review body may be omitted, but a malformed one is refused instead of
// being silently ignored.
func TestHTTPReviewBodyValidation(t *testing.T) {
	h := newAPIHarness(t)
	h.seedUser("reviewer", "admin")
	admin := h.client()
	admin.login("reviewer")
	path := "/api/v1/admin/submissions/00000000-0000-4000-8000-0000000000ff/reject"

	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(`{"comment":`))
	req.RemoteAddr = "192.0.2.10:40000"
	for _, ck := range admin.cookies {
		req.AddCookie(ck)
	}
	rec := httptest.NewRecorder()
	h.server.ServeHTTP(rec, req)
	golden(t, "review_malformed_body", response{status: rec.Code, body: rec.Body.Bytes()})

	// No body at all is still accepted (the comment is optional).
	golden(t, "review_empty_body", admin.do(http.MethodPost, path, nil))
}

// A pack whose file was never uploaded is refused without leaving a
// submission row behind.
func TestHTTPCreatePackWithMissingUploadLeavesNothing(t *testing.T) {
	h := newAPIHarness(t)
	h.seedUser("artist", "user")
	artist := h.client()
	artist.login("artist")
	up := artist.do(http.MethodPost, "/api/v1/submissions/upload-url", map[string]any{
		"name": "Ghost Pack", "file_name": "snes.png", "system_id": "snes", "kind": "background",
		"mime_type": "image/png", "size": 100,
	})
	key := up.field(t, "object_key")
	// The browser upload never happens: nothing is put in the bucket.
	golden(t, "pack_create_missing_upload", artist.do(http.MethodPost, "/api/v1/submissions", map[string]any{
		"name": "Ghost Pack", "author": "Artist",
		"files": []map[string]any{{
			"object_key": key, "file_name": "snes.png", "system_id": "snes", "kind": "background",
			"mime_type": "image/png", "size": 100,
		}},
	}))
	var n int
	if err := h.db.QueryRow(`SELECT COUNT(*) FROM submissions`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	if n != 0 {
		t.Fatalf("%d submission rows after a refused create, want 0", n)
	}
}

// TestHTTPMetadataLifecycle walks text-only metadata contributions through
// create, submit and review: an edit of an existing game (applied in one
// transaction) and a brand-new game.
func TestHTTPMetadataLifecycle(t *testing.T) {
	h := newAPIHarness(t)
	h.seedUser("writer", "user")
	h.seedUser("reviewer", "admin")
	writer := h.client()
	writer.login("writer")
	admin := h.client()
	admin.login("reviewer")

	edit := writer.do(http.MethodPost, "/api/v1/metadata/submissions", map[string]any{
		"game_id": gameMarioID, "kind": "edit",
		"payload": map[string]any{"publisher": "Nintendo of America", "description": "An updated description."},
	})
	golden(t, "metadata_create_edit", edit)
	editID := edit.field(t, "id")
	golden(t, "metadata_submit_edit", writer.do(http.MethodPost, "/api/v1/metadata/submissions/"+editID+"/submit", nil))
	golden(t, "metadata_my_list", writer.do(http.MethodGet, "/api/v1/auth/metadata/submissions", nil))
	golden(t, "metadata_admin_list", admin.do(http.MethodGet, "/api/v1/admin/metadata/submissions?status=pending", nil))
	golden(t, "metadata_approve_edit", admin.do(http.MethodPost, "/api/v1/admin/metadata/submissions/"+editID+"/approve",
		map[string]string{"comment": "Thanks."}))
	golden(t, "metadata_approve_edit_again", admin.do(http.MethodPost, "/api/v1/admin/metadata/submissions/"+editID+"/approve", nil))
	golden(t, "metadata_game_after_edit", h.client().do(http.MethodGet, "/api/v1/metadata/games/"+gameMarioID, nil))

	newGame := writer.do(http.MethodPost, "/api/v1/metadata/submissions", map[string]any{
		"system_id": "snes", "kind": "new_game",
		"payload": map[string]any{"name": "Golden Test Quest", "type": "base", "publisher": "Test Co"},
	})
	golden(t, "metadata_create_new_game", newGame)
	newID := newGame.field(t, "id")
	writer.do(http.MethodPost, "/api/v1/metadata/submissions/"+newID+"/submit", nil)
	golden(t, "metadata_approve_new_game", admin.do(http.MethodPost, "/api/v1/admin/metadata/submissions/"+newID+"/approve", nil))
	golden(t, "metadata_search_new_game", h.client().do(http.MethodGet, "/api/v1/metadata/games?q=golden+test+quest", nil))

	rejected := writer.do(http.MethodPost, "/api/v1/metadata/submissions", map[string]any{
		"game_id": gameZeldaID, "kind": "edit", "payload": map[string]any{"publisher": "Nobody"},
	})
	rejectedID := rejected.field(t, "id")
	writer.do(http.MethodPost, "/api/v1/metadata/submissions/"+rejectedID+"/submit", nil)
	golden(t, "metadata_reject", admin.do(http.MethodPost, "/api/v1/admin/metadata/submissions/"+rejectedID+"/reject",
		map[string]string{"comment": "No."}))
	golden(t, "metadata_writer_rewards", writer.do(http.MethodGet, "/api/v1/rewards", nil))
}
