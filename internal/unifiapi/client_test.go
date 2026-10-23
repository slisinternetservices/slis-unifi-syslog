package unifiapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

func fakeController(t *testing.T, key string, clients int) *httptest.Server {
	t.Helper()
	page := func(w http.ResponseWriter, r *http.Request, all []any) {
		off, _ := strconv.Atoi(r.URL.Query().Get("offset"))
		lim, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		end := min(off+lim, len(all))
		json.NewEncoder(w).Encode(map[string]any{"offset": off, "limit": lim, "count": end - off, "totalCount": len(all), "data": all[off:end]})
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/proxy/network/integration/v1/", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-API-KEY") != key {
			w.WriteHeader(401)
			return
		}
		switch r.URL.Path {
		case "/proxy/network/integration/v1/sites":
			page(w, r, []any{map[string]string{"id": "s1", "internalReference": "default", "name": "Default"}})
		case "/proxy/network/integration/v1/sites/s1/clients":
			var all []any
			for i := 0; i < clients; i++ {
				all = append(all, map[string]string{"id": strconv.Itoa(i), "name": "Client " + strconv.Itoa(i), "type": "WIRELESS",
					"macAddress": "A4:83:E7:00:00:" + strconv.FormatInt(int64(i%256), 16), "ipAddress": "192.168.1." + strconv.Itoa(i%250)})
			}
			page(w, r, all)
		case "/proxy/network/integration/v1/sites/s1/devices":
			page(w, r, []any{map[string]any{"id": "d1", "name": "AP Hall", "model": "U6-Pro", "macAddress": "f4:e2:c6:00:00:01", "state": "ONLINE"}})
		case "/proxy/network/integration/v1/sites/s1/devices/d1":
			json.NewEncoder(w).Encode(map[string]any{"id": "d1", "firmwareVersion": "6.6.55", "firmwareUpdatable": true})
		default:
			http.NotFound(w, r)
		}
	})
	return httptest.NewTLSServer(mux)
}

func TestFetchPagesAndDetails(t *testing.T) {
	srv := fakeController(t, "secret", 450)
	defer srv.Close()
	snap, err := New(srv.URL, "secret", false).Fetch(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snap.Sites) != 1 || len(snap.Clients["s1"]) != 450 {
		t.Fatalf("sites %d clients %d", len(snap.Sites), len(snap.Clients["s1"]))
	}
	d := snap.Devices["s1"][0]
	if d.Name != "AP Hall" || d.FirmwareVersion != "6.6.55" || !d.FirmwareUpdatable {
		t.Fatalf("device %+v", d)
	}
}

func TestFetchErrors(t *testing.T) {
	srv := fakeController(t, "secret", 1)
	defer srv.Close()
	if _, err := New(srv.URL, "wrong", false).Fetch(context.Background()); err != ErrAuth {
		t.Fatalf("bad key: %v", err)
	}
	// Self-signed certificate is refused when verification is on.
	if _, err := New(srv.URL, "secret", true).Fetch(context.Background()); err == nil {
		t.Fatal("self-signed certificate accepted")
	}
	plain := httptest.NewServer(http.NotFoundHandler())
	defer plain.Close()
	if _, err := New(plain.URL, "x", false).Fetch(context.Background()); err == nil {
		t.Fatal("missing API accepted")
	}
}
