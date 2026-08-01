@echo off
chcp 65001 >nul
setlocal EnableExtensions

rem 固定脚本根目录，确保双击或从任意工作目录启动时输出和 Go 包解析一致。
cd /d "%~dp0"
if errorlevel 1 (
    echo [ERROR] 无法切换到项目目录：%~dp0 1>&2
    exit /b 1
)

rem 交互选择 Go 支持的目标平台；传入参数时可用于 CI 或自动化构建。
set "INTERACTIVE=0"
set "TARGET="
set "GOARM="
set "GOARM_ARG="
if /I "%~1"=="/?" goto :usage
if /I "%~1"=="-h" goto :usage
if /I "%~1"=="--help" goto :usage
if not "%~4"=="" (
    echo [ERROR] 参数过多。最多支持 GOOS GOARCH [GOARM]。 1>&2
    exit /b 2
)
where go >nul 2>&1
if errorlevel 1 (
    echo [ERROR] 未找到 Go 工具链，请先安装 Go 并将 go 加入 PATH。 1>&2
    exit /b 1
)
if "%~1"=="" goto :select_target
set "TARGET=%~1"
if not "%~2"=="" set "TARGET=%~1/%~2"
if not "%~3"=="" (
    set "GOARM_ARG=%~3"
    set "GOARM=%~3"
)
goto :parse_target

:select_target
set "INTERACTIVE=1"
echo.
echo 当前 Go 支持的目标平台：
for /f "delims=" %%T in ('go tool dist list') do echo   %%T
echo.
set /p "TARGET=请输入目标平台（例如 linux/arm64）："
if errorlevel 1 (
    echo [ERROR] 无法读取目标平台。 1>&2
    exit /b 2
)
if not defined TARGET (
    echo [ERROR] 目标平台不能为空。 1>&2
    exit /b 2
)

:parse_target
set "GOOS="
set "GOARCH="
set "TARGET_EXTRA="
for /f "tokens=1,2,3 delims=/" %%A in ("%TARGET%") do (
    set "GOOS=%%A"
    set "GOARCH=%%B"
    set "TARGET_EXTRA=%%C"
)
if not defined GOOS goto :invalid_target
if not defined GOARCH goto :invalid_target
if defined TARGET_EXTRA goto :invalid_target

set "TARGET_MATCH="
for /f "tokens=1,2 delims=/" %%A in ('go tool dist list') do (
    if /I "%%A/%%B"=="%GOOS%/%GOARCH%" (
        set "GOOS=%%A"
        set "GOARCH=%%B"
        set "TARGET_MATCH=1"
    )
)
if not defined TARGET_MATCH (
    echo [ERROR] Go 不支持目标平台：%GOOS%/%GOARCH% 1>&2
    goto :usage_error
)

if /I "%GOARCH%"=="arm" goto :configure_arm
goto :configure_output

:configure_arm
if defined GOARM goto :configure_output
if "%INTERACTIVE%"=="1" goto :prompt_goarm
set "GOARM=7"
goto :configure_output

:prompt_goarm
set /p "GOARM=请输入 GOARM（5、6、7，默认 7）："
if errorlevel 1 (
    echo [ERROR] 无法读取 GOARM。 1>&2
    exit /b 2
)
if not defined GOARM set "GOARM=7"

:configure_output
if /I not "%GOARCH%"=="arm" if defined GOARM_ARG (
    echo [ERROR] 只有 GOARCH=arm 时才能设置 GOARM。 1>&2
    exit /b 2
)
if /I "%GOARCH%"=="arm" (
    if not "%GOARM%"=="5" if not "%GOARM%"=="6" if not "%GOARM%"=="7" (
        echo [ERROR] GOARM 只能是 5、6 或 7。 1>&2
        exit /b 2
    )
)
set "TARGET_NAME=%GOOS%-%GOARCH%"
if /I "%GOARCH%"=="arm" set "TARGET_NAME=%TARGET_NAME%v%GOARM%"
set "SUFFIX="
if /I "%GOOS%"=="windows" set "SUFFIX=.exe"
if /I "%GOOS%"=="js" if /I "%GOARCH%"=="wasm" set "SUFFIX=.wasm"
set "CGO_ENABLED=0"
set "OUTPUT_DIR=bin"
set "OUTPUT=%OUTPUT_DIR%\thinkgo-%TARGET_NAME%-nocgo%SUFFIX%"

if not exist "%OUTPUT_DIR%" (
    mkdir "%OUTPUT_DIR%"
    if errorlevel 1 (
        echo [ERROR] 创建输出目录失败：%OUTPUT_DIR% 1>&2
        exit /b 1
    )
)

rem 删除同名旧产物，避免构建失败后误把旧文件当成本次结果。
if exist "%OUTPUT%" (
    del /f /q "%OUTPUT%"
    if errorlevel 1 (
        echo [ERROR] 删除旧产物失败：%OUTPUT% 1>&2
        exit /b 1
    )
)

go build -mod=readonly -trimpath -buildvcs=false -o "%OUTPUT%" .
if errorlevel 1 (
    echo [ERROR] %GOOS%/%GOARCH% 构建失败。 1>&2
    exit /b 1
)
if not exist "%OUTPUT%" (
    echo [ERROR] 构建完成但未生成产物：%OUTPUT% 1>&2
    exit /b 1
)

echo [SUCCESS] %GOOS%/%GOARCH% nocgo 可执行文件：%OUTPUT%
exit /b 0

:invalid_target
echo [ERROR] 目标平台必须采用 GOOS/GOARCH 格式，例如 linux/arm64。 1>&2
goto :usage_error

:usage_error
echo.
echo 用法：
echo   build.bat                         交互选择目标平台
echo   build.bat GOOS/GOARCH            构建指定平台
echo   build.bat GOOS GOARCH [GOARM]    分开传入目标和 ARM 版本
echo 示例：
echo   build.bat windows/amd64
echo   build.bat linux arm 7
exit /b 2

:usage
echo 用法：
echo   build.bat                         交互选择目标平台
echo   build.bat GOOS/GOARCH            构建指定平台
echo   build.bat GOOS GOARCH [GOARM]    分开传入目标和 ARM 版本
echo 示例：
echo   build.bat windows/amd64
echo   build.bat linux arm 7
exit /b 0
