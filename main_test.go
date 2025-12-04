package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func resetGlobals() {
	authRules = nil
	authEnabled = false
	customMIMETypes = make(map[string]string)
	customMIMEViewable = make(map[string]bool)
}

func TestParseAuthRules_SimplePassword(t *testing.T) {
	resetGlobals()
	if err := parseAuthRules([]string{"secret123"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(authRules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(authRules))
	}
	r := authRules[0]
	if r.Username != "" || r.Password != "secret123" || r.Permission != "rw" || r.Pattern != nil {
		t.Fatalf("unexpected rule: %+v", r)
	}
}

func TestParseAuthRules_UsernamePassword(t *testing.T) {
	resetGlobals()
	if err := parseAuthRules([]string{"alice:alicepass"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(authRules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(authRules))
	}
	r := authRules[0]
	if r.Username != "alice" || r.Password != "alicepass" || r.Permission != "rw" || r.Pattern != nil {
		t.Fatalf("unexpected rule: %+v", r)
	}
}

func TestParseAuthRules_PatternPermissions(t *testing.T) {
	resetGlobals()
	if err := parseAuthRules([]string{"bob:secret:r:*.txt"}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(authRules) != 1 {
		t.Fatalf("expected 1 rule, got %d", len(authRules))
	}
	r := authRules[0]
	if r.Username != "bob" || r.Password != "secret" || r.Permission != "r" || r.Pattern == nil {
		t.Fatalf("unexpected rule: %+v", r)
	}
	if !r.Pattern.MatchString("notes.txt") {
		t.Fatalf("pattern should match notes.txt")
	}
	if r.Pattern.MatchString("notes.jpg") {
		t.Fatalf("pattern should not match notes.jpg")
	}
}

func TestGlobToRegex(t *testing.T) {
	r, err := globToRegex("*.txt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r.MatchString("hello.txt") {
		t.Fatalf("expected hello.txt to match")
	}
	if r.MatchString("hello.jpg") {
		t.Fatalf("expected hello.jpg not to match")
	}

	r2, err := globToRegex("public/*")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !r2.MatchString("public/file.txt") {
		t.Fatalf("expected public/file.txt to match")
	}
}

func TestAuthenticateAndPermissions(t *testing.T) {
	resetGlobals()
	// Admin has rw on everything
	authRules = []AuthRule{{Username: "admin", Password: "apass", Permission: "rw", Pattern: nil}}
	// Reader has only r on *.txt
	rx, _ := globToRegex("*.txt")
	authRules = append(authRules, AuthRule{Username: "reader", Password: "rpass", Permission: "r", Pattern: rx})

	user, ok := authenticate("admin", "apass")
	if !ok || user == nil {
		t.Fatalf("admin should authenticate")
	}
	if !hasReadPermission(user, "foo.jpg") || !hasWritePermission(user, "foo.jpg") {
		t.Fatalf("admin should have both read and write access")
	}

	reader, ok := authenticate("reader", "rpass")
	if !ok || reader == nil {
		t.Fatalf("reader should authenticate")
	}
	if !hasReadPermission(reader, "notes.txt") {
		t.Fatalf("reader should have read access to notes.txt")
	}
	if hasWritePermission(reader, "notes.txt") {
		t.Fatalf("reader should NOT have write access to notes.txt")
	}
	if hasReadPermission(reader, "notes.jpg") {
		t.Fatalf("reader should NOT have read access to notes.jpg")
	}
}

func TestParseCustomMIMETypes(t *testing.T) {
	resetGlobals()
	parseCustomMIMETypes("mhtml,shtml:text/html,v;archive:application/x-archive")
	mt, view := getMIMEType("index.mhtml")
	if mt != "text/html" || view != true {
		t.Fatalf("expected text/html viewable, got %s (%v)", mt, view)
	}
	mt2, view2 := getMIMEType("file.archive")
	if mt2 != "application/x-archive" || view2 != false {
		t.Fatalf("expected application/x-archive not viewable, got %s (%v)", mt2, view2)
	}
}

func TestParseRange(t *testing.T) {
	// Valid ranges
	ranges, err := parseRange("bytes=0-499", 1000)
	if err != nil || len(ranges) != 1 || ranges[0].start != 0 || ranges[0].end != 499 {
		t.Fatalf("unexpected range parse result: %+v, err=%v", ranges, err)
	}

	ranges, err = parseRange("bytes=-500", 1000)
	if err != nil || len(ranges) != 1 || ranges[0].start != 500 || ranges[0].end != 999 {
		t.Fatalf("unexpected suffix range parse result: %+v, err=%v", ranges, err)
	}

	ranges, err = parseRange("bytes=500-", 1000)
	if err != nil || len(ranges) != 1 || ranges[0].start != 500 || ranges[0].end != 999 {
		t.Fatalf("unexpected open ended range parse result: %+v, err=%v", ranges, err)
	}

	ranges, err = parseRange("bytes=0-0,500-999", 1000)
	if err != nil || len(ranges) != 2 {
		t.Fatalf("expected two ranges, got %+v, err=%v", ranges, err)
	}

	if _, err := parseRange("bytes=1000-2000", 1000); err == nil {
		t.Fatalf("expected invalid range error")
	}
}

func TestAuthMiddleware(t *testing.T) {
	resetGlobals()
	// set auth enabled and a rule
	authEnabled = true
	authRules = []AuthRule{{Username: "xuser", Password: "xpass", Permission: "rw", Pattern: nil}}

	h := authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})

	req := httptest.NewRequest("GET", "http://example.com/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when no credentials provided, got %d", rr.Code)
	}

	req = httptest.NewRequest("GET", "http://example.com/", nil)
	req.SetBasicAuth("xuser", "wrong")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 when wrong credentials provided, got %d", rr.Code)
	}

	req = httptest.NewRequest("GET", "http://example.com/", nil)
	req.SetBasicAuth("xuser", "xpass")
	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("expected 200 when correct credentials provided, got %d", rr.Code)
	}
}
