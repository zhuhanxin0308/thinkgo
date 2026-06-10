package http

import (
	"bytes"
	"compress/flate"
	"compress/gzip"
	"io"
	"net/http"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

// compressionPoolKey 标识同编码、同压缩级别的一组可复用压缩器。
type compressionPoolKey struct {
	encoding string
	level    int
}

var compressionWriterPools = struct {
	lock  sync.Mutex
	pools map[compressionPoolKey]*compressionWriterPool
}{
	pools: make(map[compressionPoolKey]*compressionWriterPool),
}

// compressionWriterPool 维护某一类压缩器的空闲实例栈，保证同编码请求优先复用最近归还的对象。
type compressionWriterPool struct {
	lock    sync.Mutex
	writers []*pooledCompressionWriter
}

// Get 取出一个空闲压缩器。
func (p *compressionWriterPool) Get() *pooledCompressionWriter {
	p.lock.Lock()
	defer p.lock.Unlock()

	if len(p.writers) == 0 {
		return nil
	}

	lastIndex := len(p.writers) - 1
	writer := p.writers[lastIndex]
	p.writers = p.writers[:lastIndex]
	return writer
}

// Put 归还一个空闲压缩器。
func (p *compressionWriterPool) Put(writer *pooledCompressionWriter) {
	if writer == nil {
		return
	}

	p.lock.Lock()
	p.writers = append(p.writers, writer)
	p.lock.Unlock()
}

// pooledCompressionWriter 封装可复用压缩器，并在关闭后归还到对应对象池。
type pooledCompressionWriter struct {
	writer  io.WriteCloser
	reset   func(io.Writer)
	flush   func() error
	release func(*pooledCompressionWriter)
	inUse   bool
}

// Write 透传压缩写入。
func (w *pooledCompressionWriter) Write(b []byte) (int, error) {
	return w.writer.Write(b)
}

// Flush 把压缩器内部缓冲刷新到下游写入器。
func (w *pooledCompressionWriter) Flush() error {
	if w.flush == nil {
		return nil
	}
	return w.flush()
}

// Close 关闭压缩流并把实例归还给对象池，避免重复分配压缩器状态机。
func (w *pooledCompressionWriter) Close() error {
	if w == nil || !w.inUse {
		return nil
	}

	err := w.writer.Close()
	w.inUse = false
	if w.reset != nil {
		w.reset(io.Discard)
	}
	if w.release != nil {
		w.release(w)
	}
	return err
}

// bind 将池化压缩器重新绑定到当前请求的响应写入器。
func (w *pooledCompressionWriter) bind(target io.Writer) {
	w.reset(target)
	w.inUse = true
}

// CompressionResponseWriter 为响应提供按需压缩能力，并在达到阈值前先做缓冲。
type CompressionResponseWriter struct {
	io.Writer
	http.ResponseWriter
	encoding     string
	minSize      int
	levels       map[string]int
	wroteHeader  bool
	statusCode   int
	buffer       bytes.Buffer
	pooledWriter *pooledCompressionWriter
}

// NewCompressionResponseWriter 创建新的压缩响应写入器。
func NewCompressionResponseWriter(w http.ResponseWriter, r *http.Request, minSize int, levels map[string]int) *CompressionResponseWriter {
	acceptEncoding := r.Header.Get("Accept-Encoding")
	encoding := ""

	if strings.Contains(acceptEncoding, "zstd") {
		encoding = "zstd"
	} else if strings.Contains(acceptEncoding, "br") {
		encoding = "br"
	} else if strings.Contains(acceptEncoding, "gzip") {
		encoding = "gzip"
	} else if strings.Contains(acceptEncoding, "deflate") {
		encoding = "deflate"
	}

	return &CompressionResponseWriter{
		ResponseWriter: w,
		encoding:       encoding,
		minSize:        minSize,
		levels:         levels,
		statusCode:     http.StatusOK,
	}
}

// WriteHeader 先缓存状态码，等待拿到响应体大小后再决定是否压缩。
func (w *CompressionResponseWriter) WriteHeader(code int) {
	if w.wroteHeader {
		return
	}
	w.statusCode = code
}

// Write 在满足阈值前先缓冲，小响应直接透传，避免压缩收益为负。
func (w *CompressionResponseWriter) Write(b []byte) (int, error) {
	if w.wroteHeader {
		return w.Writer.Write(b)
	}

	if w.shouldBypassCompression() {
		w.startPlainWriter()
		return w.Writer.Write(b)
	}

	if w.minSize > 0 && w.buffer.Len()+len(b) < w.minSize {
		return w.buffer.Write(b)
	}

	if err := w.startCompressedWriter(); err != nil {
		return 0, err
	}
	if err := w.flushBufferedBody(); err != nil {
		return 0, err
	}
	return w.Writer.Write(b)
}

// Close 在请求结束时补写尚未刷出的缓冲区，并关闭压缩器。
func (w *CompressionResponseWriter) Close() error {
	if !w.wroteHeader {
		if w.shouldBypassCompression() || w.buffer.Len() == 0 || (w.minSize > 0 && w.buffer.Len() < w.minSize) {
			w.startPlainWriter()
		} else {
			if err := w.startCompressedWriter(); err != nil {
				return err
			}
		}
		if err := w.flushBufferedBody(); err != nil {
			return err
		}
	}

	if c, ok := w.Writer.(io.Closer); ok {
		return c.Close()
	}
	return nil
}

// Flush 优先把已决策的数据刷出，兼容流式响应场景。
func (w *CompressionResponseWriter) Flush() {
	if !w.wroteHeader {
		if w.shouldBypassCompression() || w.buffer.Len() == 0 || (w.minSize > 0 && w.buffer.Len() < w.minSize) {
			w.startPlainWriter()
		} else if err := w.startCompressedWriter(); err == nil {
			_ = w.flushBufferedBody()
		}
	}

	type flushableWriter interface {
		Flush() error
	}

	if f, ok := w.Writer.(flushableWriter); ok {
		_ = f.Flush()
	} else if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// shouldBypassCompression 判断当前响应是否应该跳过压缩。
func (w *CompressionResponseWriter) shouldBypassCompression() bool {
	if w.encoding == "" || w.Header().Get("Content-Encoding") != "" {
		return true
	}

	contentType := w.Header().Get("Content-Type")
	return strings.HasPrefix(contentType, "image/") ||
		strings.HasPrefix(contentType, "video/") ||
		strings.HasPrefix(contentType, "audio/")
}

// startPlainWriter 切换到普通写出模式。
func (w *CompressionResponseWriter) startPlainWriter() {
	if w.wroteHeader {
		return
	}

	w.ResponseWriter.WriteHeader(w.statusCode)
	w.Writer = w.ResponseWriter
	w.wroteHeader = true
}

// startCompressedWriter 初始化压缩器并写入压缩响应头。
func (w *CompressionResponseWriter) startCompressedWriter() error {
	if w.wroteHeader {
		return nil
	}

	var (
		compressor *pooledCompressionWriter
		err        error
		level      int
	)

	switch w.encoding {
	case "zstd":
		level = 2
		zstdLevel := zstd.SpeedDefault
		if l, ok := w.levels["zstd"]; ok {
			level = l
			switch l {
			case 1:
				zstdLevel = zstd.SpeedFastest
			case 2:
				zstdLevel = zstd.SpeedDefault
			case 3:
				zstdLevel = zstd.SpeedBetterCompression
			case 4:
				zstdLevel = zstd.SpeedBestCompression
			}
		}
		compressor, err = getPooledCompressionWriter(w.encoding, level, w.ResponseWriter, zstd.WithEncoderLevel(zstdLevel))
	case "br":
		level = brotli.DefaultCompression
		if l, ok := w.levels["br"]; ok && l >= 0 && l <= 11 {
			level = l
		}
		compressor, err = getPooledCompressionWriter(w.encoding, level, w.ResponseWriter)
	case "gzip":
		level = gzip.DefaultCompression
		if l, ok := w.levels["gzip"]; ok {
			level = l
		}
		compressor, err = getPooledCompressionWriter(w.encoding, level, w.ResponseWriter)
	case "deflate":
		level = flate.DefaultCompression
		if l, ok := w.levels["deflate"]; ok {
			level = l
		}
		compressor, err = getPooledCompressionWriter(w.encoding, level, w.ResponseWriter)
	default:
		w.startPlainWriter()
		return nil
	}

	if err != nil {
		w.startPlainWriter()
		return nil
	}

	w.Header().Del("Content-Length")
	w.Header().Set("Content-Encoding", w.encoding)
	w.Header().Set("Vary", "Accept-Encoding")
	w.ResponseWriter.WriteHeader(w.statusCode)
	w.Writer = compressor
	w.pooledWriter = compressor
	w.wroteHeader = true
	return nil
}

// flushBufferedBody 把阈值判断阶段暂存的响应体刷到最终写入器。
func (w *CompressionResponseWriter) flushBufferedBody() error {
	if w.buffer.Len() == 0 {
		return nil
	}
	if _, err := w.Writer.Write(w.buffer.Bytes()); err != nil {
		return err
	}
	w.buffer.Reset()
	return nil
}

// getPooledCompressionWriter 根据编码与级别借出已初始化的压缩器。
func getPooledCompressionWriter(encoding string, level int, target io.Writer, options ...zstd.EOption) (*pooledCompressionWriter, error) {
	key := compressionPoolKey{
		encoding: encoding,
		level:    level,
	}
	pool := getCompressionWriterPool(key)

	if borrowed := pool.Get(); borrowed != nil {
		borrowed.bind(target)
		return borrowed, nil
	}

	compressor, err := newPooledCompressionWriter(pool, key, options...)
	if err != nil {
		return nil, err
	}
	compressor.bind(target)
	return compressor, nil
}

// getCompressionWriterPool 返回指定编码桶对应的对象池。
func getCompressionWriterPool(key compressionPoolKey) *compressionWriterPool {
	compressionWriterPools.lock.Lock()
	defer compressionWriterPools.lock.Unlock()

	if pool, ok := compressionWriterPools.pools[key]; ok {
		return pool
	}

	pool := &compressionWriterPool{}
	compressionWriterPools.pools[key] = pool
	return pool
}

// newPooledCompressionWriter 创建一个可放回对象池的压缩器包装。
func newPooledCompressionWriter(pool *compressionWriterPool, key compressionPoolKey, options ...zstd.EOption) (*pooledCompressionWriter, error) {
	compressor := &pooledCompressionWriter{
		release: func(writer *pooledCompressionWriter) {
			pool.Put(writer)
		},
	}

	switch key.encoding {
	case "gzip":
		writer, err := gzip.NewWriterLevel(io.Discard, key.level)
		if err != nil {
			return nil, err
		}
		compressor.writer = writer
		compressor.reset = writer.Reset
		compressor.flush = writer.Flush
	case "deflate":
		writer, err := flate.NewWriter(io.Discard, key.level)
		if err != nil {
			return nil, err
		}
		compressor.writer = writer
		compressor.reset = writer.Reset
		compressor.flush = writer.Flush
	case "br":
		writer := brotli.NewWriterLevel(io.Discard, key.level)
		compressor.writer = writer
		compressor.reset = writer.Reset
		compressor.flush = writer.Flush
	case "zstd":
		writer, err := zstd.NewWriter(io.Discard, options...)
		if err != nil {
			return nil, err
		}
		compressor.writer = writer
		compressor.reset = writer.Reset
		compressor.flush = writer.Flush
	default:
		return nil, nil
	}

	return compressor, nil
}
