package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestDashboardServed(t *testing.T) {
	_, ts := newTestServer(t, true)
	res, body := get(t, ts.URL+"/dashboard/", nil)
	if res.StatusCode != 200 || !strings.Contains(string(body), `id="login-form"`) {
		t.Fatalf("dashboard index: %d", res.StatusCode)
	}
	for _, p := range []string{"/dashboard/app.js", "/dashboard/style.css"} {
		res, _ := get(t, ts.URL+p, nil)
		if res.StatusCode != 200 || res.Header.Get("Cache-Control") != pageCacheControl {
			t.Errorf("%s: %d %q", p, res.StatusCode, res.Header.Get("Cache-Control"))
		}
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res2, err := client.Get(ts.URL + "/dashboard")
	if err != nil {
		t.Fatal(err)
	}
	res2.Body.Close()
	if res2.StatusCode != http.StatusMovedPermanently || res2.Header.Get("Location") != "/dashboard/" {
		t.Errorf("/dashboard redirect: %d %q", res2.StatusCode, res2.Header.Get("Location"))
	}
}
