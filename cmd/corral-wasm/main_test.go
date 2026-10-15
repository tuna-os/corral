//go:build js && wasm

package main

// Runs under node: GOOS=js GOARCH=wasm go test \
//   -exec="$(go env GOROOT)/lib/wasm/go_js_wasm_exec" ./cmd/corral-wasm/
// (the web-demo workflow does this). serve is the whole contract between
// web-demo/sw.js and the Go dashboard, so these tests drive it the way the
// service worker does and read the answer back from the JS side.

import (
	"io"
	"net/http"
	"strings"
	"syscall/js"
	"testing"
	"time"
)

type jsResult struct {
	status  int
	headers map[string][]string
	body    string
	err     string
}

// call invokes serve like sw.js does and waits for the Promise to settle.
func call(t *testing.T, h http.Handler, method, url string, headers [][2]string, body []byte) jsResult {
	t.Helper()
	hdrs := js.Global().Get("Array").New()
	for _, h := range headers {
		hdrs.Call("push", js.ValueOf([]any{h[0], h[1]}))
	}
	jsBody := js.Null()
	if body != nil {
		jsBody = js.Global().Get("Uint8Array").New(len(body))
		js.CopyBytesToJS(jsBody, body)
	}
	done := make(chan jsResult, 1)
	onOK := js.FuncOf(func(_ js.Value, a []js.Value) any {
		v := a[0]
		r := jsResult{status: v.Get("status").Int(), headers: map[string][]string{}}
		hs := v.Get("headers")
		for i := 0; i < hs.Length(); i++ {
			name := http.CanonicalHeaderKey(hs.Index(i).Index(0).String())
			r.headers[name] = append(r.headers[name], hs.Index(i).Index(1).String())
		}
		b := make([]byte, v.Get("body").Length())
		js.CopyBytesToGo(b, v.Get("body"))
		r.body = string(b)
		done <- r
		return nil
	})
	onErr := js.FuncOf(func(_ js.Value, a []js.Value) any {
		done <- jsResult{err: a[0].String()}
		return nil
	})
	defer onOK.Release()
	defer onErr.Release()
	serve(h, []js.Value{js.ValueOf(method), js.ValueOf(url), hdrs, jsBody}).(js.Value).Call("then", onOK, onErr)
	select {
	case r := <-done:
		return r
	case <-time.After(30 * time.Second):
		t.Fatal("serve's Promise never settled")
		return jsResult{}
	}
}

func TestServe_PassesRequestThroughAndAnswersInJS(t *testing.T) {
	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		w.Header()["X-Seen"] = r.Header.Values("X-Multi")
		w.Header().Set("X-Method", r.Method)
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, r.URL.RequestURI()+"|"+string(b))
	})
	r := call(t, echo, "POST", "/api/vms?x=1", [][2]string{{"X-Multi", "a"}, {"X-Multi", "b"}}, []byte("payload\x00\xff"))
	if r.err != "" {
		t.Fatalf("rejected: %s", r.err)
	}
	if r.status != http.StatusTeapot || r.body != "/api/vms?x=1|payload\x00\xff" {
		t.Errorf("got %d %q, want 418 with the URL and binary body round-tripped", r.status, r.body)
	}
	if strings.Join(r.headers["X-Seen"], ",") != "a,b" || r.headers["X-Method"][0] != "POST" {
		t.Errorf("headers = %v, want repeated request headers kept and response headers returned", r.headers)
	}
}

func TestServe_NullBodyIsEmpty(t *testing.T) {
	h := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		_, _ = io.WriteString(w, "len="+string(rune('0'+len(b))))
	})
	if r := call(t, h, "GET", "/", nil, nil); r.status != 200 || r.body != "len=0" {
		t.Errorf("got %+v", r)
	}
}

func TestServe_BadRequestRejectsThePromise(t *testing.T) {
	h := http.HandlerFunc(func(http.ResponseWriter, *http.Request) { t.Error("handler reached") })
	if r := call(t, h, "GET", "http://[::1", nil, nil); r.err == "" {
		t.Errorf("an unparsable URL resolved with %+v, want a rejected Promise", r)
	}
}
