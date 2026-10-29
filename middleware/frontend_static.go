package middleware

import (
	"bytes"
	"compress/gzip"
	"errors"
	"io"
	"mime"
	"net/http"
	"path"
	"strconv"
	"strings"
	"sync"

	ginGzip "github.com/gin-contrib/gzip"
	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/klauspost/compress/zstd"
)

// precompressedFile caches one frontend build file in the encodings clients
// ask for. The build never changes while the process runs, so each encoding
// of each file is compressed once and the bytes are reused, instead of
// recompressing multi-megabyte chunks on every request.
type precompressedFile struct {
	gzipOnce    sync.Once
	zstdOnce    sync.Once
	gzipBody    []byte
	zstdBody    []byte
	contentType string
	err         error
}

// zstdBest is shared across files: EncodeAll is safe for concurrent use and
// the encoder carries the one-time expensive table setup.
var (
	zstdBest     *zstd.Encoder
	zstdBestOnce sync.Once
)

func zstdBestEncoder() *zstd.Encoder {
	zstdBestOnce.Do(func() {
		enc, err := zstd.NewWriter(nil, zstd.WithEncoderLevel(zstd.SpeedBestCompression))
		if err != nil {
			enc, _ = zstd.NewWriter(nil)
		}
		zstdBest = enc
	})
	return zstdBest
}

// ServeFrontendFiles serves files from the embedded frontend build, picking
// zstd or gzip by Accept-Encoding. Clients without either, the image types
// the compression middlewares skip, directories, and the index.html redirect
// keep using http.FileServer.
func ServeFrontendFiles(frontendFS static.ServeFileSystem) gin.HandlerFunc {
	fileServer := http.FileServer(frontendFS)
	var cache sync.Map // URL path -> *precompressedFile
	return func(c *gin.Context) {
		urlPath := c.Request.URL.Path
		if !frontendFS.Exists("/", urlPath) {
			return
		}
		acceptEncoding := c.GetHeader("Accept-Encoding")
		excluded := ginGzip.DefaultExcludedExtentions.Contains(path.Ext(urlPath)) ||
			strings.HasSuffix(urlPath, "/index.html")
		useZstd := !excluded && strings.Contains(acceptEncoding, "zstd")
		useGzip := !useZstd && !excluded && strings.Contains(acceptEncoding, "gzip")
		if !useZstd && !useGzip {
			c.Abort()
			fileServer.ServeHTTP(c.Writer, c.Request)
			return
		}

		value, ok := cache.Load(urlPath)
		if !ok {
			value, _ = cache.LoadOrStore(urlPath, &precompressedFile{})
		}
		file := value.(*precompressedFile)

		readRaw := func() ([]byte, error) {
			f, err := frontendFS.Open(urlPath)
			if err != nil {
				return nil, err
			}
			defer f.Close()
			info, err := f.Stat()
			if err != nil {
				return nil, err
			}
			if info.IsDir() {
				return nil, errors.New("directory")
			}
			return io.ReadAll(f)
		}
		setContentType := func(raw []byte) {
			if file.contentType == "" {
				// Match http.FileServer: extension first, then content sniffing.
				file.contentType = mime.TypeByExtension(path.Ext(urlPath))
				if file.contentType == "" {
					file.contentType = http.DetectContentType(raw)
				}
			}
		}

		encoding := "gzip"
		if useZstd {
			encoding = "zstd"
			file.zstdOnce.Do(func() {
				raw, err := readRaw()
				if err != nil {
					file.err = err
					return
				}
				setContentType(raw)
				file.zstdBody = zstdBestEncoder().EncodeAll(raw, nil)
			})
			if file.err != nil || file.zstdBody == nil {
				c.Abort()
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		} else {
			file.gzipOnce.Do(func() {
				raw, err := readRaw()
				if err != nil {
					file.err = err
					return
				}
				setContentType(raw)
				var compressed bytes.Buffer
				writer := gzip.NewWriter(&compressed)
				if _, err := writer.Write(raw); err != nil {
					file.err = err
					return
				}
				if err := writer.Close(); err != nil {
					file.err = err
					return
				}
				file.gzipBody = bytes.Clone(compressed.Bytes())
			})
			if file.err != nil || file.gzipBody == nil {
				c.Abort()
				fileServer.ServeHTTP(c.Writer, c.Request)
				return
			}
		}

		body := file.zstdBody
		if encoding == "gzip" {
			body = file.gzipBody
		}
		// Range requests get the whole body; a server may ignore Range.
		header := c.Writer.Header()
		header.Set("Content-Encoding", encoding)
		header.Set("Vary", "Accept-Encoding")
		header.Set("Content-Type", file.contentType)
		header.Set("Content-Length", strconv.Itoa(len(body)))
		c.Writer.WriteHeader(http.StatusOK)
		if c.Request.Method != http.MethodHead {
			_, _ = c.Writer.Write(body)
		}
	}
}
