#!/usr/bin/env bash
set -euo pipefail

# 固定脚本根目录，确保从任意工作目录启动时输出和 Go 包解析一致。
script_dir="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
cd -- "$script_dir"

# 交互选择 Go 支持的目标平台；传入参数时可用于 CI 或自动化构建。
usage() {
    cat <<'EOF'
用法：
  ./build.sh                         交互选择目标平台
  ./build.sh GOOS/GOARCH            构建指定平台
  ./build.sh GOOS GOARCH [GOARM]    分开传入目标和 ARM 版本
示例：
  ./build.sh windows/amd64
  ./build.sh linux arm 7
EOF
}

if [[ "${1:-}" == "-h" || "${1:-}" == "--help" ]]; then
    usage
    exit 0
fi

if ! command -v go >/dev/null 2>&1; then
    echo "[ERROR] 未找到 Go 工具链，请先安装 Go 并将 go 加入 PATH。" >&2
    exit 1
fi

interactive=0
target=""
goarm_arg="${3:-}"
goarm=""
case "$#" in
    0)
        interactive=1
        echo "当前 Go 支持的目标平台："
        go tool dist list
        if ! read -r -p "请输入目标平台（例如 linux/arm64）：" target; then
            echo "[ERROR] 无法读取目标平台。" >&2
            exit 2
        fi
        ;;
    1)
        target="$1"
        ;;
    2)
        target="$1/$2"
        ;;
    3)
        target="$1/$2"
        goarm_arg="$3"
        ;;
    *)
        echo "[ERROR] 参数过多。" >&2
        usage >&2
        exit 2
        ;;
esac

if [[ "$target" != */* || "$target" == */*/* ]]; then
    echo "[ERROR] 目标平台必须采用 GOOS/GOARCH 格式，例如 linux/arm64。" >&2
    usage >&2
    exit 2
fi

goos="${target%%/*}"
goarch="${target#*/}"
goos="${goos,,}"
goarch="${goarch,,}"
if ! go tool dist list | grep -Fxqi -- "$goos/$goarch"; then
    echo "[ERROR] Go 不支持目标平台：$goos/$goarch" >&2
    usage >&2
    exit 2
fi

if [[ "$goarch" == "arm" ]]; then
    goarm="${goarm_arg:-${GOARM:-}}"
fi
if [[ "$goarch" == "arm" && -z "$goarm" ]]; then
    if (( interactive )); then
        if ! read -r -p "请输入 GOARM（5、6、7，默认 7）：" goarm; then
            echo "[ERROR] 无法读取 GOARM。" >&2
            exit 2
        fi
        goarm="${goarm:-7}"
    else
        goarm=7
    fi
fi
if [[ -n "$goarm_arg" && "$goarch" != "arm" ]]; then
    echo "[ERROR] 只有 GOARCH=arm 时才能设置 GOARM。" >&2
    exit 2
fi
if [[ "$goarch" == "arm" && "$goarm" != "5" && "$goarm" != "6" && "$goarm" != "7" ]]; then
    echo "[ERROR] GOARM 只能是 5、6 或 7。" >&2
    exit 2
fi

target_name="$goos-$goarch"
if [[ "$goarch" == "arm" ]]; then
    target_name+="v$goarm"
fi
suffix=""
if [[ "$goos" == "windows" ]]; then
    suffix=".exe"
elif [[ "$goos" == "js" && "$goarch" == "wasm" ]]; then
    suffix=".wasm"
fi

output_dir="bin"
output="$output_dir/thinkgo-$target_name-nocgo$suffix"
mkdir -p "$output_dir"

# 删除同名旧产物，避免构建失败后误把旧文件当成本次结果。
if [[ -e "$output" || -L "$output" ]]; then
    rm -f -- "$output"
fi

build_env=("CGO_ENABLED=0" "GOOS=$goos" "GOARCH=$goarch")
if [[ "$goarch" == "arm" ]]; then
    build_env+=("GOARM=$goarm")
fi

if ! env "${build_env[@]}" go build -mod=readonly -trimpath -buildvcs=false -o "$output" .; then
    echo "[ERROR] $goos/$goarch 构建失败。" >&2
    exit 1
fi
if [[ ! -f "$output" ]]; then
    echo "[ERROR] 构建完成但未生成产物：$output" >&2
    exit 1
fi

printf '[SUCCESS] %s/%s nocgo 可执行文件：%s\n' "$goos" "$goarch" "$output"
