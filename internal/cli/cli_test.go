package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
)

func environment(t *testing.T, endpoint string) {
	t.Helper()
	for _, key := range []string{"CASDOOR_PROFILE", "CASDOOR_CONFIG", "CASDOOR_TOKEN", "CASDOOR_OWNER"} {
		t.Setenv(key, "")
	}
	t.Setenv("XDG_CONFIG_HOME", t.TempDir())
	t.Setenv("CASDOOR_ENDPOINT", endpoint)
	t.Setenv("CASDOOR_CLIENT_ID", "test-client")
	t.Setenv("CASDOOR_CLIENT_SECRET", "test-secret")
}

func invoke(t *testing.T, input string, args ...string) (int, string, string) {
	t.Helper()
	var out, err bytes.Buffer
	code := Run(args, strings.NewReader(input), &out, &err, "test")
	return code, out.String(), err.String()
}

func decode(t *testing.T, s string) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal([]byte(s), &m); err != nil {
		t.Fatalf("not JSON: %s", s)
	}
	return m
}

func TestResourceCRUD(t *testing.T) {
	for _, kind := range []string{"user", "role", "permission", "application"} {
		t.Run(kind, func(t *testing.T) {
			var paths []string
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				a, b, ok := r.BasicAuth()
				if !ok || a != "test-client" || b != "test-secret" {
					t.Error("missing Basic Auth")
				}
				if r.URL.Query().Get("clientSecret") != "" {
					t.Error("credential in URL")
				}
				paths = append(paths, r.Method+" "+r.URL.Path)
				switch {
				case r.Method == "GET" && r.URL.Path == "/api/get-"+resources[kind]:
					if r.URL.Query().Get("owner") != "org" {
						t.Error("owner not sent")
					}
					if r.URL.Query().Get("p") != "1" {
						t.Error("page not sent")
					}
					fmt.Fprint(w, `{"status":"ok","data":[{"owner":"org","name":"测试"}],"data2":7}`)
				case r.Method == "GET":
					if r.URL.Query().Get("id") != "org/测试" {
						t.Error("ID encoding")
					}
					fmt.Fprint(w, `{"owner":"org","name":"测试","displayName":"before","arn":"custom:arn","third_role_Id":"cloud-id","properties":{"large":9007199254740993},"users":["org/alice"],"roles":[],"isEnabled":true}`)
				case r.Method == "POST":
					var m map[string]json.RawMessage
					if err := json.NewDecoder(r.Body).Decode(&m); err != nil {
						t.Error(err)
					}
					if r.URL.Path == "/api/update-"+kind {
						if string(m["arn"]) != `"custom:arn"` || string(m["third_role_Id"]) != `"cloud-id"` {
							t.Error("custom fields lost")
						}
						if !strings.Contains(string(m["properties"]), "9007199254740993") {
							t.Error("integer precision lost")
						}
						if string(m["displayName"]) != `"after"` {
							t.Error("patch not applied")
						}
						if string(m["isEnabled"]) != "true" {
							t.Error("unrelated flag lost")
						}
					}
					fmt.Fprint(w, `{"status":"ok","data":"Affected"}`)
				}
			}))
			defer server.Close()
			environment(t, server.URL)
			commands := [][]string{
				{kind, "list", "--owner", "org", "--page", "1", "--page-size", "2"},
				{kind, "get", "org/测试"},
				{kind, "create", "--file", "-"},
				{kind, "update", "org/测试", "--file", "-"},
				{kind, "delete", "org/测试"},
			}
			for i, args := range commands {
				input := `{"owner":"org","name":"测试"}`
				if i == 3 {
					input = `{"displayName":"after"}`
				}
				code, out, err := invoke(t, input, args...)
				if code != 0 {
					t.Fatalf("%v: %s", args, err)
				}
				m := decode(t, out)
				if i == 0 && m["total"] != float64(7) {
					t.Error("missing pagination total")
				}
				if i >= 2 && m["affected"] != true {
					t.Error("missing affected status")
				}
			}
			if len(paths) != 6 {
				t.Errorf("unexpected requests: %v", paths)
			}
		})
	}
}

func TestDryRunAndMembership(t *testing.T) {
	var writes atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writes.Add(1)
		}
		fmt.Fprint(w, `{"owner":"org","name":"readers","users":["org/alice"],"roles":["org/child"],"arn":"preserved","clientSecret":"do-not-print"}`)
	}))
	defer s.Close()
	environment(t, s.URL)
	for _, kind := range []string{"role", "permission"} {
		for _, action := range []string{"add-user", "remove-user", "add-role", "remove-role"} {
			flag, value, field := "--user", "org/alice", "users"
			if strings.HasSuffix(action, "role") {
				flag, value, field = "--role", "org/child", "roles"
			}
			code, out, err := invoke(t, "", kind, action, "org/readers", flag, value, "--dry-run")
			if code != 0 {
				t.Fatal(err)
			}
			m := decode(t, out)
			if m["dryRun"] != true {
				t.Fatal("dryRun not set")
			}
			body := m["data"].(map[string]any)["body"].(map[string]any)
			if body["arn"] != "preserved" {
				t.Fatal("lost custom role data")
			}
			members := body[field].([]any)
			want := 1
			if strings.HasPrefix(action, "remove") {
				want = 0
			}
			if len(members) != want {
				t.Fatalf("%s wrong membership %v", action, members)
			}
			if strings.Contains(out, "do-not-print") {
				t.Fatal("secret in dry run")
			}
		}
	}
	code, _, err := invoke(t, `{"owner":"org","name":"new"}`, "user", "create", "--file", "-", "--dry-run")
	if code != 0 {
		t.Fatal(err)
	}
	code, _, err = invoke(t, "", "application", "delete", "org/readers", "--dry-run")
	if code != 0 {
		t.Fatal(err)
	}
	if writes.Load() != 0 {
		t.Fatal("dry-run wrote to server")
	}
}

func TestResponseErrorsAndRedaction(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		bad        bool
	}{
		{"raw-list", `[{"name":"x","clientSecret":"hidden","nested":{"access_token":"hidden"}}]`, 200, false},
		{"api-error", `{"status":"error","msg":"bad test-secret"}`, 200, true},
		{"forbidden", `<html>denied</html>`, 403, true},
		{"html", `<html>login</html>`, 200, true},
		{"trailing", `{} {}`, 200, true},
		{"not-found", `null`, 200, true},
		{"unaffected", `{"status":"ok","data":"Unaffected"}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); fmt.Fprint(w, tc.body) }))
			defer s.Close()
			environment(t, s.URL)
			code, out, err := invoke(t, "", "user", "get", "org/a")
			if (code != 0) != tc.bad {
				t.Fatalf("code=%d out=%s err=%s", code, out, err)
			}
			if strings.Contains(out+err, "hidden") || strings.Contains(out+err, "test-secret") {
				t.Fatal("leaked secret")
			}
			if tc.name == "unaffected" && decode(t, out)["affected"] != false {
				t.Error("Unaffected reported as success change")
			}
			if tc.bad && out != "" {
				t.Fatal("error polluted stdout")
			}
		})
	}
}

func TestInvalidInputDoesNotWrite(t *testing.T) {
	var writes atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writes.Add(1)
		}
		fmt.Fprint(w, `{"owner":"org","name":"a","roles":[],"users":[]}`)
	}))
	defer s.Close()
	environment(t, s.URL)
	for _, tc := range []struct {
		input string
		args  []string
	}{
		{`null`, []string{"user", "create", "--file", "-"}},
		{`{"owner":"org"}`, []string{"role", "create", "--file", "-"}},
		{`{"owner":"org","name":"x","users":["alice"]}`, []string{"permission", "create", "--file", "-"}},
		{`{} {}`, []string{"user", "create", "--file", "-"}},
		{`{"name":"b"}`, []string{"user", "update", "org/a", "--file", "-"}},
		{`{"owner":"other"}`, []string{"role", "update", "org/a", "--file", "-"}},
		{``, []string{"user", "delete", "no-owner"}},
		{``, []string{"user", "list", "--owner", "org", "--page", "1"}},
		{``, []string{"user", "list", "--owner", "org", "--field", "name", "--value", "a"}},
		{``, []string{"role", "add-user", "org/a", "--user", "a"}},
		{``, []string{"user", "get", "org/a", "--bogus"}},
		{`{"password":"new-password"}`, []string{"user", "update", "org/a", "--file", "-"}},
		{`{"displayName":"New"}`, []string{"user", "update", "org/a", "--file", "-", "--columns", "password"}},
		{``, []string{"application", "list", "--owner", "admin", "--organization", "org", "--page", "1", "--page-size", "2"}},
	} {
		code, _, err := invoke(t, tc.input, tc.args...)
		if code == 0 {
			t.Fatalf("accepted %v", tc.args)
		}
		decode(t, err)
	}
	if writes.Load() != 0 {
		t.Fatal("invalid input triggered write")
	}
}

func TestRedirectDoesNotForwardAuth(t *testing.T) {
	var called atomic.Bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called.Store(true) }))
	defer target.Close()
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer s.Close()
	environment(t, s.URL)
	code, _, _ := invoke(t, "", "user", "get", "org/a")
	if code == 0 || called.Load() {
		t.Fatal("followed redirect")
	}
}

func TestConfigPermissionsAndPrecedence(t *testing.T) {
	environment(t, "https://env.example")
	p := filepath.Join(t.TempDir(), "config.json")
	raw := `{"defaultProfile":"prod","profiles":{"prod":{"endpoint":"https://profile.example","clientId":"id","clientSecret":"secret","owner":"org"}}}`
	if err := os.WriteFile(p, []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	cfg, err := loadProfile(Options{Config: p, Endpoint: "https://flag.example"})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint != "https://flag.example" || cfg.ClientID != "test-client" || cfg.Owner != "org" {
		t.Fatal("wrong precedence")
	}
	if err := os.Chmod(p, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProfile(Options{Config: p}); err == nil {
		t.Fatal("accepted world-readable config")
	}
	if err := os.Chmod(p, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadProfile(Options{Config: p, Profile: "missing"}); err == nil {
		t.Fatal("accepted unknown profile")
	}
	for _, endpoint := range []string{"http://example.com", "https://user:pass@example.com", "https://example.com?token=abc"} {
		if _, err := loadProfile(Options{Endpoint: endpoint}); err == nil {
			t.Errorf("accepted endpoint %s", endpoint)
		}
	}
}

func TestBearerAndUserExtendedRoles(t *testing.T) {
	var posts atomic.Int32
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-bearer" {
			t.Error("missing bearer token")
		}
		if r.Method == "POST" {
			posts.Add(1)
			if r.URL.Query().Get("columns") != "display_name" {
				t.Error("missing columns")
			}
			fmt.Fprint(w, `{"status":"ok","data":"Affected"}`)
			return
		}
		fmt.Fprint(w, `{"owner":"org","name":"a","roles":[{"name":"extended-role"}],"isAdmin":true}`)
	}))
	defer s.Close()
	environment(t, s.URL)
	t.Setenv("CASDOOR_TOKEN", "test-bearer")
	code, _, err := invoke(t, `{"displayName":"New"}`, "user", "update", "org/a", "--file", "-", "--columns", "display_name")
	if code != 0 {
		t.Fatal(err)
	}
	if posts.Load() != 1 {
		t.Fatal("update not performed")
	}
}
