package source

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestFetchFreshReplacesCachedFile(t *testing.T) {
	body := "v1"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(body))
	}))
	defer srv.Close()
	cache := t.TempDir()
	url := srv.URL + "/index.json"

	p, err := FetchFresh(url, cache)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != "v1" {
		t.Fatalf("first fetch = %q", data)
	}
	body = "v2"
	p, err = FetchFresh(url, cache)
	if err != nil {
		t.Fatal(err)
	}
	if data, _ := os.ReadFile(p); string(data) != "v2" {
		t.Fatalf("second fetch = %q, want v2", data)
	}
	if got := p; got != filepath.Join(cache, "index.json") {
		t.Fatalf("cache path = %s", got)
	}
}

func TestFetchFreshNotFound(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	defer srv.Close()
	if _, err := FetchFresh(srv.URL+"/missing.sig", t.TempDir()); !IsNotFound(err) {
		t.Fatalf("error = %v, want NotFoundError", err)
	}
}
