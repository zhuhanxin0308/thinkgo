package http

import (
	"bufio"
	"bytes"
	"compress/flate"
	"compress/gzip"
	"errors"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/andybalholm/brotli"
	"github.com/klauspost/compress/zstd"
)

const maxIdleCompressionWritersPerPool = 64

const (
	maxAcceptEncodingBytes   = 8192
	maxAcceptEncodingEntries = 64
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
	if len(p.writers) < maxIdleCompressionWritersPerPool {
		p.writers = append(p.writers, writer)
	}
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
	if err == nil && w.release != nil {
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
	encoding       string
	minSize        int
	levels         map[string]int
	headerSet      bool
	wroteHeader    bool
	closed         bool
	statusCode     int
	buffer         bytes.Buffer
	pooledWriter   *pooledCompressionWriter
	compressionErr error
}

// NewCompressionResponseWriter 创建新的压缩响应写入器。
func NewCompressionResponseWriter(w http.ResponseWriter, r *http.Request, minSize int, levels map[string]int) *CompressionResponseWriter {
	return newCompressionResponseWriter(w, r, minSize, levels, true)
}

// newConfiguredCompressionResponseWriter 使用启动阶段已经冻结的配置，避免每个请求重复复制压缩级别。
func newConfiguredCompressionResponseWriter(w http.ResponseWriter, r *http.Request, minSize int, levels map[string]int) *CompressionResponseWriter {
	return newCompressionResponseWriter(w, r, minSize, levels, false)
}

func newCompressionResponseWriter(w http.ResponseWriter, r *http.Request, minSize int, levels map[string]int, cloneLevels bool) *CompressionResponseWriter {
	encoding := ""
	if r != nil && r.Method != http.MethodHead {
		encoding = negotiateContentEncoding(r.Header.Get("Accept-Encoding"))
	}

	if minSize < 0 {
		minSize = 0
	}
	if minSize > 8<<20 {
		minSize = 8 << 20
	}
	copiedLevels := levels
	if cloneLevels {
		copiedLevels = make(map[string]int, len(levels))
	}
	if cloneLevels {
		duplicatedLevels := make(map[string]bool)
		for algorithm, level := range levels {
			normalized := strings.ToLower(strings.TrimSpace(algorithm))
			if duplicatedLevels[normalized] {
				continue
			}
			if _, exists := copiedLevels[normalized]; exists {
				// 公共构造器无法返回配置错误，发生大小写冲突时回退算法默认等级以保持确定性。
				delete(copiedLevels, normalized)
				duplicatedLevels[normalized] = true
				continue
			}
			copiedLevels[normalized] = level
		}
	}
	writer := &CompressionResponseWriter{
		ResponseWriter: w,
		encoding:       encoding,
		minSize:        minSize,
		levels:         copiedLevels,
		statusCode:     http.StatusOK,
	}
	addVaryHeader(writer.Header(), "Accept-Encoding")
	return writer
}

// WriteHeader 先缓存状态码，等待拿到响应体大小后再决定是否压缩。
func (w *CompressionResponseWriter) WriteHeader(code int) {
	if isInterimHTTPStatus(code) {
		if w.closed || w.headerSet || w.wroteHeader {
			return
		}
		w.ResponseWriter.WriteHeader(code)
		return
	}
	if w.headerSet {
		return
	}
	w.headerSet = true
	w.statusCode = code
}

// Write 在满足阈值前先缓冲，小响应直接透传，避免压缩收益为负。
func (w *CompressionResponseWriter) Write(b []byte) (int, error) {
	if w.closed {
		return 0, io.ErrClosedPipe
	}
	w.ensureContentType(b)
	if w.wroteHeader {
		return w.Writer.Write(b)
	}

	// 阈值前统一暂存，即使内容类型无需压缩，也为尚未 Flush 的流式响应保留异常回滚能力。
	if w.minSize > 0 && w.buffer.Len()+len(b) < w.minSize {
		return w.buffer.Write(b)
	}

	if w.shouldBypassCompression() {
		w.startPlainWriter()
		if err := w.flushBufferedBody(); err != nil {
			return 0, err
		}
		return w.Writer.Write(b)
	}

	if err := w.startCompressedWriter(); err != nil {
		return 0, err
	}
	if err := w.flushBufferedBody(); err != nil {
		return 0, err
	}
	return w.Writer.Write(b)
}

func (w *CompressionResponseWriter) ensureContentType(current []byte) {
	if len(current) == 0 || w.Header().Get("Content-Type") != "" {
		return
	}
	sampleSize := w.buffer.Len() + len(current)
	if sampleSize > 512 {
		sampleSize = 512
	}
	sample := make([]byte, 0, sampleSize)
	if w.buffer.Len() > 0 {
		buffered := w.buffer.Bytes()
		if len(buffered) > sampleSize {
			buffered = buffered[:sampleSize]
		}
		sample = append(sample, buffered...)
	}
	remaining := sampleSize - len(sample)
	if remaining > 0 {
		sample = append(sample, current[:remaining]...)
	}
	w.Header().Set("Content-Type", http.DetectContentType(sample))
}

// Close 在请求结束时补写尚未刷出的缓冲区，并关闭压缩器。
func (w *CompressionResponseWriter) Close() error {
	if w.closed {
		return w.compressionErr
	}
	w.closed = true
	var closeErr error
	if !w.wroteHeader {
		if w.shouldBypassCompression() || w.buffer.Len() == 0 || (w.minSize > 0 && w.buffer.Len() < w.minSize) {
			w.startPlainWriter()
		} else {
			if err := w.startCompressedWriter(); err != nil {
				closeErr = errors.Join(closeErr, err)
			}
		}
		if err := w.flushBufferedBody(); err != nil {
			closeErr = errors.Join(closeErr, err)
		}
	}

	if w.pooledWriter != nil {
		closeErr = errors.Join(closeErr, w.pooledWriter.Close())
		w.pooledWriter = nil
	}
	return errors.Join(w.compressionErr, closeErr)
}

// Abort 丢弃失败响应的剩余内容并释放压缩器，绝不向客户端补写成功尾部。
func (w *CompressionResponseWriter) Abort() {
	if w == nil || w.closed {
		return
	}
	w.closed = true
	w.buffer.Reset()
	if w.pooledWriter != nil {
		// Reset 先切断网络输出，再关闭并归还对象池，避免截断 gzip 被补成有效压缩流。
		w.pooledWriter.reset(io.Discard)
		_ = w.pooledWriter.Close()
		w.pooledWriter = nil
	}
}

// ResetUncommitted 丢弃尚未提交的业务缓冲和响应头，供统一异常处理安全重写响应。
func (w *CompressionResponseWriter) ResetUncommitted() bool {
	if w == nil || w.closed || w.wroteHeader {
		return false
	}
	w.buffer.Reset()
	w.headerSet = false
	w.statusCode = http.StatusOK
	w.Writer = nil
	w.compressionErr = nil
	for key := range w.Header() {
		w.Header().Del(key)
	}
	addVaryHeader(w.Header(), "Accept-Encoding")
	return true
}

// Flush 是旧具体类型的兼容入口；真实请求链只在底层支持时通过自适应 facade 暴露 Flusher。
// Deprecated: 新代码应对 ServeHTTP 提供的 writer 使用 http.NewResponseController(writer).Flush()。
func (w *CompressionResponseWriter) Flush() {
	_ = w.flushResponse()
}

// flushResponse 保留压缩错误，供自适应 facade 的 FlushError 返回给 ResponseController。
func (w *CompressionResponseWriter) flushResponse() error {
	if w.closed {
		return errors.Join(io.ErrClosedPipe, w.compressionErr)
	}
	if !w.wroteHeader {
		if w.shouldBypassCompression() || w.buffer.Len() == 0 || (w.minSize > 0 && w.buffer.Len() < w.minSize) {
			w.startPlainWriter()
			if err := w.flushBufferedBody(); err != nil {
				w.compressionErr = errors.Join(w.compressionErr, err)
			}
		} else if err := w.startCompressedWriter(); err == nil {
			if err = w.flushBufferedBody(); err != nil {
				w.compressionErr = errors.Join(w.compressionErr, err)
			}
		} else {
			w.compressionErr = errors.Join(w.compressionErr, err)
		}
	}

	if w.pooledWriter != nil {
		if err := w.pooledWriter.Flush(); err != nil {
			w.compressionErr = errors.Join(w.compressionErr, err)
		}
	}
	// 压缩器只负责把数据推到 ResponseWriter，还必须继续刷新网络层缓冲。
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
	return w.compressionErr
}

// shouldBypassCompression 判断当前响应是否应该跳过压缩。
func (w *CompressionResponseWriter) shouldBypassCompression() bool {
	if w.encoding == "" || w.Header().Get("Content-Encoding") != "" {
		return true
	}
	if w.statusCode < http.StatusOK || w.statusCode == http.StatusNoContent || w.statusCode == http.StatusNotModified || w.statusCode == http.StatusPartialContent {
		return true
	}
	if w.Header().Get("Content-Range") != "" || headerTokenContains(w.Header().Get("Cache-Control"), "no-transform") {
		return true
	}

	contentType := strings.ToLower(w.Header().Get("Content-Type"))
	return (strings.HasPrefix(contentType, "image/") && !strings.HasPrefix(contentType, "image/svg+xml")) ||
		strings.HasPrefix(contentType, "video/") ||
		strings.HasPrefix(contentType, "audio/") ||
		strings.HasPrefix(contentType, "application/octet-stream") ||
		strings.HasPrefix(contentType, "application/zip") ||
		strings.HasPrefix(contentType, "application/gzip") ||
		strings.HasPrefix(contentType, "text/event-stream")
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
		if l, ok := w.levels["zstd"]; ok && l >= 1 && l <= 4 {
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
		if l, ok := w.levels["gzip"]; ok && l >= gzip.HuffmanOnly && l <= gzip.BestCompression {
			level = l
		}
		compressor, err = getPooledCompressionWriter(w.encoding, level, w.ResponseWriter)
	case "deflate":
		level = flate.DefaultCompression
		if l, ok := w.levels["deflate"]; ok && l >= flate.HuffmanOnly && l <= flate.BestCompression {
			level = l
		}
		compressor, err = getPooledCompressionWriter(w.encoding, level, w.ResponseWriter)
	default:
		w.startPlainWriter()
		return nil
	}

	if err != nil {
		w.compressionErr = errors.Join(w.compressionErr, err)
		w.startPlainWriter()
		return nil
	}

	w.Header().Del("Content-Length")
	w.Header().Set("Content-Encoding", w.encoding)
	addVaryHeader(w.Header(), "Accept-Encoding")
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
		return nil, errors.New("不支持的压缩算法")
	}

	return compressor, nil
}

// Unwrap 允许 http.ResponseController 访问底层写入器能力。
func (w *CompressionResponseWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// Status 返回底层已记录状态，供标准库路由处理器生成终结器响应快照。
func (w *CompressionResponseWriter) Status() int {
	if provider, ok := w.ResponseWriter.(interface{ Status() int }); ok {
		return provider.Status()
	}
	return http.StatusOK
}

// Hijack 透传 WebSocket 等连接劫持能力。
func (w *CompressionResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	return w.hijackResponse()
}

// hijackResponse 只允许尚未开始最终响应的连接转移，避免丢弃压缩缓冲或半截压缩流。
func (w *CompressionResponseWriter) hijackResponse() (net.Conn, *bufio.ReadWriter, error) {
	if w == nil || w.closed || w.headerSet || w.wroteHeader || w.buffer.Len() > 0 || w.pooledWriter != nil {
		return nil, nil, ErrResponseBoundaryCrossed
	}
	connection, readWriter, err := http.NewResponseController(w.ResponseWriter).Hijack()
	if err != nil {
		return nil, nil, err
	}
	w.closed = true
	return connection, readWriter, nil
}

// Push 透传 HTTP/2 Server Push 能力。
func (w *CompressionResponseWriter) Push(target string, options *http.PushOptions) error {
	return w.pushResponse(target, options)
}

func (w *CompressionResponseWriter) pushResponse(target string, options *http.PushOptions) error {
	if w == nil || w.closed {
		return ErrResponseBoundaryCrossed
	}
	return pushThroughResponseWriter(w.ResponseWriter, target, options)
}

func negotiateContentEncoding(header string) string {
	if len(header) > maxAcceptEncodingBytes {
		return ""
	}
	items := strings.Split(header, ",")
	if len(items) > maxAcceptEncodingEntries {
		return ""
	}
	qualities := make(map[string]float64)
	wildcardQuality := -1.0
	for _, item := range items {
		parts := strings.Split(item, ";")
		name := strings.ToLower(strings.TrimSpace(parts[0]))
		if name == "" {
			continue
		}
		quality := 1.0
		valid := true
		qualityFound := false
		for _, parameter := range parts[1:] {
			keyValue := strings.SplitN(strings.TrimSpace(parameter), "=", 2)
			if len(keyValue) != 2 || !strings.EqualFold(strings.TrimSpace(keyValue[0]), "q") {
				continue
			}
			if qualityFound {
				valid = false
				break
			}
			qualityFound = true
			parsed, err := strconv.ParseFloat(strings.TrimSpace(keyValue[1]), 64)
			if err != nil || math.IsNaN(parsed) || math.IsInf(parsed, 0) || parsed < 0 || parsed > 1 {
				valid = false
				break
			}
			quality = parsed
		}
		if !valid {
			quality = 0
		}
		if name == "*" {
			wildcardQuality = quality
			continue
		}
		if previous, exists := qualities[name]; !exists || quality > previous {
			qualities[name] = quality
		}
	}

	selected := ""
	selectedQuality := 0.0
	for _, encoding := range []string{"zstd", "br", "gzip", "deflate"} {
		quality, exists := qualities[encoding]
		if !exists {
			quality = wildcardQuality
		}
		if quality > selectedQuality {
			selected = encoding
			selectedQuality = quality
		}
	}
	return selected
}

func headerTokenContains(value, target string) bool {
	for _, token := range strings.Split(value, ",") {
		if strings.EqualFold(strings.TrimSpace(token), target) {
			return true
		}
	}
	return false
}

func addVaryHeader(header http.Header, value string) {
	for _, existing := range header.Values("Vary") {
		if headerTokenContains(existing, value) {
			return
		}
	}
	header.Add("Vary", value)
}
