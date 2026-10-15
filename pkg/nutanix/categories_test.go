package nutanix

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

// categoriesFixture is the prism v4 categories list shape (data[].extId,
// key, value), as nutanixdev's v4 categories sample reads it.
var categoriesFixture = map[string]string{
	"env=prod":  "cat-env-prod",
	"env=stage": "cat-env-stage",
}

// serveCategories honours $filter=key eq '…' and value eq '…'.
func (f *fakePC) serveCategories(w http.ResponseWriter, r *http.Request) {
	filter := r.URL.Query().Get("$filter")
	var key, value string
	if k, rest, ok := strings.Cut(strings.TrimPrefix(filter, "key eq '"), "' and value eq '"); ok {
		key, value = k, strings.TrimSuffix(rest, "'")
	}
	data := []map[string]string{}
	if id, ok := categoriesFixture[key+"="+value]; ok {
		data = append(data, map[string]string{"extId": id, "key": key, "value": value})
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
}

func TestParseCategory(t *testing.T) {
	for _, tag := range []string{"env=prod", "env:prod", " env = prod "} {
		if k, v, err := ParseCategory(tag); err != nil || k != "env" || v != "prod" {
			t.Errorf("ParseCategory(%q) = %q, %q, %v", tag, k, v, err)
		}
	}
	for _, tag := range []string{"prod", "=prod", "env=", ""} {
		if _, _, err := ParseCategory(tag); err == nil || !strings.Contains(err.Error(), "key=value") {
			t.Errorf("ParseCategory(%q) = %v, want a key=value refusal", tag, err)
		}
	}
}

func TestSetCategoryAssociatesWithETag(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)
	task, err := c.SetCategory("web-prod", "env=stage", true)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.WaitTask(task, 0); err != nil {
		t.Fatal(err)
	}
	var post *recordedRequest
	for i, r := range f.requests {
		if r.Method == http.MethodPost {
			post = &f.requests[i]
		}
		if strings.HasSuffix(r.Path, "/config/categories") &&
			r.Query.Get("$filter") != "key eq 'env' and value eq 'stage'" {
			t.Errorf("category filter = %q", r.Query.Get("$filter"))
		}
	}
	if post == nil {
		t.Fatal("no associate request")
	}
	if want := "/api/vmm/v4.0/ahv/config/vms/11111111-1111-1111-1111-111111111111/$actions/associate-categories"; post.Path != want {
		t.Errorf("path = %s, want %s", post.Path, want)
	}
	if post.IfMatch != "etag-11111111-1111-1111-1111-111111111111" {
		t.Errorf("If-Match = %q", post.IfMatch)
	}
	if post.Body != `{"categories":[{"extId":"cat-env-stage"}]}` {
		t.Errorf("body = %s", post.Body)
	}
}

func TestSetCategoryDisassociates(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)
	if _, err := c.SetCategory("web-prod", "env:prod", false); err != nil {
		t.Fatal(err)
	}
	last := f.lastRequest()
	if !strings.HasSuffix(last.Path, "/$actions/disassociate-categories") || last.Body != `{"categories":[{"extId":"cat-env-prod"}]}` {
		t.Errorf("last request = %+v", last)
	}
}

func TestSetCategoryAlreadyInPlaceIsANoop(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)
	for _, tc := range []struct {
		tag string
		on  bool
	}{{"env=prod", true}, {"env=stage", false}} {
		task, err := c.SetCategory("web-prod", tc.tag, tc.on)
		if err != nil || task.Valid() {
			t.Errorf("SetCategory(%s, %v) = %v, %v; want no task", tc.tag, tc.on, task, err)
		}
	}
	for _, r := range f.requests {
		if r.Method == http.MethodPost {
			t.Errorf("a no-op change wrote: %+v", r)
		}
	}
}

func TestSetCategoryUnknownIsRefused(t *testing.T) {
	f := newFakePC(t)
	c := testClient(t, f)
	_, err := c.SetCategory("web-prod", "team=blue", true)
	if err == nil || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v, want a missing-category refusal", err)
	}
}
