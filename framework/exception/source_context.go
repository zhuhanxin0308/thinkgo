package exception

import (
	"bufio"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// parseGoStack 把 Go 堆栈解析为结构化帧信息。
func parseGoStack(stack string) []StackFrame {
	lines := strings.Split(stack, "\n")
	frames := make([]StackFrame, 0)

	for index := 0; index < len(lines); index++ {
		line := strings.TrimSpace(lines[index])
		if line == "" || strings.HasPrefix(line, "goroutine ") {
			continue
		}

		functionName := line
		if left := strings.Index(functionName, "("); left > 0 {
			functionName = functionName[:left]
		}

		if index+1 >= len(lines) {
			continue
		}

		file, lineNumber := parseFileLine(strings.TrimSpace(lines[index+1]))
		if file == "" {
			continue
		}

		frame := StackFrame{
			Function:  functionName,
			File:      file,
			ShortFile: filepath.Base(file),
			Line:      lineNumber,
			Source:    loadSourceContext(file, lineNumber, 8),
		}
		frames = append(frames, frame)
		index++
	}

	return frames
}

// parseFileLine 解析堆栈中的文件位置信息。
func parseFileLine(value string) (string, int) {
	value = strings.TrimSpace(value)
	if index := strings.LastIndex(value, " +0x"); index > 0 {
		value = value[:index]
	}

	for index := len(value) - 1; index >= 0; index-- {
		if value[index] != ':' {
			continue
		}
		lineValue := value[index+1:]
		lineNumber, err := strconv.Atoi(lineValue)
		if err == nil {
			return value[:index], lineNumber
		}
	}

	return value, 0
}

// loadSourceContext 加载错误位置附近的源码上下文。
func loadSourceContext(filePath string, targetLine int, contextLines int) []SourceLine {
	// #nosec G304 -- filePath 来自运行时 Go 堆栈，调试源码读取仅在受控调试边界内执行。
	file, err := os.Open(filePath)
	if err != nil {
		return nil
	}
	defer file.Close()

	startLine := targetLine - contextLines
	if startLine < 1 {
		startLine = 1
	}
	endLine := targetLine + contextLines

	lines := make([]SourceLine, 0, endLine-startLine+1)
	scanner := bufio.NewScanner(file)
	lineNumber := 0

	for scanner.Scan() {
		lineNumber++
		if lineNumber < startLine {
			continue
		}
		if lineNumber > endLine {
			break
		}

		lines = append(lines, SourceLine{
			Number:    lineNumber,
			Content:   scanner.Text(),
			IsCurrent: lineNumber == targetLine,
			IsNear:    lineNumber >= targetLine-2 && lineNumber <= targetLine+2,
		})
	}

	return lines
}

// isRuntimeFrame 判断该帧是否属于运行时或框架内部噪声。
func isRuntimeFrame(functionName string) bool {
	return strings.HasPrefix(functionName, "runtime.") ||
		strings.HasPrefix(functionName, "runtime/debug.") ||
		strings.Contains(functionName, "exception.") ||
		strings.Contains(functionName, "middleware.")
}
