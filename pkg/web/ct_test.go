package web

import (
	"net/http"
	"strings"
	"testing"
)

func TestHandleListCTs_Empty(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Server.Close()

	fx.Runner.AddResponseKV("kubectl", []string{"get", "pvc", "-A", "-l", "corral.dev/ct=true", "-o", "json"}, `{"items":[]}`, nil)
	fx.Runner.AddResponseKV("kubectl", []string{"get", "pods", "-A", "-l", "corral.dev/ct=true", "-o", "json"}, `{"items":[]}`, nil)

	resp, err := http.Get(fx.Server.URL + "/api/cts")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHandleCreateCT_Success(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Server.Close()

	fx.Runner.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)

	body := strings.NewReader(`{"name":"web1","namespace":"corral-ct","image":"debian:bookworm","cpu":1,"mem":"512Mi","disk":"5Gi"}`)
	resp, err := http.Post(fx.Server.URL+"/api/cts", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("expected 201, got %d", resp.StatusCode)
	}
}

func TestHandleCreateCT_MissingFields(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Server.Close()

	body := strings.NewReader(`{}`)
	resp, err := http.Post(fx.Server.URL+"/api/cts", "application/json", body)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleCTAction_Start(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Server.Close()

	fx.Runner.AddResponseKV("kubectl", []string{
		"get", "pvc", "web1-data", "-n", "corral-ct", "-o",
		`jsonpath={.metadata.annotations.corral\.ct/spec}`,
	}, `{"image":"debian","cpu":1,"mem":"512Mi"}`, nil)
	fx.Runner.AddResponseKV("kubectl", []string{"apply", "-f", "-"}, "applied", nil)

	resp, err := http.Post(fx.Server.URL+"/api/cts/corral-ct/web1/start", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHandleCTAction_Stop(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Server.Close()

	fx.Runner.AddResponseKV("kubectl", []string{"delete", "pod", "web1", "-n", "corral-ct", "--ignore-not-found"}, "", nil)

	resp, err := http.Post(fx.Server.URL+"/api/cts/corral-ct/web1/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

func TestHandleCTAction_UnknownAction(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Server.Close()

	resp, err := http.Post(fx.Server.URL+"/api/cts/corral-ct/web1/frobnicate", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("expected 400, got %d", resp.StatusCode)
	}
}

func TestHandleDeleteCT(t *testing.T) {
	fx := NewTestFixture()
	defer fx.Server.Close()

	fx.Runner.AddResponseKV("kubectl", []string{"delete", "pod", "web1", "-n", "corral-ct", "--ignore-not-found"}, "", nil)
	fx.Runner.AddResponseKV("kubectl", []string{"delete", "svc", "web1-svc", "-n", "corral-ct", "--ignore-not-found"}, "", nil)
	fx.Runner.AddResponseKV("kubectl", []string{"delete", "pvc", "web1-data", "-n", "corral-ct", "--ignore-not-found"}, "", nil)

	req, _ := http.NewRequest(http.MethodDelete, fx.Server.URL+"/api/cts/corral-ct/web1", nil)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}
