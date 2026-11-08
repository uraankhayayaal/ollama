package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"ai/infrastructure/workspace"
)

// --- HTTP Helpers ---

func postJSON(t *testing.T, handler http.Handler, path string, body any) (int, []byte) {
	t.Helper()
	data, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader(data))
	req.Header.Set("Content-Type", "application/json")
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

func getJSON(t *testing.T, handler http.Handler, path string) (int, []byte) {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	handler.ServeHTTP(rec, req)
	return rec.Code, rec.Body.Bytes()
}

// --- Project Registration ---

func TestE2E_ProjectRegistration(t *testing.T) {
	_, handler, _ := newTestServer(t)

	dir := t.TempDir()
	status, body := postJSON(t, handler, "/api/projects", map[string]string{
		"path_or_git": dir,
	})
	if status != http.StatusOK {
		t.Fatalf("register: %d %s", status, body)
	}

	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if _, ok := resp["project_name"]; !ok {
		t.Fatalf("missing project_name: %v", resp)
	}
	if resp["status"] != "idle" {
		t.Fatalf("expected idle status, got %v", resp["status"])
	}
}

func TestE2E_ProjectRegistrationIdempotent(t *testing.T) {
	srv, handler, _ := newTestServer(t)

	name := "idempotent-proj"
	dir := t.TempDir()
	if _, err := srv.reg.Add(workspace.AddParams{
		Name:    name,
		Kind:    workspace.KindDir,
		Root:    dir,
		Confirm: true,
	}); err != nil {
		t.Fatal(err)
	}

	_, firstBody := getJSON(t, handler, "/api/projects/"+name)
	var firstResp map[string]any
	json.Unmarshal(firstBody, &firstResp)

	_, secondBody := getJSON(t, handler, "/api/projects/"+name)
	var secondResp map[string]any
	json.Unmarshal(secondBody, &secondResp)

	if firstResp["project_name"] != secondResp["project_name"] {
		t.Fatalf("idempotent lookup name mismatch: %q vs %q",
			firstResp["project_name"], secondResp["project_name"])
	}
	if firstResp["status"] != secondResp["status"] {
		t.Fatalf("idempotent lookup status mismatch: %q vs %q",
			firstResp["status"], secondResp["status"])
	}
}

func TestE2E_ListProjectsEmpty(t *testing.T) {
	_, handler, _ := newTestServer(t)

	status, body := getJSON(t, handler, "/api/projects")
	if status != http.StatusOK {
		t.Fatalf("list empty: %d %s", status, body)
	}

	var list []map[string]any
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected empty list, got %d items", len(list))
	}
}

func TestE2E_ListProjectsWithProjects(t *testing.T) {
	srv, handler, _ := newTestServer(t)

	for _, name := range []string{"proj-a", "proj-b"} {
		dir := filepath.Join(t.TempDir(), name)
		os.MkdirAll(dir, 0o755)
		if _, err := srv.reg.Add(workspace.AddParams{
			Name:    name,
			Kind:    workspace.KindDir,
			Root:    dir,
			Confirm: true,
		}); err != nil {
			t.Fatal(err)
		}
	}

	status, body := getJSON(t, handler, "/api/projects")
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, body)
	}

	var list []map[string]any
	if err := json.Unmarshal(body, &list); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(list))
	}
}

func TestE2E_ProjectMeta(t *testing.T) {
	srv, handler, _ := newTestServer(t)

	name := "meta-test"
	dir := t.TempDir()
	if _, err := srv.reg.Add(workspace.AddParams{
		Name:    name,
		Kind:    workspace.KindDir,
		Root:    dir,
		Confirm: true,
	}); err != nil {
		t.Fatal(err)
	}

	status, body := getJSON(t, handler, "/api/projects/"+name)
	if status != http.StatusOK {
		t.Fatalf("meta: %d %s", status, body)
	}
	var resp map[string]any
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("parse: %v", err)
	}
	meta, ok := resp["meta"].(map[string]any)
	if !ok {
		t.Fatalf("missing meta field: %v", resp)
	}
	if meta["name"] != name {
		t.Fatalf("expected %s, got %v", name, meta["name"])
	}
	if meta["kind"] != "dir" {
		t.Fatalf("expected dir kind, got %v", meta["kind"])
	}
}

func TestE2E_ProjectNotFound(t *testing.T) {
	_, handler, _ := newTestServer(t)

	status, _ := getJSON(t, handler, "/api/projects/nonexistent")
	if status != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", status)
	}
}

// --- Git URL Detection ---

func TestE2E_GitURLDetection(t *testing.T) {
	tests := []struct {
		url   string
		isGit bool
	}{
		{"https://github.com/user/repo.git", true},
		{"http://github.com/user/repo.git", true},
		{"ssh://git@github.com/user/repo.git", true},
		{"git://github.com/user/repo.git", true},
		{"git@github.com:user/repo.git", true},
		{"/local/path/to/project", false},
		{"/tmp/project", false},
		{"project-name", false},
	}

	for _, tc := range tests {
		if got := isGitURL(tc.url); got != tc.isGit {
			t.Errorf("isGitURL(%q) = %v, want %v", tc.url, got, tc.isGit)
		}
	}
}

// --- Project Kind Detection ---

func TestE2E_ProjectKindDir(t *testing.T) {
	_, handler, _ := newTestServer(t)

	dir := t.TempDir()
	status, body := postJSON(t, handler, "/api/projects", map[string]string{
		"path_or_git": dir,
	})
	if status != http.StatusOK {
		t.Fatalf("register: %d %s", status, body)
	}

	var resp map[string]any
	json.Unmarshal(body, &resp)
	if resp["kind"] != "dir" {
		t.Fatalf("expected dir kind, got %v", resp["kind"])
	}
}

func TestE2E_ProjectKindTemp(t *testing.T) {
	_, handler, _ := newTestServer(t)

	status, body := postJSON(t, handler, "/api/projects", map[string]string{
		"path_or_git": "nonexistent-temp-proj",
	})
	if status != http.StatusOK {
		t.Fatalf("register: %d %s", status, body)
	}

	var resp map[string]any
	json.Unmarshal(body, &resp)
	if resp["kind"] != "temp" {
		t.Fatalf("expected temp kind, got %v", resp["kind"])
	}
}

// --- Multiple Projects ---

func TestE2E_MultipleProjectsIndependent(t *testing.T) {
	srv, handler, _ := newTestServer(t)

	var projectNames []string
	for i := 0; i < 3; i++ {
		name := "multi-proj-" + strings.Repeat("x", i+1)
		dir := t.TempDir()
		if _, err := srv.reg.Add(workspace.AddParams{
			Name:    name,
			Kind:    workspace.KindDir,
			Root:    dir,
			Confirm: true,
		}); err != nil {
			t.Fatal(err)
		}
		projectNames = append(projectNames, name)
	}

	status, body := getJSON(t, handler, "/api/projects")
	if status != http.StatusOK {
		t.Fatalf("list: %d %s", status, body)
	}

	var list []map[string]any
	json.Unmarshal(body, &list)
	if len(list) != 3 {
		t.Fatalf("expected 3 projects, got %d", len(list))
	}

	for _, name := range projectNames {
		found := false
		for _, item := range list {
			if item["project_name"] == name {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("project %s not found in list", name)
		}
	}
}

// --- dirBase ---

func TestE2E_DirBase(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/home/user/project", "project"},
		{"/tmp/test", "test"},
		{"C:\\Users\\project", "project"},
		{"project", "project"},
		{"/", ""},
	}

	for _, tc := range tests {
		got := dirBase(tc.path)
		if got != tc.want {
			t.Errorf("dirBase(%q) = %q, want %q", tc.path, got, tc.want)
		}
	}
}
