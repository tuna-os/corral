//go:build js && wasm

// Command corral-wasm is the Corral dashboard, built for the browser, for the
// static demo site (#284). It serves the real web UI and API against the
// in-memory demo cluster (pkg/demo), with no socket: web-demo/sw.js, a
// service worker, hands each page request to corralServe and replays the
// answer. Build it with scripts/build-web-demo.sh.
package main

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"syscall/js"

	"github.com/tuna-os/corral/pkg/web"
)

func main() {
	handler, err := web.DemoHandler()
	if err != nil {
		js.Global().Get("console").Call("error", "corral: "+err.Error())
		return
	}
	js.Global().Set("corralServe", js.FuncOf(func(_ js.Value, args []js.Value) any {
		return serve(handler, args)
	}))
	// Keep the Go runtime alive so later requests reach the handler.
	select {}
}

// serve answers corralServe(method, url, headers, body). Headers are an array
// of [name, value] pairs and body is a Uint8Array or null. It returns a
// Promise of {status, headers, body}, since the handler may block and a
// js.FuncOf callback must not.
func serve(handler http.Handler, args []js.Value) any {
	method, url := args[0].String(), args[1].String()
	var headers [][2]string
	for i := 0; i < args[2].Length(); i++ {
		pair := args[2].Index(i)
		headers = append(headers, [2]string{pair.Index(0).String(), pair.Index(1).String()})
	}
	var body []byte
	if b := args[3]; !b.IsNull() && !b.IsUndefined() {
		body = make([]byte, b.Length())
		js.CopyBytesToGo(body, b)
	}

	promise := js.Global().Get("Promise")
	return promise.New(js.FuncOf(func(_ js.Value, p []js.Value) any {
		resolve, reject := p[0], p[1]
		go func() {
			req, err := http.NewRequest(method, url, bytes.NewReader(body))
			if err != nil {
				reject.Invoke(err.Error())
				return
			}
			for _, h := range headers {
				req.Header.Add(h[0], h[1])
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			res := rec.Result()
			out, _ := io.ReadAll(res.Body)

			jsHeaders := js.Global().Get("Array").New()
			for name, values := range res.Header {
				for _, v := range values {
					jsHeaders.Call("push", js.ValueOf([]any{name, v}))
				}
			}
			jsBody := js.Global().Get("Uint8Array").New(len(out))
			js.CopyBytesToJS(jsBody, out)
			resolve.Invoke(js.ValueOf(map[string]any{
				"status":  res.StatusCode,
				"headers": jsHeaders,
				"body":    jsBody,
			}))
		}()
		return nil
	}))
}
