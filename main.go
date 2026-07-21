package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"embed"
	"encoding/hex"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"mime/multipart"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

//go:embed templates/*
var templateFS embed.FS

var templates *template.Template

var (
	addr               string
	workingDir         string
	intelligentMIME    bool
	verbose            bool
	customMIMETypes    map[string]string
	customMIMEViewable map[string]bool
	authRules          []AuthRule
	authEnabled        bool

	sessions   = make(map[string]*UserContext)
	sessionsMu sync.RWMutex
)

const sessionCookieName = "files_session"

// AuthRule represents an authentication and authorization rule
type AuthRule struct {
	Username   string
	Password   string
	Permission string // "r" (read), "w" (write), "rw" (read+write), or "" (any username with password)
	Pattern    *regexp.Regexp
}

// UserContext stores authenticated user information
type UserContext struct {
	Username string
	Rules    []AuthRule
}

type FileInfo struct {
	Name    string
	Path    string
	Size    int64
	ModTime time.Time
	IsDir   bool
}

type PageData struct {
	CurrentPath string
	ParentPath  string
	Files       []FileInfo
	Error       string
	Token       string
}

func init() {
	var err error
	funcMap := template.FuncMap{
		"formatSize": formatSize,
		"formatDate": formatDate,
		"splitPath":  splitPath,
		"joinPath":   joinPath,
	}
	templates, err = template.New("").Funcs(funcMap).ParseFS(templateFS, "templates/*.html")
	if err != nil {
		log.Fatal("Failed to parse templates:", err)
	}
}

// formatSize formats file size in human-readable format
func formatSize(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	div, exp := int64(unit), 0
	for n := size / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(size)/float64(div), "KMGTPE"[exp])
}

// formatDate formats time in human-readable format
func formatDate(t time.Time) string {
	return t.Format("2006-01-02 15:04:05")
}

// splitPath splits a path into components
func splitPath(path string) []string {
	return strings.Split(filepath.Clean(path), string(filepath.Separator))
}

// joinPath joins path components
func joinPath(parts ...string) string {
	return filepath.Join(parts...)
}

// authFlags is a custom flag type to collect multiple -auth flags
type authFlags []string

func (a *authFlags) String() string {
	return strings.Join(*a, ",")
}

func (a *authFlags) Set(value string) error {
	*a = append(*a, value)
	return nil
}

func getLocalIPs() []string {
	var ips []string
	ifaces, err := net.Interfaces()
	if err != nil {
		return ips
	}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, err := iface.Addrs()
		if err != nil {
			continue
		}
		for _, addr := range addrs {
			var ip net.IP
			switch v := addr.(type) {
			case *net.IPNet:
				ip = v.IP
			case *net.IPAddr:
				ip = v.IP
			}
			if ip == nil || ip.IsLoopback() {
				continue
			}
			if ip4 := ip.To4(); ip4 != nil {
				ips = append(ips, ip4.String())
			}
		}
	}
	return ips
}

func logVerbose(format string, v ...interface{}) {
	if verbose {
		log.Printf("[verbose] "+format, v...)
	}
}

func main() {
	// Parse command-line flags
	hostFlag := flag.String("host", "0.0.0.0", "Address to listen on")
	portFlag := flag.String("port", "8080", "Port to listen on")
	dirFlag := flag.String("dir", "", "Working directory to serve files from (default: current directory)")
	intelligentMIMEFlag := flag.String("i", "", "Enable intelligent MIME recognition. Use 'true' for defaults, or specify custom mappings like 'ext1,ext2:mime/type;ext3:mime/type2,v' (,v indicates viewable)")

	verboseFlag := flag.Bool("verbose", false, "Enable verbose logging for debugging")

	// Define auth flag to collect multiple -auth flags
	var authFlagValues authFlags
	flag.Var(&authFlagValues, "auth", "Authentication rules. Can be: password, username:password, or username:password:permission:pattern. Can be specified multiple times or comma-separated.")

	flag.Parse()

	verbose = *verboseFlag

	// Initialize custom MIME types map
	customMIMETypes = make(map[string]string)
	customMIMEViewable = make(map[string]bool)

	// Process the -i flag
	if *intelligentMIMEFlag != "" {
		intelligentMIME = true
		if *intelligentMIMEFlag != "true" {
			// Parse custom MIME type mappings
			parseCustomMIMETypes(*intelligentMIMEFlag)
		}
	}

	// Set address
	addr = fmt.Sprintf("%s:%s", *hostFlag, strings.TrimPrefix(*portFlag, ":"))

	// Set working directory
	var err error
	if *dirFlag != "" {
		workingDir, err = filepath.Abs(*dirFlag)
		if err != nil {
			log.Fatal("Failed to resolve directory path:", err)
		}
		// Check if directory exists
		if info, err := os.Stat(workingDir); err != nil {
			log.Fatal("Directory does not exist:", err)
		} else if !info.IsDir() {
			log.Fatal("Path is not a directory:", workingDir)
		}
	} else {
		workingDir, err = os.Getwd()
		if err != nil {
			log.Fatal("Failed to get working directory:", err)
		}
	}

	// Parse authentication rules
	if len(authFlagValues) > 0 {
		authEnabled = true
		if err := parseAuthRules(authFlagValues); err != nil {
			log.Fatal("Failed to parse authentication rules:", err)
		}
		log.Printf("Authentication enabled with %d rule(s)", len(authRules))
	}

	http.HandleFunc("/", authMiddleware(logRequestMiddleware(browseHandler)))
	http.HandleFunc("/download/", authMiddleware(logRequestMiddleware(downloadHandler)))
	http.HandleFunc("/upload", authMiddleware(logRequestMiddleware(uploadHandler)))

	port := strings.TrimPrefix(*portFlag, ":")
	ips := getLocalIPs()
	if len(ips) > 0 {
		log.Printf("Server starting on :%s (available at %s)", port, strings.Join(ips, ", "))
	} else {
		log.Printf("Server starting on :%s", port)
	}
	if verbose {
		log.Printf("Verbose logging enabled")
	}
	log.Printf("Serving files from: %s", workingDir)
	if intelligentMIME {
		log.Printf("Intelligent MIME recognition enabled")
	}
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("Server failed:", err)
	}
}

// responseLogger wraps http.ResponseWriter to capture the status code
type responseLogger struct {
	http.ResponseWriter
	statusCode int
}

func (rl *responseLogger) WriteHeader(code int) {
	rl.statusCode = code
	rl.ResponseWriter.WriteHeader(code)
}

// logRequestMiddleware wraps a handler to log HTTP requests
func logRequestMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("[%s] %s %s", r.Method, r.URL.Path, r.RemoteAddr)
		if verbose {
			logVerbose("  Host: %s", r.Host)
			logVerbose("  User-Agent: %s", r.UserAgent())
			logVerbose("  Referer: %s", r.Referer())
			for name, values := range r.Header {
				for _, value := range values {
					logVerbose("  %s: %s", name, value)
				}
			}
		}
		rl := &responseLogger{ResponseWriter: w, statusCode: http.StatusOK}
		next(rl, r)
		log.Printf("[%s] %s %d completed in %v", r.Method, r.URL.Path, rl.statusCode, time.Since(start))
	}
}

// parseAuthRules parses authentication rules from command-line flags
func parseAuthRules(authFlagValues []string) error {
	for _, flagValue := range authFlagValues {
		// Split by comma to handle multiple rules in one flag
		rules := strings.Split(flagValue, ",")
		for _, rule := range rules {
			rule = strings.TrimSpace(rule)
			if rule == "" {
				continue
			}

			parts := strings.Split(rule, ":")
			switch len(parts) {
			case 1:
				// Format: password (any username)
				authRules = append(authRules, AuthRule{
					Username:   "",
					Password:   parts[0],
					Permission: "rw",
					Pattern:    nil,
				})
				log.Printf("Added auth rule: password-only (any username)")

			case 2:
				// Format: username:password
				authRules = append(authRules, AuthRule{
					Username:   parts[0],
					Password:   parts[1],
					Permission: "rw",
					Pattern:    nil,
				})
				log.Printf("Added auth rule: %s (full access)", parts[0])

			case 4:
				// Format: username:password:permission:pattern
				perm := strings.ToLower(parts[2])
				if perm != "r" && perm != "w" && perm != "rw" {
					return fmt.Errorf("invalid permission '%s' in rule '%s' (must be r, w, or rw)", parts[2], rule)
				}

				// Compile the glob pattern to regex
				pattern := parts[3]
				regex, err := globToRegex(pattern)
				if err != nil {
					return fmt.Errorf("invalid pattern '%s' in rule '%s': %v", parts[3], rule, err)
				}

				authRules = append(authRules, AuthRule{
					Username:   parts[0],
					Password:   parts[1],
					Permission: perm,
					Pattern:    regex,
				})
				log.Printf("Added auth rule: %s (permission: %s, pattern: %s)", parts[0], perm, pattern)

			default:
				return fmt.Errorf("invalid auth rule format: '%s' (expected password, username:password, or username:password:permission:pattern)", rule)
			}
		}
	}

	if len(authRules) == 0 {
		return fmt.Errorf("no valid auth rules found")
	}

	return nil
}

// globToRegex converts a glob pattern to a regular expression
func globToRegex(pattern string) (*regexp.Regexp, error) {
	// Escape special regex characters except * and ?
	regexPattern := regexp.QuoteMeta(pattern)
	// Replace escaped glob wildcards with regex equivalents
	regexPattern = strings.ReplaceAll(regexPattern, "\\*", ".*")
	regexPattern = strings.ReplaceAll(regexPattern, "\\?", ".")
	// Anchor the pattern to match the entire path
	regexPattern = "^" + regexPattern + "$"
	return regexp.Compile(regexPattern)
}

// authMiddleware handles HTTP Basic Authentication
//
// Authentication order:
//  1. session cookie  – set after a prior successful Basic Auth login
//  2. Authorization header – standard HTTP Basic Auth (also sets the cookie)
func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !authEnabled {
			logVerbose("auth: disabled, skipping")
			next(w, r)
			return
		}

		logVerbose("auth: enabled, checking credentials")

		// 1. Try session cookie.
		cookieToken := ""
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			cookieToken = cookie.Value
		}

		// Also check ?token= query parameter (embedded in download URLs).
		if cookieToken == "" {
			cookieToken = r.URL.Query().Get("token")
		}

		if cookieToken != "" {
			sessionsMu.RLock()
			userCtx, ok := sessions[cookieToken]
			sessionsMu.RUnlock()
			if ok {
				logVerbose("auth: valid session token for user '%s'", userCtx.Username)
				ctx := setUserContext(r.Context(), userCtx)
				r = r.WithContext(ctx)
				next(w, r)
				return
			}
			logVerbose("auth: stale session token")
		}

		// 2. Try Basic Auth.
		username, password, ok := r.BasicAuth()
		if !ok {
			logVerbose("auth: no credentials, requesting auth")
			requestAuth(w, "Authorization required")
			return
		}

		logVerbose("auth: basic auth for user '%s'", username)

		userCtx, authenticated := authenticate(username, password)
		if !authenticated {
			logVerbose("auth: authentication failed for '%s'", username)
			requestAuth(w, "Invalid credentials")
			return
		}

		logVerbose("auth: user '%s' authenticated, setting session cookie", username)

		// Issue a session cookie so subsequent requests (including from
		// download managers that share the cookie jar) are authenticated.
		token := generateSessionToken()
		sessionsMu.Lock()
		sessions[token] = userCtx
		sessionsMu.Unlock()
		http.SetCookie(w, &http.Cookie{
			Name:     sessionCookieName,
			Value:    token,
			Path:     "/",
			HttpOnly: true,
		})

		ctx := setUserContext(r.Context(), userCtx)
		r = r.WithContext(ctx)

		next(w, r)
	}
}

func generateSessionToken() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		log.Printf("WARNING: failed to generate session token: %v", err)
		return ""
	}
	return hex.EncodeToString(b)
}

// authenticate validates credentials and returns user context
func authenticate(username, password string) (*UserContext, bool) {
	var matchedRules []AuthRule

	for _, rule := range authRules {
		// Check if username matches (empty username in rule means any username)
		usernameMatches := rule.Username == "" || rule.Username == username

		// Use constant-time comparison for password
		passwordMatches := subtle.ConstantTimeCompare([]byte(rule.Password), []byte(password)) == 1

		if usernameMatches && passwordMatches {
			matchedRules = append(matchedRules, rule)
		}
	}

	if len(matchedRules) == 0 {
		return nil, false
	}

	return &UserContext{
		Username: username,
		Rules:    matchedRules,
	}, true
}

// requestAuth sends a 401 Unauthorized response with WWW-Authenticate header
func requestAuth(w http.ResponseWriter, message string) {
	w.Header().Set("WWW-Authenticate", `Basic realm="File Server"`)
	http.Error(w, message, http.StatusUnauthorized)
}

// Context key type for user context
type contextKey string

const userContextKey contextKey = "user"

// setUserContext stores user context in the request context
func setUserContext(ctx context.Context, user *UserContext) context.Context {
	return context.WithValue(ctx, userContextKey, user)
}

// getUserContext retrieves user context from the request context
func getUserContext(r *http.Request) *UserContext {
	if user, ok := r.Context().Value(userContextKey).(*UserContext); ok {
		return user
	}
	return nil
}

// hasReadPermission checks if the user has read permission for a path
func hasReadPermission(user *UserContext, path string) bool {
	if user == nil {
		return !authEnabled
	}

	for _, rule := range user.Rules {
		// Check if permission includes read
		if !strings.Contains(rule.Permission, "r") {
			continue
		}

		// If no pattern is specified, allow access
		if rule.Pattern == nil {
			return true
		}

		// Check if path matches the pattern
		if rule.Pattern.MatchString(path) {
			return true
		}
	}

	return false
}

// hasWritePermission checks if the user has write permission for a path
func hasWritePermission(user *UserContext, path string) bool {
	if user == nil {
		return !authEnabled
	}

	for _, rule := range user.Rules {
		// Check if permission includes write
		if !strings.Contains(rule.Permission, "w") {
			continue
		}

		// If no pattern is specified, allow access
		if rule.Pattern == nil {
			return true
		}

		// Check if path matches the pattern
		if rule.Pattern.MatchString(path) {
			return true
		}
	}

	return false
}

// browseHandler handles file browsing requests
func browseHandler(w http.ResponseWriter, r *http.Request) {
	logVerbose("browse: method=%s path=%s", r.Method, r.URL.Path)

	if r.Method != http.MethodGet {
		logVerbose("browse: method not allowed: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the requested path (relative to workingDir)
	requestedPath := strings.TrimPrefix(r.URL.Path, "/")
	logVerbose("browse: requestedPath=%s", requestedPath)

	// Check read permission
	user := getUserContext(r)
	if !hasReadPermission(user, requestedPath) {
		logVerbose("browse: read permission denied for path=%s", requestedPath)
		http.Error(w, "Access denied: insufficient permissions for this path", http.StatusForbidden)
		return
	}

	fullPath := filepath.Join(workingDir, requestedPath)
	logVerbose("browse: fullPath=%s", fullPath)

	// Security check: ensure the path is within workingDir
	cleanPath, err := filepath.Abs(fullPath)
	if err != nil {
		logVerbose("browse: invalid path: %v", err)
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	cleanWorkingDir, _ := filepath.Abs(workingDir)
	if !strings.HasPrefix(cleanPath, cleanWorkingDir) {
		logVerbose("browse: path traversal attempt: %s not within %s", cleanPath, cleanWorkingDir)
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	// Check if path exists
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			logVerbose("browse: path not found: %s", fullPath)
			http.Error(w, "Path not found", http.StatusNotFound)
			return
		}
		logVerbose("browse: error accessing path: %v", err)
		http.Error(w, "Error accessing path", http.StatusInternalServerError)
		return
	}

	// If it's a file, redirect to download
	if !info.IsDir() {
		target := "/download/" + requestedPath
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}
		logVerbose("browse: redirecting file to %s", target)
		http.Redirect(w, r, target, http.StatusFound)
		return
	}

	// List directory contents
	entries, err := os.ReadDir(fullPath)
	if err != nil {
		http.Error(w, "Error reading directory", http.StatusInternalServerError)
		return
	}

	var files []FileInfo
	for _, entry := range entries {
		entryInfo, err := entry.Info()
		if err != nil {
			continue
		}

		files = append(files, FileInfo{
			Name:    entry.Name(),
			Path:    filepath.Join(requestedPath, entry.Name()),
			Size:    entryInfo.Size(),
			ModTime: entryInfo.ModTime(),
			IsDir:   entry.IsDir(),
		})
	}

	// Calculate parent path
	parentPath := ""
	if requestedPath != "" {
		parentPath = filepath.Dir(requestedPath)
		if parentPath == "." {
			parentPath = ""
		}
	}

	data := PageData{
		CurrentPath: requestedPath,
		ParentPath:  parentPath,
		Files:       files,
	}

	if authEnabled {
		if cookie, err := r.Cookie(sessionCookieName); err == nil {
			data.Token = cookie.Value
		}
	}

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "browse.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Error rendering page", http.StatusInternalServerError)
	}
}

// downloadHandler handles file downloads with resume support (Range requests)
func downloadHandler(w http.ResponseWriter, r *http.Request) {
	logVerbose("download: method=%s path=%s", r.Method, r.URL.Path)

	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		logVerbose("download: method not allowed: %s", r.Method)
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the requested file path
	requestedPath := strings.TrimPrefix(r.URL.Path, "/download/")
	logVerbose("download: requestedPath=%s", requestedPath)

	// Check read permission
	user := getUserContext(r)
	if !hasReadPermission(user, requestedPath) {
		logVerbose("download: read permission denied for path=%s", requestedPath)
		http.Error(w, "Access denied: insufficient permissions for this path", http.StatusForbidden)
		return
	}
	logVerbose("download: read permission granted")

	fullPath := filepath.Join(workingDir, requestedPath)
	logVerbose("download: fullPath=%s", fullPath)

	// Security check: ensure the path is within workingDir
	cleanPath, err := filepath.Abs(fullPath)
	if err != nil {
		logVerbose("download: invalid path: %v", err)
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	cleanWorkingDir, _ := filepath.Abs(workingDir)
	if !strings.HasPrefix(cleanPath, cleanWorkingDir) {
		logVerbose("download: path traversal attempt: %s not within %s", cleanPath, cleanWorkingDir)
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}
	logVerbose("download: path security check passed")

	// Open the file
	file, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			logVerbose("download: file not found: %s", fullPath)
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		logVerbose("download: error opening file: %v", err)
		http.Error(w, "Error opening file", http.StatusInternalServerError)
		return
	}
	defer file.Close()
	logVerbose("download: file opened successfully")

	// Get file info
	fileInfo, err := file.Stat()
	if err != nil {
		logVerbose("download: error statting file: %v", err)
		http.Error(w, "Error getting file info", http.StatusInternalServerError)
		return
	}

	// Don't allow downloading directories
	if fileInfo.IsDir() {
		logVerbose("download: attempt to download directory: %s", fullPath)
		http.Error(w, "Cannot download directory", http.StatusBadRequest)
		return
	}

	fileSize := fileInfo.Size()
	fileName := filepath.Base(fullPath)
	logVerbose("download: fileName=%s size=%d", fileName, fileSize)

	// Determine content type and disposition
	contentType, isViewable := getMIMEType(fullPath)
	disposition := "attachment"
	if intelligentMIME && isViewable {
		disposition = "inline"
	}
	logVerbose("download: contentType=%s disposition=%s isViewable=%v intelligentMIME=%v",
		contentType, disposition, isViewable, intelligentMIME)

	// Set headers for file download
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, fileName))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentType)

	// Handle range requests for resume support
	rangeHeader := r.Header.Get("Range")
	logVerbose("download: Range header: %q", rangeHeader)

	if rangeHeader == "" {
		// No range requested, send entire file
		logVerbose("download: no range requested, serving full file (%d bytes)", fileSize)
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			written, err := io.Copy(w, file)
			if err != nil {
				logVerbose("download: io.Copy error after %d bytes: %v", written, err)
			}
			logVerbose("download: sent %d bytes (200 OK)", written)
		}
		return
	}

	// Parse range header
	ranges, err := parseRange(rangeHeader, fileSize)
	if err != nil || len(ranges) != 1 {
		logVerbose("download: invalid range: %q (err=%v ranges=%d)", rangeHeader, err, len(ranges))
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	start := ranges[0].start
	end := ranges[0].end
	logVerbose("download: parsed range: bytes=%d-%d/%d", start, end, fileSize)

	// If the range covers the entire file, serve as a normal 200 response
	if start == 0 && end == fileSize-1 {
		logVerbose("download: range covers entire file, serving as 200 OK")
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			written, err := io.Copy(w, file)
			if err != nil {
				logVerbose("download: io.Copy error after %d bytes: %v", written, err)
			}
			logVerbose("download: sent %d bytes (200 OK from full range)", written)
		}
		return
	}

	contentLength := end - start + 1
	logVerbose("download: partial content: %d bytes (%d-%d)", contentLength, start, end)

	// Seek to start position
	if _, err := file.Seek(start, 0); err != nil {
		logVerbose("download: seek error: %v", err)
		http.Error(w, "Error seeking file", http.StatusInternalServerError)
		return
	}

	// Set headers for partial content
	w.Header().Set("Content-Range", fmt.Sprintf("bytes %d-%d/%d", start, end, fileSize))
	w.Header().Set("Content-Length", strconv.FormatInt(contentLength, 10))
	w.WriteHeader(http.StatusPartialContent)

	// Send the requested range
	if r.Method != http.MethodHead {
		io.CopyN(w, file, contentLength)
		logVerbose("download: sent %d bytes (206 Partial Content)", contentLength)
	}
}

// uploadHandler handles file uploads
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		if err := templates.ExecuteTemplate(w, "upload.html", nil); err != nil {
			log.Printf("Template error: %v", err)
			http.Error(w, "Error rendering page", http.StatusInternalServerError)
		}
		return
	}

	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	user := getUserContext(r)

	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, "Error parsing form: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Resolve target directory (optional subdirectory)
	subDir := r.FormValue("directory")
	targetDir := workingDir
	uploadPath := ""
	if subDir != "" {
		subDir = filepath.Clean(subDir)
		targetDir = filepath.Join(workingDir, subDir)
		uploadPath = subDir

		cleanTargetDir, err := filepath.Abs(targetDir)
		if err != nil {
			http.Error(w, "Invalid directory path", http.StatusBadRequest)
			return
		}
		cleanWorkingDir, _ := filepath.Abs(workingDir)
		if !strings.HasPrefix(cleanTargetDir, cleanWorkingDir) {
			http.Error(w, "Access denied", http.StatusForbidden)
			return
		}

		if err := os.MkdirAll(targetDir, 0755); err != nil {
			http.Error(w, "Error creating directory: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Collect all uploaded files (supports single "file" and batch "files")
	var fileHeaders []*multipart.FileHeader
	if fhs := r.MultipartForm.File["files"]; len(fhs) > 0 {
		fileHeaders = fhs
	}
	if fhs := r.MultipartForm.File["file"]; len(fhs) > 0 {
		fileHeaders = append(fileHeaders, fhs...)
	}

	if len(fileHeaders) == 0 {
		http.Error(w, "No files provided", http.StatusBadRequest)
		return
	}

	var success, failed int
	for _, header := range fileHeaders {
		fileName := filepath.Base(header.Filename)
		filePath := filepath.Join(uploadPath, fileName)
		dstPath := filepath.Join(targetDir, fileName)

		logVerbose("upload: saving %s -> %s", header.Filename, dstPath)

		if !hasWritePermission(user, filePath) {
			logVerbose("upload: write permission denied for %s", filePath)
			failed++
			continue
		}

		file, err := header.Open()
		if err != nil {
			logVerbose("upload: failed to open %s: %v", header.Filename, err)
			failed++
			continue
		}

		dst, err := os.Create(dstPath)
		if err != nil {
			file.Close()
			logVerbose("upload: failed to create %s: %v", dstPath, err)
			failed++
			continue
		}

		if _, err := io.Copy(dst, file); err != nil {
			dst.Close()
			file.Close()
			logVerbose("upload: failed to save %s: %v", header.Filename, err)
			failed++
			continue
		}

		dst.Close()
		file.Close()
		success++
	}

	redirectPath := "/"
	if subDir != "" {
		redirectPath = "/" + subDir
	}
	http.Redirect(w, r, fmt.Sprintf("%s?upload=ok&success=%d&failed=%d", redirectPath, success, failed), http.StatusSeeOther)
}

// byteRange represents a byte range request
type byteRange struct {
	start int64
	end   int64
}

// parseCustomMIMETypes parses custom MIME type mappings from a string
// Format: "ext1,ext2:mime/type;ext3:mime/type2,v;ext4:mime/type3"
// Multiple extensions can be mapped to the same MIME type by comma-separating them
// Optional ",v" suffix after MIME type indicates the type is viewable in browser (default: false)
func parseCustomMIMETypes(input string) {
	// Split by semicolon to get each mapping group
	mappings := strings.Split(input, ";")

	for _, mapping := range mappings {
		mapping = strings.TrimSpace(mapping)
		if mapping == "" {
			continue
		}

		// Split by colon to separate extensions from MIME type (and optional viewability flag)
		parts := strings.Split(mapping, ":")
		if len(parts) != 2 {
			log.Printf("Invalid MIME mapping format: %s (expected 'ext:mime/type' or 'ext:mime/type,v')", mapping)
			continue
		}

		extensions := strings.TrimSpace(parts[0])
		mimeInfo := strings.TrimSpace(parts[1])

		if extensions == "" || mimeInfo == "" {
			log.Printf("Empty extension or MIME type in mapping: %s", mapping)
			continue
		}

		// Check if the mime info has the ,v suffix to indicate viewable
		isViewable := false
		if strings.HasSuffix(mimeInfo, ",v") {
			isViewable = true
			mimeInfo = strings.TrimSuffix(mimeInfo, ",v")
			mimeInfo = strings.TrimSpace(mimeInfo)
		}

		if mimeInfo == "" {
			log.Printf("Empty MIME type after removing suffix: %s", mapping)
			continue
		}

		// Split by comma to handle multiple extensions with the same MIME type
		extList := strings.Split(extensions, ",")
		for _, ext := range extList {
			ext = strings.TrimSpace(ext)
			if ext == "" {
				continue
			}

			// Normalize extension to start with dot
			if !strings.HasPrefix(ext, ".") {
				ext = "." + ext
			}
			ext = strings.ToLower(ext)

			customMIMETypes[ext] = mimeInfo
			customMIMEViewable[ext] = isViewable
			viewStr := "not viewable"
			if isViewable {
				viewStr = "viewable"
			}
			log.Printf("Registered custom MIME type: %s -> %s (%s)", ext, mimeInfo, viewStr)
		}
	}
}

// getMIMEType returns the MIME type for a file based on its extension
// Returns (mimeType, isViewable) where isViewable indicates if it's a browser-viewable multimedia type
func getMIMEType(filePath string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(filePath))

	// Check custom MIME types first
	if customMime, exists := customMIMETypes[ext]; exists {
		isViewable := customMIMEViewable[ext]
		return customMime, isViewable
	}

	// Image types
	imageTypes := map[string]bool{
		".jpg":  true,
		".jpeg": true,
		".png":  true,
		".gif":  true,
		".bmp":  true,
		".webp": true,
		".svg":  true,
		".ico":  true,
	}

	// Audio types
	audioTypes := map[string]bool{
		".mp3":  true,
		".wav":  true,
		".flac": true,
		".aac":  true,
		".ogg":  true,
		".m4a":  true,
		".weba": true,
	}

	// Video types
	videoTypes := map[string]bool{
		".mp4":  true,
		".webm": true,
		".ogv":  true,
		".mov":  true,
		".mkv":  true,
		".avi":  true,
		".flv":  true,
		".m3u8": true,
	}

	// Text/document/application types that browsers can display
	documentTypes := map[string]bool{
		".html": true,
		".htm":  true,
		".txt":  true,
		".pdf":  true,
		".xml":  true,
	}

	// Application types that are always downloaded
	applicationTypes := map[string]string{
		".apk": "application/vnd.android.package-archive",
	}

	// Check image types
	if imageTypes[ext] {
		switch ext {
		case ".jpg", ".jpeg":
			return "image/jpeg", true
		case ".png":
			return "image/png", true
		case ".gif":
			return "image/gif", true
		case ".bmp":
			return "image/bmp", true
		case ".webp":
			return "image/webp", true
		case ".svg":
			return "image/svg+xml", true
		case ".ico":
			return "image/x-icon", true
		}
	}

	// Check audio types
	if audioTypes[ext] {
		switch ext {
		case ".mp3":
			return "audio/mpeg", true
		case ".wav":
			return "audio/wav", true
		case ".flac":
			return "audio/flac", true
		case ".aac":
			return "audio/aac", true
		case ".ogg":
			return "audio/ogg", true
		case ".m4a":
			return "audio/mp4", true
		case ".weba":
			return "audio/webp", true
		}
	}

	// Check video types
	if videoTypes[ext] {
		switch ext {
		case ".mp4":
			return "video/mp4", true
		case ".webm":
			return "video/webm", true
		case ".ogv":
			return "video/ogg", true
		case ".mov":
			return "video/quicktime", true
		case ".mkv":
			return "video/x-matroska", true
		case ".avi":
			return "video/x-msvideo", true
		case ".flv":
			return "video/x-flv", true
		case ".m3u8":
			return "application/vnd.apple.mpegurl", true
		}
	}

	// Check document types
	if documentTypes[ext] {
		switch ext {
		case ".html", ".htm":
			return "text/html", true
		case ".txt":
			return "text/plain", true
		case ".pdf":
			return "application/pdf", true
		case ".xml":
			return "application/xml", true
		}
	}

	// Check application types (always downloaded, not viewable)
	if mime, ok := applicationTypes[ext]; ok {
		return mime, false
	}

	return "application/octet-stream", false
}

// parseRange parses a Range header value
func parseRange(s string, size int64) ([]byteRange, error) {
	if !strings.HasPrefix(s, "bytes=") {
		return nil, fmt.Errorf("invalid range header")
	}

	s = strings.TrimPrefix(s, "bytes=")
	ranges := []byteRange{}

	for _, rangeSpec := range strings.Split(s, ",") {
		rangeSpec = strings.TrimSpace(rangeSpec)
		parts := strings.Split(rangeSpec, "-")
		if len(parts) != 2 {
			return nil, fmt.Errorf("invalid range spec")
		}

		var start, end int64
		var err error

		if parts[0] == "" {
			// Suffix range: -500 means last 500 bytes
			end = size - 1
			start, err = strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return nil, err
			}
			start = size - start
			if start < 0 {
				start = 0
			}
		} else if parts[1] == "" {
			// Start range: 500- means from byte 500 to end
			start, err = strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				return nil, err
			}
			end = size - 1
		} else {
			// Full range: 500-999
			start, err = strconv.ParseInt(parts[0], 10, 64)
			if err != nil {
				return nil, err
			}
			end, err = strconv.ParseInt(parts[1], 10, 64)
			if err != nil {
				return nil, err
			}
		}

		if start < 0 || start >= size || end < start || end >= size {
			return nil, fmt.Errorf("invalid range")
		}

		ranges = append(ranges, byteRange{start: start, end: end})
	}

	return ranges, nil
}
