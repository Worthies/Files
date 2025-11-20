package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

//go:embed templates/*
var templateFS embed.FS

var templates *template.Template

var (
	addr               string
	workingDir         string
	intelligentMIME    bool
	customMIMETypes    map[string]string
	customMIMEViewable map[string]bool
)

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

func main() {
	// Parse command-line flags
	hostFlag := flag.String("host", "0.0.0.0", "Address to listen on")
	portFlag := flag.String("port", "8080", "Port to listen on")
	dirFlag := flag.String("dir", "", "Working directory to serve files from (default: current directory)")
	intelligentMIMEFlag := flag.String("i", "", "Enable intelligent MIME recognition. Use 'true' for defaults, or specify custom mappings like 'ext1,ext2:mime/type;ext3:mime/type2,v' (,v indicates viewable)")
	flag.Parse()

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

	http.HandleFunc("/", logRequestMiddleware(browseHandler))
	http.HandleFunc("/download/", logRequestMiddleware(downloadHandler))
	http.HandleFunc("/upload", logRequestMiddleware(uploadHandler))

	log.Printf("Server starting on http://%s", addr)
	log.Printf("Serving files from: %s", workingDir)
	if intelligentMIME {
		log.Printf("Intelligent MIME recognition enabled")
	}
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("Server failed:", err)
	}
}

// logRequestMiddleware wraps a handler to log HTTP requests
func logRequestMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		log.Printf("[%s] %s %s", r.Method, r.URL.Path, r.RemoteAddr)
		next(w, r)
		log.Printf("[%s] %s completed in %v", r.Method, r.URL.Path, time.Since(start))
	}
}

// browseHandler handles file browsing requests
func browseHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the requested path (relative to workingDir)
	requestedPath := strings.TrimPrefix(r.URL.Path, "/")
	fullPath := filepath.Join(workingDir, requestedPath)

	// Security check: ensure the path is within workingDir
	cleanPath, err := filepath.Abs(fullPath)
	if err != nil {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	cleanWorkingDir, _ := filepath.Abs(workingDir)
	if !strings.HasPrefix(cleanPath, cleanWorkingDir) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	// Check if path exists
	info, err := os.Stat(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "Path not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error accessing path", http.StatusInternalServerError)
		return
	}

	// If it's a file, redirect to download
	if !info.IsDir() {
		http.Redirect(w, r, "/download/"+requestedPath, http.StatusFound)
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

	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := templates.ExecuteTemplate(w, "browse.html", data); err != nil {
		log.Printf("Template error: %v", err)
		http.Error(w, "Error rendering page", http.StatusInternalServerError)
	}
}

// downloadHandler handles file downloads with resume support (Range requests)
func downloadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Get the requested file path
	requestedPath := strings.TrimPrefix(r.URL.Path, "/download/")
	fullPath := filepath.Join(workingDir, requestedPath)

	// Security check: ensure the path is within workingDir
	cleanPath, err := filepath.Abs(fullPath)
	if err != nil {
		http.Error(w, "Invalid path", http.StatusBadRequest)
		return
	}
	cleanWorkingDir, _ := filepath.Abs(workingDir)
	if !strings.HasPrefix(cleanPath, cleanWorkingDir) {
		http.Error(w, "Access denied", http.StatusForbidden)
		return
	}

	// Open the file
	file, err := os.Open(fullPath)
	if err != nil {
		if os.IsNotExist(err) {
			http.Error(w, "File not found", http.StatusNotFound)
			return
		}
		http.Error(w, "Error opening file", http.StatusInternalServerError)
		return
	}
	defer file.Close()

	// Get file info
	fileInfo, err := file.Stat()
	if err != nil {
		http.Error(w, "Error getting file info", http.StatusInternalServerError)
		return
	}

	// Don't allow downloading directories
	if fileInfo.IsDir() {
		http.Error(w, "Cannot download directory", http.StatusBadRequest)
		return
	}

	fileSize := fileInfo.Size()
	fileName := filepath.Base(fullPath)

	// Determine content type and disposition
	contentType := "application/octet-stream"
	disposition := "attachment"

	if intelligentMIME {
		if mimeType, isViewable := getMIMEType(fullPath); isViewable {
			contentType = mimeType
			disposition = "inline"
		}
	}

	// Set headers for file download
	w.Header().Set("Content-Disposition", fmt.Sprintf(`%s; filename="%s"`, disposition, fileName))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", contentType)

	// Handle range requests for resume support
	rangeHeader := r.Header.Get("Range")
	if rangeHeader == "" {
		// No range requested, send entire file
		w.Header().Set("Content-Length", strconv.FormatInt(fileSize, 10))
		w.WriteHeader(http.StatusOK)
		if r.Method != http.MethodHead {
			io.Copy(w, file)
		}
		return
	}

	// Parse range header
	ranges, err := parseRange(rangeHeader, fileSize)
	if err != nil || len(ranges) != 1 {
		w.Header().Set("Content-Range", fmt.Sprintf("bytes */%d", fileSize))
		http.Error(w, "Invalid range", http.StatusRequestedRangeNotSatisfiable)
		return
	}

	start := ranges[0].start
	end := ranges[0].end
	contentLength := end - start + 1

	// Seek to start position
	if _, err := file.Seek(start, 0); err != nil {
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
	}
}

// uploadHandler handles file uploads
func uploadHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodGet {
		// Show upload form
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

	// Parse multipart form (max 100MB in memory)
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		http.Error(w, "Error parsing form: "+err.Error(), http.StatusBadRequest)
		return
	}

	// Get the uploaded file
	file, header, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "Error retrieving file: "+err.Error(), http.StatusBadRequest)
		return
	}
	defer file.Close()

	// Get optional subdirectory
	subDir := r.FormValue("directory")
	targetDir := workingDir
	if subDir != "" {
		// Clean and validate subdirectory path
		subDir = filepath.Clean(subDir)
		targetDir = filepath.Join(workingDir, subDir)

		// Security check
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

		// Create directory if it doesn't exist
		if err := os.MkdirAll(targetDir, 0755); err != nil {
			http.Error(w, "Error creating directory: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}

	// Create destination file
	dstPath := filepath.Join(targetDir, filepath.Base(header.Filename))
	dst, err := os.Create(dstPath)
	if err != nil {
		http.Error(w, "Error creating file: "+err.Error(), http.StatusInternalServerError)
		return
	}
	defer dst.Close()

	// Copy file content
	if _, err := io.Copy(dst, file); err != nil {
		http.Error(w, "Error saving file: "+err.Error(), http.StatusInternalServerError)
		return
	}

	// Redirect back to browse page
	redirectPath := "/"
	if subDir != "" {
		redirectPath = "/" + subDir
	}
	http.Redirect(w, r, redirectPath+"?upload=success", http.StatusSeeOther)
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

	// Text/document types that browsers can display
	documentTypes := map[string]bool{
		".html": true,
		".htm":  true,
		".txt":  true,
		".pdf":  true,
		".xml":  true,
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
