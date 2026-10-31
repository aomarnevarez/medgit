package httpserver_test

import (
	"encoding/json"
	"net"
	"net/http"
	"testing"

	"github.com/aomarnevarez/medgit/internal/httpserver"
	"github.com/aomarnevarez/medgit/internal/repo"
)

func TestHTTPControlPlaneServesTheDemo(t *testing.T) {
	server := &httpserver.Server{DataRoot: t.TempDir()}
	if err := server.EnsureDemo(); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	httpServer := &http.Server{Handler: server}
	go func() { _ = httpServer.Serve(listener) }()
	t.Cleanup(func() { _ = httpServer.Close() })

	base := "http://" + listener.Addr().String()
	var health struct {
		OK     bool   `json:"ok"`
		Medgit string `json:"medgit"`
	}
	getJSON(t, base+"/health", &health)
	if !health.OK || health.Medgit != repo.Version {
		t.Fatalf("health = %#v", health)
	}
	var verified struct {
		OK bool `json:"ok"`
	}
	getJSON(t, base+"/records/f001/verify", &verified)
	if !verified.OK {
		t.Fatal("demo verify failed")
	}
	var listing struct {
		Records []string `json:"records"`
	}
	getJSON(t, base+"/records", &listing)
	if len(listing.Records) != 1 || listing.Records[0] != "f001" {
		t.Fatalf("records = %#v", listing.Records)
	}
}

func getJSON(t *testing.T, url string, dest any) {
	t.Helper()
	response, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("%s status %d", url, response.StatusCode)
	}
	if err := json.NewDecoder(response.Body).Decode(dest); err != nil {
		t.Fatal(err)
	}
}
