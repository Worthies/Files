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

### Install (recommended)

Install the latest stable release with the `go` command:

```bash
go install github.com/worthies/files@latest
```

Install the current tip of the default branch (useful for nightly/testing builds):

```bash
go install github.com/worthies/files@master
```

### Download pre-compiled binaries

You can also download pre-compiled binaries from the [nightly releases](https://github.com/worthies/files/releases):

1. Go to the [Releases page](https://github.com/worthies/files/releases)
2. Download the appropriate binary for your platform (Windows, Linux, macOS)
3. Make the binary executable (on Unix-like systems): `chmod +x files`
4. Move it to a directory in your PATH or run it directly

Notes:
- `go install ...@latest` installs the latest released module version.
- Installing `@master` (or `@main`) fetches the tip of the branch — treat this as a nightly/edge build.
- The installed binary is placed in `$GOBIN` (if set) or `$(go env GOPATH)/bin`; make sure that directory is on your `PATH`:

```bash
export PATH=$PATH:$(go env GOPATH)/bin
```

### Build from source

If you prefer to build locally:

```bash
git clone https://github.com/worthies/files.git
cd files
go build -o files ./...
```

Requirements:
- Go 1.21 or newer (see `go.mod`).


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
- `-i <config>` - Enable intelligent MIME recognition for browser-viewable multimedia. Use `true` for default mappings, or specify custom mappings in format: `ext1,ext2:mime/type;ext3:mime/type2,v` where `,v` indicates viewable in browser (optional)

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

Enable intelligent MIME recognition:
```bash
./files -i true
```

Enable intelligent MIME recognition with custom MIME type mappings:
```bash
# Map .mhtml, .shtml to text/html (viewable in browser)
./files -i "mhtml,shtml:text/html,v"

# Multiple mappings with different MIME types
./files -i "mhtml,shtml:text/html,v;custom:application/custom;doc:application/msword"

# Mix viewable and non-viewable types
./files -i "mhtml,shtml:text/html,v;archive:application/x-archive"
```

Enable intelligent MIME recognition with other options:
```bash
./files -i true -port 9000 -dir /path/to/files
./files -i "mhtml,shtml:text/html,v" -port 9000 -dir /path/to/files
```

## Features Details

### Request Logging
- All HTTP requests are logged to console with method, path, and client IP address
- Request completion time is displayed for performance monitoring
- Useful for debugging and monitoring server activity

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

### Intelligent MIME Recognition
When enabled with `-i`, the server intelligently recognizes file types and serves them inline in the browser when appropriate:
- **Default mode** (`-i true`): Recognizes common multimedia and document types (images, audio, video, PDF, HTML, etc.)
- **Custom mappings**: Map file extensions to custom MIME types with optional viewability control
  - Example: `jpg,png:image/jpeg,v` maps .jpg and .png files to image/jpeg and marks as viewable
  - Non-viewable types: `zip:application/zip` will download the file
  - Viewable types marked with `,v`: served inline in browser
  - Without `,v`: serves as attachment (download)

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
