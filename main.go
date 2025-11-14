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
	addr       string
	workingDir string
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
	flag.Parse()

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

	http.HandleFunc("/", browseHandler)
	http.HandleFunc("/download/", downloadHandler)
	http.HandleFunc("/upload", uploadHandler)

	log.Printf("Server starting on http://%s", addr)
	log.Printf("Serving files from: %s", workingDir)
	if err := http.ListenAndServe(addr, nil); err != nil {
		log.Fatal("Server failed:", err)
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

	// Set headers for file download
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s"`, fileName))
	w.Header().Set("Accept-Ranges", "bytes")
	w.Header().Set("Content-Type", "application/octet-stream")

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
