@echo off
chcp 65001 >nul
setlocal

rem 生成不依赖本机 C 交叉编译器的 Linux amd64 可执行文件。
rem 该产物不包含 SQLite CGO 驱动能力，文件名显式标注 nocgo。
set "CGO_ENABLED=0"
set "GOOS=linux"
set "GOARCH=amd64"
set "OUTPUT_DIR=bin"
set "OUTPUT=%OUTPUT_DIR%\thinkgo-linux-amd64-nocgo"

if not exist "%OUTPUT_DIR%" (
    mkdir "%OUTPUT_DIR%"
    if errorlevel 1 (
        echo [ERROR] Failed to create output directory: %OUTPUT_DIR% 1>&2
        exit /b 1
    )
)

rem 删除旧产物，避免构建失败后把陈旧文件误认为本次结果。
if exist "%OUTPUT%" (
    del /f /q "%OUTPUT%"
    if errorlevel 1 (
        echo [ERROR] Failed to remove stale output: %OUTPUT% 1>&2
        exit /b 1
    )
)

go build -trimpath -buildvcs=false -o "%OUTPUT%" .
if errorlevel 1 (
    echo [ERROR] Linux amd64 build failed. 1>&2
    exit /b 1
)
if not exist "%OUTPUT%" (
    echo [ERROR] Build completed without producing %OUTPUT%. 1>&2
    exit /b 1
)

echo [SUCCESS] Linux amd64 nocgo executable: %OUTPUT%
exit /b 0
