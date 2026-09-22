package middleware

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/debug"
	frameworkVersion "github.com/zhuhanxin0308/thinkgo/v3/version"
)

// Trace 按 ThinkPHP Debug 状态和 trace.type 创建请求级调试 collector。
type Trace struct {
	Debug    *debug.Debug
	Location *time.Location
	Type     string
	Channel  string
}

// location 返回 Trace 使用的应用时区。
func (t *Trace) location() *time.Location {
	if t == nil || t.Location == nil {
		return time.UTC
	}
	return t.Location
}

const (
	traceStylesheetPath = "/__thinkgo_debug__/trace.css"
	traceScriptPath     = "/__thinkgo_debug__/trace.js"
)

const traceStylesheetContent = `
#tg-debug-bar { font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, Helvetica, Arial, sans-serif; font-size: 13px; line-height: 1.5; color: #333; }
#tg-debug-bar * { box-sizing: border-box; }
#tg-bar-toggle { position: fixed; bottom: 0; right: 20px; background: #333; color: #fff; padding: 5px 10px; border-radius: 4px 4px 0 0; cursor: pointer; z-index: 999999; font-weight: bold; font-size: 12px; box-shadow: 0 -2px 5px rgba(0,0,0,0.1); }
#tg-bar-container { position: fixed; bottom: 0; left: 0; right: 0; height: 300px; background: #fff; border-top: 1px solid #ccc; z-index: 999998; display: none; flex-direction: column; box-shadow: 0 -5px 20px rgba(0,0,0,0.1); }
#tg-bar-header { display: flex; background: #f5f5f5; border-bottom: 1px solid #ddd; height: 36px; align-items: center; padding: 0 10px; }
.tg-tab { padding: 0 15px; height: 36px; line-height: 36px; cursor: pointer; border-right: 1px solid #e0e0e0; color: #666; transition: all 0.2s; }
.tg-tab:hover, .tg-tab.active { background: #fff; color: #42b983; font-weight: 600; }
.tg-tab-logo { font-weight: bold; color: #333; margin-right: 10px; border: none; }
.tg-tab-close { margin-left: auto; border: none; font-size: 18px; padding: 0 10px; }
#tg-bar-content { flex: 1; overflow: auto; padding: 0; position: relative; }
.tg-panel { display: none; padding: 15px; }
.tg-panel.active { display: block; }
.tg-table { width: 100%; border-collapse: collapse; }
.tg-table th, .tg-table td { text-align: left; padding: 8px; border-bottom: 1px solid #eee; vertical-align: top; }
.tg-table th { background: #f9f9f9; color: #888; font-weight: 600; }
.tg-badge { display: inline-block; padding: 2px 6px; border-radius: 3px; font-size: 11px; color: #fff; }
.tg-badge-info { background: #2196F3; }
.tg-badge-error { background: #F44336; }
pre { margin: 0; white-space: pre-wrap; word-break: break-all; }
`

const traceScriptContent = `
window.TgDebug = window.TgDebug || {
	toggle: function() {
		var container = document.getElementById('tg-bar-container');
		var toggle = document.getElementById('tg-bar-toggle');
		if (!container || !toggle) {
			return;
		}
		if (container.style.display === 'flex') {
			container.style.display = 'none';
			toggle.style.display = 'block';
		} else {
			container.style.display = 'flex';
			toggle.style.display = 'none';
		}
	},
	tab: function(el, name) {
		if (!el || el.classList.contains('tg-tab-close') || el.classList.contains('tg-tab-logo')) {
			return;
		}

		var tabs = document.querySelectorAll('.tg-tab');
		tabs.forEach(function(tab) { tab.classList.remove('active'); });
		el.classList.add('active');

		var panels = document.querySelectorAll('.tg-panel');
		panels.forEach(function(panel) { panel.classList.remove('active'); });

		var panel = document.getElementById('tg-panel-' + name);
		if (panel) {
			panel.classList.add('active');
		}
	}
};
`

// Handle 处理请求并在 HTML 响应中注入调试面板。
// 面板内容全部在服务端完成转义，避免把未转义数据再交给前端 innerHTML 渲染。
func (t *Trace) Handle(req *context.Request, next func(*context.Request) *context.Response) *context.Response {
	if next == nil {
		return nil
	}
	if t == nil || t.Debug == nil || !t.Debug.Enabled {
		return next(req)
	}
	if req == nil {
		return nil
	}
	traceType := t.traceType()
	if traceType == "html" {
		if assetResp := t.serveTraceAsset(req); assetResp != nil {
			return assetResp
		}
	}
	reqDebug := debug.NewRequestDebug(true)
	reqDebug.SetLocation(t.location())
	reqDebug.SetLogChannel(t.Channel)
	req.Set(debug.RequestKey, reqDebug)
	defer reqDebug.Clear()

	start := time.Now().In(t.location())
	resp := next(req)
	duration := time.Since(start).Seconds()

	if resp == nil || shouldSkipTraceOutput(req, resp) {
		return resp
	}

	info := reqDebug.GetInfo()
	info["time"] = duration
	info["req_time"] = start.Format("2006-01-02 15:04:05")
	info["req_method"] = req.Method()
	info["req_uri"] = req.Url()
	info["req_ip"] = req.Ip()

	output := buildDebugBar(info)
	if traceType == "console" {
		output = buildConsoleTrace(info)
	}
	content := string(resp.GetBody())
	lowerContent := strings.ToLower(content)
	if position := strings.LastIndex(lowerContent, "</body>"); position >= 0 {
		resp.Content(content[:position] + output + content[position:])
	} else {
		resp.Content(content + output)
	}
	return resp
}

func (t *Trace) traceType() string {
	if t == nil || strings.TrimSpace(t.Type) == "" {
		return "html"
	}
	return strings.ToLower(strings.TrimSpace(t.Type))
}

func shouldSkipTraceOutput(req *context.Request, resp *context.Response) bool {
	if req == nil || resp == nil || resp.GetStatus() == http.StatusNoContent {
		return true
	}
	if req.IsAjax() || strings.Contains(strings.ToLower(req.ContentType()), "json") {
		return true
	}
	return !isHTMLResponse(resp)
}

func buildConsoleTrace(info map[string]interface{}) string {
	encoded, err := json.Marshal(info)
	if err != nil {
		encoded = []byte(`{"error":"trace encode failed"}`)
	}
	return "\n<script type='text/javascript'>\n" +
		"console.group('ThinkGo Trace');\n" +
		"console.log(" + string(encoded) + ");\n" +
		"console.groupEnd();\n" +
		"</script>\n"
}

// isHTMLResponse 判断响应是否为 HTML：优先看 Content-Type，未显式声明时按可注入处理。
func isHTMLResponse(resp *context.Response) bool {
	contentType := strings.ToLower(resp.Headers().Get("Content-Type"))
	if contentType == "" {
		return true
	}
	if strings.Contains(contentType, "text/html") {
		return true
	}
	// ThinkPHP 普通 Response 默认按页面处理；Go Content 的 text/plain 只是传输层差异。
	if strings.HasPrefix(contentType, "text/plain") {
		return true
	}
	return false
}

// serveTraceAsset 为调试面板提供静态 CSS/JS 资源，避免每个响应都重复内联整段样式与脚本。
func (t *Trace) serveTraceAsset(req *context.Request) *context.Response {
	switch req.Path() {
	case traceStylesheetPath:
		return context.NewResponse().
			ContentType("text/css", "utf-8").
			CacheControl("private, max-age=300").
			Content(traceStylesheetContent)
	case traceScriptPath:
		return context.NewResponse().
			ContentType("application/javascript", "utf-8").
			CacheControl("private, max-age=300").
			Content(traceScriptContent)
	default:
		return nil
	}
}

// buildDebugBar 构建已经转义完成的调试面板 HTML。
func buildDebugBar(info map[string]interface{}) string {
	sqlEntries := debugEntries(info["sqls"])
	cacheEntries := debugEntries(info["cache"])
	logEntries := debugEntries(info["logs"])
	files := debugStringSlice(info["files"])
	debugVars := debugVarsMap(info["vars"])
	truncated := info["truncated"]

	return fmt.Sprintf(`
<link rel="stylesheet" href="%s">
<div id="tg-debug-bar">
	<div id="tg-bar-toggle" onclick="TgDebug.toggle()">ThinkGo Debug</div>
	<div id="tg-bar-container">
		<div id="tg-bar-header">
			<div class="tg-tab tg-tab-logo">ThinkGo</div>
			<div class="tg-tab active" onclick="TgDebug.tab(this, 'base')">General</div>
			<div class="tg-tab" onclick="TgDebug.tab(this, 'sql')">SQL (%s)</div>
			<div class="tg-tab" onclick="TgDebug.tab(this, 'cache')">Cache (%s)</div>
			<div class="tg-tab" onclick="TgDebug.tab(this, 'log')">Logs (%s)</div>
			<div class="tg-tab" onclick="TgDebug.tab(this, 'file')">Files (%s)</div>
			<div class="tg-tab" onclick="TgDebug.tab(this, 'debug')">Debug (%s)</div>
			<div class="tg-tab tg-tab-close" onclick="TgDebug.toggle()">&times;</div>
		</div>
		<div id="tg-bar-content">
			<div id="tg-panel-base" class="tg-panel active">%s</div>
			<div id="tg-panel-sql" class="tg-panel">%s</div>
			<div id="tg-panel-cache" class="tg-panel">%s</div>
			<div id="tg-panel-log" class="tg-panel">%s</div>
			<div id="tg-panel-file" class="tg-panel">%s</div>
			<div id="tg-panel-debug" class="tg-panel">%s</div>
		</div>
	</div>
</div>
<script src="%s"></script>
`,
		traceStylesheetPath,
		debugCountLabel(len(sqlEntries), debugSnapshotTruncated(truncated, debug.KindSQL)),
		debugCountLabel(len(cacheEntries), debugSnapshotTruncated(truncated, debug.KindCache)),
		debugCountLabel(len(logEntries), debugSnapshotTruncated(truncated, debug.KindLog)),
		debugCountLabel(len(files), debugSnapshotTruncated(truncated, debug.KindFile)),
		debugCountLabel(len(debugVars), debugSnapshotTruncated(truncated, debug.KindVar)),
		renderBasePanel(info),
		renderSQLPanel(sqlEntries),
		renderCachePanel(cacheEntries),
		renderLogPanel(logEntries),
		renderFilePanel(files),
		renderDebugPanel(debugVars),
		traceScriptPath,
	)
}

// renderBasePanel 渲染基础请求信息面板。
func renderBasePanel(info map[string]interface{}) string {
	var builder strings.Builder
	builder.WriteString(`<table class="tg-table">`)
	writeDebugRow(&builder, "Version", frameworkVersion.Framework)
	writeDebugRow(&builder, "Time", fmt.Sprintf("%.4fs", debugFloat64(info["time"])))
	writeDebugRow(&builder, "Memory", formatDebugBytes(debugUint64(info["mem"])))
	writeDebugRow(&builder, "Method", debugText(info["req_method"]))
	writeDebugRow(&builder, "URI", debugText(info["req_uri"]))
	writeDebugRow(&builder, "Client IP", debugText(info["req_ip"]))
	writeDebugRow(&builder, "Req Time", debugText(info["req_time"]))
	builder.WriteString(`</table>`)
	return builder.String()
}

// renderSQLPanel 渲染 SQL 面板。
func renderSQLPanel(entries []map[string]interface{}) string {
	if len(entries) == 0 {
		return renderEmptyTable([]string{"Time", "Duration", "SQL"}, "No SQL executed", 3)
	}

	var builder strings.Builder
	builder.WriteString(`<table class="tg-table"><thead><tr><th>Time</th><th>Duration</th><th>SQL</th></tr></thead><tbody>`)
	for _, entry := range entries {
		builder.WriteString(`<tr>`)
		builder.WriteString(`<td width="100">` + escapeDebugText(debugText(entry["time"])) + `</td>`)
		builder.WriteString(`<td width="80">` + escapeDebugText(fmt.Sprintf("%.4fs", debugFloat64(entry["duration"]))) + `</td>`)
		builder.WriteString(`<td><pre>` + escapeDebugText(debugText(entry["sql"])) + `</pre></td>`)
		builder.WriteString(`</tr>`)
	}
	builder.WriteString(`</tbody></table>`)
	return builder.String()
}

// renderCachePanel 渲染缓存面板。
func renderCachePanel(entries []map[string]interface{}) string {
	if len(entries) == 0 {
		return renderEmptyTable([]string{"Time", "Op", "Key"}, "No cache operations", 3)
	}

	var builder strings.Builder
	builder.WriteString(`<table class="tg-table"><thead><tr><th>Time</th><th>Op</th><th>Key</th></tr></thead><tbody>`)
	for _, entry := range entries {
		builder.WriteString(`<tr>`)
		builder.WriteString(`<td width="100">` + escapeDebugText(debugText(entry["time"])) + `</td>`)
		builder.WriteString(`<td width="60">` + escapeDebugText(debugText(entry["op"])) + `</td>`)
		builder.WriteString(`<td><pre>` + escapeDebugText(debugText(entry["key"])) + `</pre></td>`)
		builder.WriteString(`</tr>`)
	}
	builder.WriteString(`</tbody></table>`)
	return builder.String()
}

// renderLogPanel 渲染日志面板。
func renderLogPanel(entries []map[string]interface{}) string {
	if len(entries) == 0 {
		return renderEmptyTable([]string{"Time", "Level", "Message"}, "No logs", 3)
	}

	var builder strings.Builder
	builder.WriteString(`<table class="tg-table"><thead><tr><th>Time</th><th>Level</th><th>Message</th></tr></thead><tbody>`)
	for _, entry := range entries {
		level := debugText(entry["level"])
		badgeClass := "tg-badge-info"
		if strings.EqualFold(level, "error") {
			badgeClass = "tg-badge-error"
		}

		builder.WriteString(`<tr>`)
		builder.WriteString(`<td width="100">` + escapeDebugText(debugText(entry["time"])) + `</td>`)
		builder.WriteString(`<td width="60"><span class="tg-badge ` + badgeClass + `">` + escapeDebugText(level) + `</span></td>`)
		builder.WriteString(`<td><pre>` + escapeDebugText(debugText(entry["msg"])) + `</pre></td>`)
		builder.WriteString(`</tr>`)
	}
	builder.WriteString(`</tbody></table>`)
	return builder.String()
}

// renderFilePanel 渲染文件面板。
func renderFilePanel(files []string) string {
	if len(files) == 0 {
		return renderEmptyTable([]string{"File"}, "No files loaded", 1)
	}

	var builder strings.Builder
	builder.WriteString(`<table class="tg-table"><thead><tr><th>File</th></tr></thead><tbody>`)
	for _, file := range files {
		builder.WriteString(`<tr><td><pre>` + escapeDebugText(file) + `</pre></td></tr>`)
	}
	builder.WriteString(`</tbody></table>`)
	return builder.String()
}

// renderDebugPanel 渲染调试变量面板。
func renderDebugPanel(vars map[string]interface{}) string {
	if len(vars) == 0 {
		return renderEmptyTable([]string{"Key", "Value"}, "No debug variables", 2)
	}

	keys := make([]string, 0, len(vars))
	for key := range vars {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var builder strings.Builder
	builder.WriteString(`<table class="tg-table"><thead><tr><th>Key</th><th>Value</th></tr></thead><tbody>`)
	for _, key := range keys {
		builder.WriteString(`<tr>`)
		builder.WriteString(`<td width="150"><b>` + escapeDebugText(key) + `</b></td>`)
		builder.WriteString(`<td><pre>` + escapeDebugText(formatDebugValue(vars[key])) + `</pre></td>`)
		builder.WriteString(`</tr>`)
	}
	builder.WriteString(`</tbody></table>`)
	return builder.String()
}

// renderEmptyTable 渲染空状态表格，保持各面板结构一致。
func renderEmptyTable(headers []string, emptyText string, colspan int) string {
	var builder strings.Builder
	builder.WriteString(`<table class="tg-table"><thead><tr>`)
	for _, header := range headers {
		builder.WriteString(`<th>` + escapeDebugText(header) + `</th>`)
	}
	builder.WriteString(`</tr></thead><tbody>`)
	builder.WriteString(fmt.Sprintf(`<tr><td colspan="%d">%s</td></tr>`, colspan, escapeDebugText(emptyText)))
	builder.WriteString(`</tbody></table>`)
	return builder.String()
}

// writeDebugRow 渲染基础信息中的单行键值对。
func writeDebugRow(builder *strings.Builder, label string, value string) {
	builder.WriteString(`<tr><th width="150">`)
	builder.WriteString(escapeDebugText(label))
	builder.WriteString(`</th><td>`)
	builder.WriteString(escapeDebugText(value))
	builder.WriteString(`</td></tr>`)
}

// debugEntries 把调试信息中的列表项统一转换为 map 切片。
func debugEntries(raw interface{}) []map[string]interface{} {
	if raw == nil {
		return nil
	}

	switch typed := raw.(type) {
	case []map[string]interface{}:
		return typed
	case []interface{}:
		result := make([]map[string]interface{}, 0, len(typed))
		for _, item := range typed {
			if entry, ok := item.(map[string]interface{}); ok {
				result = append(result, entry)
			}
		}
		return result
	default:
		return nil
	}
}

// debugStringSlice 把调试信息中的文件列表转换为字符串切片。
func debugStringSlice(raw interface{}) []string {
	if raw == nil {
		return nil
	}

	switch typed := raw.(type) {
	case []string:
		return typed
	case []interface{}:
		result := make([]string, 0, len(typed))
		for _, item := range typed {
			result = append(result, debugText(item))
		}
		return result
	default:
		return nil
	}
}

// debugVarsMap 把调试变量统一转换为 map。
func debugVarsMap(raw interface{}) map[string]interface{} {
	if raw == nil {
		return map[string]interface{}{}
	}
	if typed, ok := raw.(map[string]interface{}); ok {
		return typed
	}
	return map[string]interface{}{}
}

// debugSnapshotTruncated 从 collector 快照读取指定类别的截断状态。
func debugSnapshotTruncated(raw interface{}, kind debug.Kind) bool {
	switch values := raw.(type) {
	case map[string]bool:
		return values[string(kind)]
	case map[string]interface{}:
		truncated, _ := values[string(kind)].(bool)
		return truncated
	default:
		return false
	}
}

// debugCountLabel 把被截断的类别明确标记在调试页标签中。
func debugCountLabel(count int, truncated bool) string {
	if truncated {
		return fmt.Sprintf("%d, truncated", count)
	}
	return fmt.Sprintf("%d", count)
}

// debugText 把调试值稳定转换为字符串。
func debugText(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	default:
		return fmt.Sprintf("%v", typed)
	}
}

// debugFloat64 读取浮点值，兼容常见数值类型。
func debugFloat64(value interface{}) float64 {
	switch typed := value.(type) {
	case float64:
		return typed
	case float32:
		return float64(typed)
	case int:
		return float64(typed)
	case int64:
		return float64(typed)
	case uint64:
		return float64(typed)
	default:
		return 0
	}
}

// debugUint64 读取无符号整型，兼容调试面板内的内存数值。
func debugUint64(value interface{}) uint64 {
	switch typed := value.(type) {
	case uint64:
		return typed
	case uint32:
		return uint64(typed)
	case int:
		if typed > 0 {
			return uint64(typed)
		}
	case int64:
		if typed > 0 {
			return uint64(typed)
		}
	case float64:
		if typed > 0 {
			return uint64(typed)
		}
	}
	return 0
}

// formatDebugBytes 把字节数格式化为易读文本。
func formatDebugBytes(bytes uint64) string {
	value := float64(bytes)
	unit := "B"
	if value >= 1024 {
		value /= 1024
		unit = "KB"
	}
	if value >= 1024 {
		value /= 1024
		unit = "MB"
	}
	if value >= 1024 {
		value /= 1024
		unit = "GB"
	}
	return fmt.Sprintf("%.2f %s", value, unit)
}

// formatDebugValue 把复杂调试变量编码成稳定的文本形式，便于服务端直接转义输出。
func formatDebugValue(value interface{}) string {
	switch typed := value.(type) {
	case nil:
		return ""
	case string:
		return typed
	default:
		formatted, err := json.MarshalIndent(typed, "", "  ")
		if err == nil {
			return string(formatted)
		}
		return fmt.Sprintf("%v", typed)
	}
}

// escapeDebugText 对最终输出到 HTML 的文本统一做转义。
func escapeDebugText(value string) string {
	return html.EscapeString(value)
}
