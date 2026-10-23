package api

import (
	"strconv"
	"strings"
	"testing"
)

func TestSavedSearchAccess(t *testing.T) {
	s, h, admin := testServer(t)
	analyst := addUser(t, s.Meta, "ana", "analyst")
	viewer := addUser(t, s.Meta, "vic", "viewer")

	// Everyone can save a private search; sharing needs the analyst role.
	w, out := jsonCall(t, h, viewer, "POST", "/api/saved-searches", `{"name":"My Wi-Fi","query":"category:wifi","preset":"24h","pinned":true}`)
	if w.Code != 200 || out["pinned"] != true || out["mine"] != true {
		t.Fatalf("viewer save: %d %s", w.Code, w.Body)
	}
	private := strconv.Itoa(int(out["id"].(float64)))
	if w, _ := jsonCall(t, h, viewer, "POST", "/api/saved-searches", `{"name":"x","query":"","shared":true}`); w.Code != 403 {
		t.Errorf("viewer shared: %d", w.Code)
	}
	if w, _ := jsonCall(t, h, viewer, "POST", "/api/saved-searches", `{"name":"x","query":"nosuchfield:1"}`); w.Code != 400 {
		t.Errorf("bad filter: %d", w.Code)
	}
	if w, _ := jsonCall(t, h, viewer, "POST", "/api/saved-searches", `{"name":"x","preset":"custom","from":"yesterday"}`); w.Code != 400 {
		t.Errorf("bad range: %d", w.Code)
	}
	w, out = jsonCall(t, h, analyst, "POST", "/api/saved-searches", `{"name":"Team IPS","query":"category:ips","shared":true}`)
	if w.Code != 200 {
		t.Fatalf("analyst share: %d %s", w.Code, w.Body)
	}
	shared := strconv.Itoa(int(out["id"].(float64)))

	// Private searches stay private, even for administrators.
	if w := call(h, analyst, "GET", "/api/saved-searches", "", nil); w.Code != 200 || containsName(w.Body.String(), "My Wi-Fi") {
		t.Errorf("analyst sees private search: %s", w.Body)
	}
	if w, _ := jsonCall(t, h, admin, "DELETE", "/api/saved-searches/"+private, ""); w.Code != 404 {
		t.Errorf("admin deletes private search: %d", w.Code)
	}

	// Others can pin a shared search but not change it; admins can.
	if w, out := jsonCall(t, h, viewer, "PUT", "/api/saved-searches/"+shared+"/pin", `{"pinned":true}`); w.Code != 200 || out["pinned"] != true {
		t.Errorf("viewer pin: %d %s", w.Code, w.Body)
	}
	if w, _ := jsonCall(t, h, viewer, "PUT", "/api/saved-searches/"+private+"/pin", `{"pinned":false}`); w.Code != 200 {
		t.Errorf("unpin own: %d", w.Code)
	}
	if w, _ := jsonCall(t, h, viewer, "PUT", "/api/saved-searches/"+shared, `{"name":"Hijack","query":""}`); w.Code != 403 {
		t.Errorf("viewer edits shared: %d", w.Code)
	}
	if w, _ := jsonCall(t, h, analyst, "PUT", "/api/saved-searches/"+private+"/pin", `{"pinned":true}`); w.Code != 404 {
		t.Errorf("pin someone's private search: %d", w.Code)
	}
	if w, out := jsonCall(t, h, admin, "PUT", "/api/saved-searches/"+shared, `{"name":"IPS (team)","query":"category:ips","shared":true}`); w.Code != 200 || out["owner"] != "ana" || out["can_edit"] != true {
		t.Errorf("admin edit: %d %s", w.Code, w.Body)
	}
	if w, _ := jsonCall(t, h, analyst, "DELETE", "/api/saved-searches/"+shared, ""); w.Code != 200 {
		t.Errorf("owner delete: %d", w.Code)
	}
}

func containsName(body, name string) bool { return strings.Contains(body, `"name":"`+name+`"`) }
