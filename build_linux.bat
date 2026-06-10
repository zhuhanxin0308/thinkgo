@echo off
:: 设置交叉编译环境变量
SET CGO_ENABLED=0
SET GOOS=linux
SET GOARCH=amd64

:: 带构建标签编译（可选）
:: go build -tags "jsoniter" -o output_name
:: 无标签编译示例
go build -o thinkgo

:: 验证输出
if exist thinkgo (
    echo [SUCCESS] Linux amd64 executable: thinkgo
) else (
    echo [ERROR] Compilation failed
)
pause