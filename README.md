# File Server

A simple and powerful file server written in Go with support for browsing, uploading, and downloading files with resume capability.

## Features

- 📁 **File Browsing** - Navigate through directories with a clean web interface
- 📤 **File Upload** - Upload files via web form or drag & drop (both on upload page and browse page)
- 📥 **File Download** - Download files with resume support (HTTP Range requests)
- 🔒 **Security** - Path traversal protection to prevent accessing files outside the working directory
- 🎨 **Modern UI** - Clean and responsive interface
- ⚡ **Lightweight** - Single binary with embedded templates

## Installation

```bash
go build -o files main.go
```

## Usage

### Basic Usage

Run the server in the current directory on port 8080:

```bash
./files
```

### Command-Line Options

```bash
./files [options]
```

Options:
- `-host <address>` - Address to listen on (default: 0.0.0.0)
- `-port <port>` - Port to listen on (default: 8080)
- `-dir <directory>` - Working directory to serve files from (default: current directory)

### Examples

Run on custom port:
```bash
./files -port 9000
```

Listen only on localhost:
```bash
./files -host 127.0.0.1
```

Serve files from a specific directory:
```bash
./files -dir /path/to/files
```

Combine options:
```bash
./files -host 192.168.1.100 -port 9000 -dir /path/to/files
```

## Features Details

### File Browsing
- Navigate through directories using the web interface
- View file sizes and modification times
- Breadcrumb navigation for easy path traversal

### File Upload
1. Click "Upload File" button
2. Select a file or drag and drop onto the upload area
3. Optionally specify a subdirectory
4. Upload progress indicator shows transfer status

You can also drag and drop files directly onto the browse page!

### File Download
- Click on any file to download it
- Resume support: Partial downloads can be resumed if interrupted
- Automatic file name preservation

### Security
- Path traversal protection prevents accessing files outside the configured directory
- All paths are validated and sanitized
- No execution of uploaded files

## API Endpoints

- `GET /` - Browse files in the current directory
- `GET /<path>` - Browse files in a specific directory
- `GET /download/<path>` - Download a file (supports HTTP Range requests)
- `GET /upload` - Display upload form
- `POST /upload` - Handle file upload

## Technical Details

- **Language**: Go
- **Dependencies**: Standard library only
- **Templates**: Embedded in binary using `embed` package
- **HTTP Features**: Range requests for resume support
- **Maximum upload size**: 100MB in memory

## License

See LICENSE file for details.
