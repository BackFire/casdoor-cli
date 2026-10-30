package cli

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const usage = `casdoor — Casdoor management CLI

Usage:
  casdoor <user|role|permission|application> list [flags]
  casdoor <resource> get OWNER/NAME [flags]
  casdoor <resource> create --file FILE [flags]
  casdoor <resource> update OWNER/NAME --file PATCH.json [flags]
  casdoor <resource> delete OWNER/NAME [flags]
  casdoor <role|permission> <add-user|remove-user> OWNER/NAME --user ORG/USER
  casdoor <role|permission> <add-role|remove-role> OWNER/NAME --role OWNER/ROLE
  casdoor permission by-role OWNER/ROLE [flags]
  casdoor config show [flags]
  casdoor version

Global flags (after resource/action, before or after ID):
  --config FILE          JSON config (default: ~/.config/casdoor/config.json)
  --profile NAME         Select profile (or CASDOOR_PROFILE)
  --endpoint URL         Override server endpoint
  --owner OWNER          List owner (required unless profile/env sets it)
  --timeout DURATION     Request timeout (default: 30s)
  --show-secrets         Include credentials in output (default: redacted)
  --dry-run              Preview a write; update previews perform a GET only

Input/output:
  --file FILE            JSON object; '-' reads standard input
  --columns a,b          User update DB columns; otherwise server defaults apply
  --page N --page-size N Paginated list (both required together)
  --field F --value V    Server-side list filter (requires pagination)
  --sort-field F --sort-order ascend|descend   Sort a paginated list
  --organization ORG    Filter application list by organization

Results are JSON on stdout; errors are JSON on stderr with exit code 1.
Updates merge top-level fields into the existing object; arrays/objects replace
whole fields. Unknown server fields are preserved. Identity changes are rejected.
No automatic retries or interactive prompts. See README for authentication.
`

var resources = map[string]string{"user": "users", "role": "roles", "permission": "permissions", "application": "applications"}

func Run(args []string, in io.Reader, out, errOut io.Writer, version string) int {
	err := run(args, in, out, errOut, version)
	if err == nil {
		return 0
	}
	// Do not echo credentials if a server error happens to include them.
	message := err.Error()
	for _, key := range []string{"CASDOOR_CLIENT_SECRET", "CASDOOR_TOKEN"} {
		if v := os.Getenv(key); v != "" {
			message = strings.ReplaceAll(message, v, "[REDACTED]")
		}
	}
	_ = json.NewEncoder(errOut).Encode(map[string]any{"error": map[string]string{"message": message}})
	return 1
}

func parse(args []string, errOut io.Writer) (Options, []string, error) {
	var o Options
	f := flag.NewFlagSet("casdoor", flag.ContinueOnError)
	f.SetOutput(io.Discard)
	f.StringVar(&o.Config, "config", "", "config file")
	f.StringVar(&o.Profile, "profile", "", "profile")
	f.StringVar(&o.Endpoint, "endpoint", "", "endpoint")
	f.StringVar(&o.Owner, "owner", "", "list owner")
	f.StringVar(&o.File, "file", "", "JSON input")
	f.StringVar(&o.User, "user", "", "user ID")
	f.StringVar(&o.Role, "role", "", "role ID")
	f.StringVar(&o.Columns, "columns", "", "user update columns")
	f.IntVar(&o.Page, "page", 0, "page")
	f.IntVar(&o.PageSize, "page-size", 0, "page size")
	f.StringVar(&o.Field, "field", "", "filter field")
	f.StringVar(&o.Value, "value", "", "filter value")
	f.StringVar(&o.SortField, "sort-field", "", "sort field")
	f.StringVar(&o.SortOrder, "sort-order", "", "sort order")
	f.StringVar(&o.Organization, "organization", "", "application organization")
	f.DurationVar(&o.Timeout, "timeout", 30*time.Second, "timeout")
	f.BoolVar(&o.DryRun, "dry-run", false, "preview")
	f.BoolVar(&o.ShowSecrets, "show-secrets", false, "show credentials")
	// The standard flag parser stops at a positional argument. Reorder known flags
	// to allow both `get org/name --profile prod` and the reverse order.
	var flags, pos []string
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			pos = append(pos, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(a, "-") || a == "-" {
			pos = append(pos, a)
			continue
		}
		key := strings.TrimLeft(strings.SplitN(a, "=", 2)[0], "-")
		opt := f.Lookup(key)
		if opt == nil {
			return o, nil, fmt.Errorf("unknown flag %s", a)
		}
		flags = append(flags, a)
		if !strings.Contains(a, "=") {
			if b, ok := opt.Value.(interface{ IsBoolFlag() bool }); ok && b.IsBoolFlag() {
				continue
			}
			if i+1 >= len(args) {
				return o, nil, fmt.Errorf("missing value for %s", a)
			}
			i++
			flags = append(flags, args[i])
		}
	}
	if err := f.Parse(flags); err != nil {
		return o, nil, err
	}
	if o.Timeout <= 0 {
		return o, nil, errors.New("timeout must be positive")
	}
	return o, pos, nil
}

func run(args []string, in io.Reader, out, errOut io.Writer, version string) error {
	if len(args) == 0 || args[0] == "help" || args[0] == "--help" || args[0] == "-h" {
		_, err := io.WriteString(out, usage)
		return err
	}
	if args[0] == "version" || args[0] == "--version" {
		return json.NewEncoder(out).Encode(map[string]string{"version": version})
	}
	if len(args) < 2 {
		return errors.New("expected resource and action; see casdoor --help")
	}
	kind, action := args[0], args[1]
	plural, valid := resources[kind]
	if !valid && kind != "config" {
		return fmt.Errorf("unknown resource %q", kind)
	}
	for _, a := range args[2:] {
		if a == "--help" || a == "-h" {
			_, err := io.WriteString(out, usage)
			return err
		}
	}
	o, pos, err := parse(args[2:], errOut)
	if err != nil {
		return err
	}
	p, err := loadProfile(o)
	if err != nil {
		return err
	}
	// Any later server error is sanitized against profile credentials too.
	err = execute(kind, plural, action, o, pos, p, in, out)
	if err != nil {
		s := err.Error()
		for _, secret := range []string{p.ClientSecret, p.Token} {
			if secret != "" {
				s = strings.ReplaceAll(s, secret, "[REDACTED]")
			}
		}
		return errors.New(s)
	}
	return nil
}

func execute(kind, plural, action string, o Options, pos []string, p Profile, in io.Reader, out io.Writer) error {
	emit := func(r Result) error {
		if !o.ShowSecrets {
			r.Data = redact(r.Data)
		}
		enc := json.NewEncoder(out)
		enc.SetIndent("", "  ")
		return enc.Encode(r)
	}
	if kind == "config" {
		if action != "show" || len(pos) != 0 {
			return errors.New("expected config show")
		}
		// Config show always redacts credentials, including with --show-secrets.
		return emit(Result{Data: map[string]any{"endpoint": p.Endpoint, "owner": p.Owner, "clientId": p.ClientID, "basicAuthConfigured": p.ClientSecret != "", "bearerAuthConfigured": p.Token != ""}})
	}
	c := newClient(p, o.Timeout)
	q := url.Values{}
	if action == "list" {
		if len(pos) != 0 {
			return errors.New("list takes no positional arguments")
		}
		if p.Owner == "" {
			return errors.New("list requires --owner (or profile owner / CASDOOR_OWNER)")
		}
		if (o.Page == 0) != (o.PageSize == 0) || o.Page < 0 || o.PageSize < 0 {
			return errors.New("--page and --page-size must both be positive")
		}
		if o.Page == 0 && (o.Field != "" || o.Value != "" || o.SortField != "" || o.SortOrder != "") {
			return errors.New("filters and sorting require --page and --page-size on the legacy server")
		}
		if (o.Field == "") != (o.Value == "") {
			return errors.New("--field and --value must be provided together")
		}
		if o.SortOrder != "" && o.SortOrder != "ascend" && o.SortOrder != "descend" {
			return errors.New("sort order must be ascend or descend")
		}
		if o.Organization != "" && kind != "application" {
			return errors.New("--organization is only valid for application list")
		}
		if o.Organization != "" && o.Page > 0 {
			return errors.New("legacy application list ignores --organization with pagination; omit --page/--page-size")
		}
		q.Set("owner", p.Owner)
		if o.Page > 0 {
			q.Set("p", fmt.Sprint(o.Page))
			q.Set("pageSize", fmt.Sprint(o.PageSize))
		}
		for k, v := range map[string]string{"field": o.Field, "value": o.Value, "sortField": o.SortField, "sortOrder": o.SortOrder, "organization": o.Organization} {
			if v != "" {
				q.Set(k, v)
			}
		}
		r, err := c.request(http.MethodGet, "get-"+plural, q, nil)
		if err != nil {
			return err
		}
		return emit(r)
	}
	if action == "create" {
		if len(pos) != 0 {
			return errors.New("create takes no positional arguments; identity comes from --file")
		}
		obj, err := readObject(o.File, in)
		if err != nil {
			return err
		}
		if _, err := identity(obj); err != nil {
			return err
		}
		if _, exists := obj["createdTime"]; !exists {
			obj["createdTime"] = time.Now().Format(time.RFC3339)
		}
		if kind == "role" || kind == "permission" {
			if err := validateMembers(obj); err != nil {
				return err
			}
		}
		return write(c, "add-"+kind, q, obj, o, emit)
	}
	if len(pos) != 1 {
		return errors.New("expected exactly one OWNER/NAME ID")
	}
	id := pos[0]
	if err := validateID(id); err != nil {
		return err
	}
	q.Set("id", id)
	if action == "by-role" && kind == "permission" {
		r, err := c.request(http.MethodGet, "get-permissions-by-role", q, nil)
		if err != nil {
			return err
		}
		return emit(r)
	}
	switch action {
	case "get":
		r, err := c.request(http.MethodGet, "get-"+kind, q, nil)
		if err != nil {
			return err
		}
		if r.Data == nil {
			return fmt.Errorf("%s %s not found", kind, id)
		}
		return emit(r)
	case "delete":
		pair := strings.SplitN(id, "/", 2)
		return write(c, "delete-"+kind, nil, map[string]any{"owner": pair[0], "name": pair[1]}, o, emit)
	case "update", "add-user", "remove-user", "add-role", "remove-role":
		if action != "update" && kind != "role" && kind != "permission" {
			return errors.New("membership commands require role or permission")
		}
		var patch map[string]any
		if action == "update" {
			var err error
			patch, err = readObject(o.File, in)
			if err != nil {
				return err
			}
			if kind == "user" {
				for k := range patch {
					if strings.Contains(strings.ToLower(k), "password") {
						return errors.New("user passwords require the dedicated server password API, not update")
					}
				}
				if strings.Contains(strings.ToLower(o.Columns), "password") {
					return errors.New("password columns cannot be updated with this command")
				}
			}
			if len(patch) == 0 {
				return errors.New("empty update")
			}
		}
		r, err := c.request(http.MethodGet, "get-"+kind, q, nil)
		if err != nil {
			return err
		}
		obj, ok := r.Data.(map[string]any)
		if !ok {
			return fmt.Errorf("%s %s not found or invalid object response", kind, id)
		}
		if current, err := identity(obj); err != nil || current != id {
			return errors.New("server returned a different or invalid identity")
		}
		if action == "update" {
			for k, v := range patch {
				obj[k] = v
			}
			if updated, err := identity(obj); err != nil || updated != id {
				return errors.New("owner/name changes are not supported; use the server's rename workflow")
			}
		} else {
			field, member := "users", o.User
			if strings.HasSuffix(action, "role") {
				field, member = "roles", o.Role
			}
			if err := validateID(member); err != nil {
				return fmt.Errorf("membership requires a valid --user or --role: %w", err)
			}
			members := []any{}
			if old := obj[field]; old != nil {
				var ok bool
				members, ok = old.([]any)
				if !ok {
					return errors.New("invalid membership array from server")
				}
			}
			next := []any{}
			found := false
			for _, v := range members {
				if v == member {
					found = true
					if strings.HasPrefix(action, "remove-") {
						continue
					}
				}
				next = append(next, v)
			}
			if strings.HasPrefix(action, "add-") && !found {
				next = append(next, member)
			}
			obj[field] = next
		}
		if kind == "role" || kind == "permission" {
			if err := validateMembers(obj); err != nil {
				return err
			}
		}
		if o.Columns != "" {
			if kind != "user" {
				return errors.New("--columns is only supported for user update")
			}
			q.Set("columns", o.Columns)
		}
		return write(c, "update-"+kind, q, obj, o, emit)
	default:
		return fmt.Errorf("unknown action %q", action)
	}
}

func write(c *Client, action string, q url.Values, obj map[string]any, o Options, emit func(Result) error) error {
	if o.DryRun {
		return emit(Result{DryRun: true, Data: map[string]any{"method": "POST", "path": "/api/" + action, "query": q, "body": obj}})
	}
	r, err := c.request(http.MethodPost, action, q, obj)
	if err != nil {
		return err
	}
	return emit(r)
}

func readObject(path string, in io.Reader) (map[string]any, error) {
	if path == "" {
		return nil, errors.New("--file is required (use --file - for stdin)")
	}
	if path != "-" {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		defer f.Close()
		in = f
	}
	raw, err := io.ReadAll(io.LimitReader(in, (32<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > 32<<20 {
		return nil, errors.New("input exceeds 32 MiB")
	}
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.UseNumber()
	var obj map[string]any
	if err := dec.Decode(&obj); err != nil {
		return nil, fmt.Errorf("invalid JSON object: %w", err)
	}
	if obj == nil {
		return nil, errors.New("input must be a JSON object")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return nil, errors.New("input must contain exactly one JSON object")
	}
	return obj, nil
}

func validateID(id string) error {
	parts := strings.Split(id, "/")
	if len(parts) != 2 || strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return errors.New("ID must be OWNER/NAME")
	}
	return nil
}
func identity(obj map[string]any) (string, error) {
	owner, a := obj["owner"].(string)
	name, b := obj["name"].(string)
	if !a || !b {
		return "", errors.New("owner and name must be strings")
	}
	id := owner + "/" + name
	return id, validateID(id)
}
func validateMembers(obj map[string]any) error {
	for _, key := range []string{"users", "roles"} {
		if v := obj[key]; v != nil {
			values, ok := v.([]any)
			if !ok {
				return fmt.Errorf("%s must be an array", key)
			}
			for _, v := range values {
				s, ok := v.(string)
				if !ok {
					return fmt.Errorf("%s must contain ID strings", key)
				}
				if err := validateID(s); err != nil {
					return fmt.Errorf("%s: %w", key, err)
				}
			}
		}
	}
	return nil
}
