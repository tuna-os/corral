package web

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func getConsolePage(t *testing.T, path string) (int, string) {
	t.Helper()
	mux, err := newMux()
	if err != nil {
		t.Fatalf("newMux: %v", err)
	}
	srv := httptest.NewServer(mux)
	defer srv.Close()
	resp, err := http.Get(srv.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func TestConsolePage_RendersHooks(t *testing.T) {
	status, body := getConsolePage(t, "/console/corral-vms/tuna-q.1_a")
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", status, body)
	}
	for _, want := range []string{
		`data-vm="tuna-q.1_a"`,
		`data-namespace="corral-vms"`,
		`data-scale="one"`,
		`data-connected="false"`,
		`id="vnc-screen"`,
		`src="../../console-page.js"`,
		`<title>tuna-q.1_a console · Corral</title>`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("page lacks %s:\n%s", want, body)
		}
	}
}

func TestConsolePage_ScaleFit(t *testing.T) {
	status, body := getConsolePage(t, "/console/local/desk?scale=fit")
	if status != http.StatusOK || !strings.Contains(body, `data-scale="fit"`) {
		t.Fatalf("status = %d, want 200 with data-scale=fit:\n%s", status, body)
	}
	if status, _ := getConsolePage(t, "/console/local/desk?scale=huge"); status != http.StatusBadRequest {
		t.Errorf("scale=huge: status = %d, want 400", status)
	}
}

// The context is free text from the query, so the template must escape it.
func TestConsolePage_EscapesContext(t *testing.T) {
	status, body := getConsolePage(t, `/console/corral-vms/desk?context=%22%3E%3Cscript%3Ealert(1)%3C%2Fscript%3E`)
	if status != http.StatusOK {
		t.Fatalf("status = %d, want 200", status)
	}
	if strings.Contains(body, "<script>alert") {
		t.Fatalf("context is not escaped:\n%s", body)
	}
	if !strings.Contains(body, `data-context="&#34;&gt;&lt;script&gt;alert(1)&lt;/script&gt;"`) {
		t.Errorf("escaped context missing:\n%s", body)
	}
}

// The template escapes names too, as a second line behind the pattern.
func TestConsolePage_TemplateEscapesNames(t *testing.T) {
	var out strings.Builder
	if err := consolePageTemplate.Execute(&out, consolePage{Namespace: `a"b`, Name: "<x>", Scale: "one"}); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out.String(), "<x>") || strings.Contains(out.String(), `a"b`) {
		t.Fatalf("names are not escaped:\n%s", out.String())
	}
}

func TestConsolePage_BadNamesAre404(t *testing.T) {
	for _, path := range []string{
		"/console/corral-vms/a%22b",
		"/console/corral-vms/%3Cscript%3E",
		"/console/corral-vms/-lead",
		"/console/corral-vms/.hidden",
		"/console/%20/desk",
		"/console/corral-vms/" + strings.Repeat("a", 254),
		"/console/corral-vms/desk/extra",
	} {
		if status, _ := getConsolePage(t, path); status != http.StatusNotFound {
			t.Errorf("GET %s = %d, want 404", path, status)
		}
	}
	// The page route must not shadow the console transports it loads.
	if status, _ := getConsolePage(t, "/console/vnc.js"); status != http.StatusOK {
		t.Errorf("GET /console/vnc.js = %d, want 200", status)
	}
}

func TestListenURL(t *testing.T) {
	addr := &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 41234}
	wild := &net.TCPAddr{IP: net.IPv6unspecified, Port: 41234}
	for _, tc := range []struct {
		bind     string
		listener net.Addr
		want     string
	}{
		{"127.0.0.1:0", addr, "http://127.0.0.1:41234"},
		{"localhost:0", addr, "http://localhost:41234"},
		{"[::1]:0", &net.TCPAddr{IP: net.IPv6loopback, Port: 41234}, "http://[::1]:41234"},
		{":0", wild, "http://localhost:41234"},
		{"0.0.0.0:8006", wild, "http://localhost:41234"},
		{"100.64.0.7:8006", &net.TCPAddr{IP: net.IPv4(100, 64, 0, 7), Port: 8006}, "http://100.64.0.7:8006"},
	} {
		if got := listenURL(tc.bind, tc.listener); got != tc.want {
			t.Errorf("listenURL(%q, %v) = %q, want %q", tc.bind, tc.listener, got, tc.want)
		}
	}
}
