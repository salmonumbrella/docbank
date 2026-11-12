package api

import (
	"encoding/json/v2"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"go.kenn.io/docbank/internal/blob"
	"go.kenn.io/docbank/internal/config"
	"go.kenn.io/docbank/internal/store"

	"github.com/stretchr/testify/require"
)

func TestWebSessionDeniesPersonRoutes(t *testing.T) {
	t.Parallel()
	paths := []struct {
		method string
		path   string
	}{
		{http.MethodPost, "/api/v1/people"},
		{http.MethodGet, "/api/v1/people/by-id/11111111-1111-4111-8111-111111111111"},
		{http.MethodPost, "/api/v1/people/by-id/11111111-1111-4111-8111-111111111111/retire"},
		{http.MethodPost, "/api/v1/people/by-id/11111111-1111-4111-8111-111111111111/split"},
		{http.MethodPost, "/api/v1/people/by-id//merge"},
		{http.MethodPost, "/api/v1/people/by-id/11111111-1111-4111-8111-111111111111/merge?extra=1"},
		{http.MethodGet, "/api/v1/people/by-id/11111111-1111-4111-8111-111111111111/merge"},
		{http.MethodPost, "/api/v1/people/by-id/11111111-1111-4111-8111-111111111111/merge/extra"},
	}
	for _, path := range paths {
		require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), path.method, path.path, nil)), path.method+" "+path.path)
	}
	version := "/api/v1/photos/versions/11111111-1111-4111-8111-111111111111"
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, version + "/face-decisions"}, {http.MethodPost, version + "/faces?extra=1"}, {http.MethodGet, version + "/faces/extra"},
		{http.MethodGet, "/api/v1/photos/versions/invalid/faces"}, {http.MethodGet, "/api/v1/people?query=Ada&query=Grace"},
		{http.MethodGet, "/api/v1/people?extra=1"},
		{http.MethodGet, "/api/v1/photos/people/11111111-1111-4111-8111-111111111111/face-previews?extra=1"},
		{http.MethodGet, "/api/v1/photos/people/11111111-1111-4111-8111-111111111111/face-previews?hidden=false&hidden=true"},
		{http.MethodGet, "/api/v1/photos/people/11111111-1111-4111-8111-111111111111/face-previews?hidden=%zz"},
	} {
		require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), route.method, route.path, nil)), route.path)
	}
}

func TestWebSessionAllowsPhotoAlbumAuthoredAndEventRoutes(t *testing.T) {
	t.Parallel()
	album := "/api/v1/photos/albums/11111111-1111-4111-8111-111111111111"
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/photos/versions/11111111-1111-4111-8111-111111111111/faces"},
		{http.MethodPost, "/api/v1/photos/versions/11111111-1111-4111-8111-111111111111/faces"},
		{http.MethodPost, "/api/v1/photos/versions/11111111-1111-4111-8111-111111111111/face-decisions"},
		{http.MethodGet, "/api/v1/people?query=Ada&limit=100&cursor=next"},
		{http.MethodGet, "/api/v1/people?photo_summary=true&recent_first=true"},
		{http.MethodPatch, "/api/v1/people/by-id/11111111-1111-4111-8111-111111111111"},
		{http.MethodPost, "/api/v1/people/by-id/11111111-1111-4111-8111-111111111111/merge"},
		{http.MethodGet, "/api/v1/photos/people/11111111-1111-4111-8111-111111111111/face-previews"},
		{http.MethodGet, "/api/v1/photos/people/11111111-1111-4111-8111-111111111111/face-previews?hidden=false"},
	} {
		require.True(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), route.method, route.path, nil)), route.path)
	}
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/nodes"},
		{http.MethodGet, photoEventsPath},
		{http.MethodGet, "/api/v1/photos/albums"},
		{http.MethodPost, "/api/v1/photos/albums"},
		{http.MethodPut, album},
		{http.MethodDelete, album},
		{http.MethodPut, album + "/cover"},
		{http.MethodPost, album + "/duplicate"},
		{http.MethodPost, album + "/members/add"},
		{http.MethodPost, album + "/members/remove"},
		{http.MethodPost, "/api/v1/photos/authored/prepare"},
		{http.MethodPost, "/api/v1/photos/authored/edit"},
		{http.MethodPost, "/api/v1/photos/authored/stack"},
		{http.MethodPost, "/api/v1/photos/authored/40000000-0000-4000-8000-000000000001/undo"},
	} {
		require.True(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), route.method, route.path, nil)), route.method+" "+route.path)
		require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), route.method, route.path+"?extra=1", nil)))
		if route.method == http.MethodPost && route.path != "/api/v1/photos/albums" {
			require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodGet, route.path, nil)))
		}
	}
	require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/nodes?parent_id=1", nil)))
	xmp := "/api/v1/photos/authored/40000000-0000-4000-8000-000000000001/xmp"
	require.True(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodPost, xmp, nil)))
	require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodGet, xmp, nil)))
	for _, path := range []string{album + "/extra", album + "/members/add/extra", "/api/v1/photos/authored/photos/undo", "/api/v1/photos/authored/invalid/xmp", xmp + "/extra"} {
		require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, nil)), path)
	}
	for _, path := range []string{"/api/v1/photos/albums/invalid", "/api/v1/photos/albums/11111111-1111-4111-8111-11111111111A", "/api/v1/photos/albums/"} {
		require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodPut, path, nil)), path)
	}
	checkout := "/api/v1/photos/checkouts/11111111-1111-4111-8111-111111111111"
	for _, route := range []struct{ method, path string }{
		{http.MethodPost, "/api/v1/photos/checkouts/estimate"},
		{http.MethodPost, "/api/v1/photos/checkouts"},
		{http.MethodGet, "/api/v1/photos/checkouts"},
		{http.MethodGet, checkout},
		{http.MethodPost, checkout + "/retire"},
		{http.MethodPost, checkout + "/scan"},
		{http.MethodPost, checkout + "/commit"},
		{http.MethodPost, checkout + "/rebind"},
	} {
		require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), route.method, route.path, nil)), route.method+" "+route.path)
	}
	require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodGet, album, nil)))
	require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodPatch, album, nil)))
	require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), http.MethodPost, "/api/v1/photos/assets", nil)))
}

func TestWebSessionPhotoGroupingAndCheckpointMethods(t *testing.T) {
	t.Parallel()
	for _, route := range []struct {
		path    string
		methods []string
	}{
		{photoCheckpointPath, []string{http.MethodGet, http.MethodPut}},
		{photoCheckpointPath + "/header", []string{http.MethodGet}},
		{"/api/v1/photos/settings", []string{http.MethodGet, http.MethodPut}},
		{"/api/v1/photos/stacks/group", []string{http.MethodPost}},
	} {
		for _, method := range []string{http.MethodGet, http.MethodPut, http.MethodPost, http.MethodPatch, http.MethodDelete, http.MethodHead} {
			allowed := slices.Contains(route.methods, method)
			require.Equal(t, allowed, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), method, route.path, nil)), method+" "+route.path)
			require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), method, route.path+"?extra=1", nil)))
			require.False(t, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), method, route.path+"/extra", nil)))
		}
	}
}

func TestWebSessionAllowsOnlyContractRegisterReads(t *testing.T) {
	t.Parallel()
	const contract = "/api/v1/contracts/11111111-1111-4111-8111-111111111111"
	for _, route := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodGet, "/api/v1/contracts?limit=100&offset=0", true},
		{http.MethodGet, "/api/v1/contracts?status=active&type=lease&expiring_before=2027-02-01&as_of=2027-01-01&limit=100&offset=200", true},
		{http.MethodGet, contract, true},
		{http.MethodGet, contract + "?as_of=2027-01-01", true},
		{http.MethodGet, "/api/v1/contracts?q=storage", true},
		{http.MethodGet, "/api/v1/contracts?party_id=11111111-1111-4111-8111-111111111111", false},
		{http.MethodGet, "/api/v1/contracts?status=active&status=draft", false},
		{http.MethodGet, "/api/v1/contracts?extra=1", false},
		{http.MethodGet, "/api/v1/contracts?status=%zz", false},
		{http.MethodGet, contract + "?as_of=2027-01-01&as_of=2027-01-02", false},
		{http.MethodGet, contract + "?limit=1", false},
		{http.MethodGet, "/api/v1/contracts/not-a-uuid", false},
		{http.MethodGet, "/api/v1/contracts/11111111-1111-4111-8111-11111111111A", false},
		{http.MethodGet, contract + "/documents", false},
	} {
		require.Equal(t, route.allowed, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), route.method, route.path, nil)), route.method+" "+route.path)
	}
}

func TestWebSessionContractWriteBoundaries(t *testing.T) {
	t.Parallel()
	const id = "11111111-1111-4111-8111-111111111111"
	const upper = "11111111-1111-4111-8111-11111111111A"
	const contract = "/api/v1/contracts/" + id
	const party = contract + "/parties/" + id
	const document = contract + "/documents/412"
	for _, tc := range []struct {
		method, path string
		allowed      bool
	}{
		{http.MethodPost, "/api/v1/contracts", true},
		{http.MethodPatch, contract, true},
		{http.MethodPut, party + "?role=own", true},
		{http.MethodDelete, party + "?role=own", true},
		{http.MethodPut, document + "?role=signed&content_version_id=" + id, true},
		{http.MethodDelete, document + "?role=signed&content_version_id=" + id, true},
		{http.MethodGet, "/api/v1/contracts?q=storage", true},
		{http.MethodGet, "/api/v1/parties?limit=100&offset=0", true},
		{http.MethodPost, "/api/v1/parties", true},
		{http.MethodGet, "/api/v1/nodes/412/contracts?as_of=2027-01-01&limit=100&offset=0", true},
		{http.MethodDelete, contract, false},
		{http.MethodPost, "/api/v1/contracts?x=1", false},
		{http.MethodPatch, contract + "?x=1", false},
		{http.MethodPatch, "/api/v1/contracts/" + upper, false},
		{http.MethodPut, contract + "/parties/" + upper + "?role=own", false},
		{http.MethodPut, party, false},
		{http.MethodPut, party + "?role=", false},
		{http.MethodPut, party + "?role=own&role=own", false},
		{http.MethodPut, party + "?role=own&extra=1", false},
		{http.MethodGet, party + "?role=own", false},
		{http.MethodPut, document + "?role=signed", false},
		{http.MethodPut, document + "?role=signed&content_version_id=", false},
		{http.MethodPut, document + "?role=signed&content_version_id=bad", false},
		{http.MethodPut, document + "?role=signed&content_version_id=" + upper, false},
		{http.MethodPut, document + "?role=&content_version_id=" + id, false},
		{http.MethodPut, document + "?role=signed&role=signed&content_version_id=" + id, false},
		{http.MethodPut, document + "?role=signed&content_version_id=" + id + "&content_version_id=" + id, false},
		{http.MethodPut, document + "?role=signed&content_version_id=" + id + "&extra=1", false},
		{http.MethodPut, contract + "/documents/0?role=signed&content_version_id=" + id, false},
		{http.MethodPatch, "/api/v1/parties/" + id, false},
		{http.MethodPost, "/api/v1/parties/" + id + "/retire", false},
		{http.MethodGet, "/api/v1/parties/" + id, false},
		{http.MethodPost, "/api/v1/parties?extra=1", false},
		{http.MethodGet, "/api/v1/parties?include_retired=true", false},
		{http.MethodGet, "/api/v1/parties?kind=person", false},
		{http.MethodGet, "/api/v1/parties?own=true", false},
		{http.MethodGet, "/api/v1/parties?limit=1&limit=2", false},
		{http.MethodGet, "/api/v1/contracts?party_id=" + id, false},
		{http.MethodGet, "/api/v1/contracts?q=a&q=b", false},
		{http.MethodGet, "/api/v1/nodes/0/contracts", false},
		{http.MethodGet, "/api/v1/nodes/1/contracts?extra=1", false},
		{http.MethodGet, "/api/v1/nodes/1/contracts?limit=1&limit=2", false},
		{http.MethodPost, "/api/v1/nodes/1/contracts", false},
	} {
		t.Run(tc.method+" "+tc.path, func(t *testing.T) {
			require.Equal(t, tc.allowed, webSessionRequestAllowed(httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, nil)))
		})
	}
}

func TestWebSessionContractWriteRejectsForeignOrigin(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	catalog, err := store.Open(t.Context(), filepath.Join(dir, "docbank.db"))
	require.NoError(t, err)
	t.Cleanup(func() { _ = catalog.Close() })
	blobsDir := filepath.Join(dir, "blobs")
	require.NoError(t, os.MkdirAll(filepath.Join(blobsDir, "tmp"), 0700))
	blobs, err := blob.New(store.NewPackCatalog(catalog), blobsDir)
	require.NoError(t, err)
	t.Cleanup(func() { _ = blobs.Close() })
	cfg := config.Default()
	cfg.Server.APIKey = "synthetic-key"
	cfg.Web.PublicOrigin = "https://archive.example.test"
	server := NewServer(Deps{Store: catalog, Blobs: blobs, VaultRoot: dir, Cfg: cfg, WebURL: "https://archive.example.test/", WebLoginEnabled: true})
	t.Cleanup(server.Close)
	login := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://archive.example.test/api/daemon/web-auth/login", strings.NewReader(`{"api_key":"synthetic-key"}`))
	login.Header.Set("Origin", "https://archive.example.test")
	login.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	server.Handler().ServeHTTP(response, login)
	require.Equal(t, http.StatusOK, response.Code, response.Body.String())
	var status webSignInStatus
	require.NoError(t, json.Unmarshal(response.Body.Bytes(), &status))
	require.NotNil(t, status.Session)
	cookies := response.Result().Cookies()
	require.Len(t, cookies, 1)
	request := httptest.NewRequestWithContext(t.Context(), http.MethodPost, "https://archive.example.test/api/v1/contracts", strings.NewReader(`{"title":"Synthetic"}`))
	request.Header.Set("Origin", "https://foreign.example.test")
	request.Header.Set(WebSessionHeader, status.Session.Token)
	request.AddCookie(cookies[0])
	denied := httptest.NewRecorder()
	server.Handler().ServeHTTP(denied, request)
	require.Equal(t, http.StatusForbidden, denied.Code)
	require.Contains(t, denied.Body.String(), `"code":"web_browser"`)
}

func TestWebPages(t *testing.T) {
	t.Parallel()
	mux := http.NewServeMux()
	registerWeb(mux, true, "http://localhost", "", "", config.MapTilesConfig{}, nil)
	for _, tc := range []struct {
		path    string
		allowed bool
	}{
		{"/photos/albums", true},
		{"/photos/albums/11111111-1111-4111-8111-111111111111", true},
		{"/photos/albums/invalid", false},
		{"/searches", true},
		{"/productions", true},
		{"/imports", true},
		{"/contracts", true},
		{"/trash", true},
		{"/searches/x", false},
	} {
		require.Equal(t, tc.allowed, authExempt(tc.path), tc.path)
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequestWithContext(t.Context(), http.MethodGet, tc.path, nil))
		status := http.StatusNotFound
		if tc.allowed {
			status = http.StatusOK
			require.Contains(t, response.Header().Get("Content-Type"), "text/html")
		}
		require.Equal(t, status, response.Code, tc.path)
	}
}
