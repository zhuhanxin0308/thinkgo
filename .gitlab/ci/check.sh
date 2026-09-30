#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C
source_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd -P)"
report_dir="$source_root/.ci-reports"
mkdir -p "$report_dir"
exec > >(tee "$report_dir/${1:-unknown}.log") 2>&1

# CreateFromDir 会遍历普通隐藏目录。缓存及持续增长的日志必须留在被测模块之外，
# 不能靠删除缓存、放宽归档上限或跳过归档测试规避这一边界。
source_sha="$(git -C "$source_root" rev-parse HEAD)"
workspace_root="$(mktemp -d "${TMPDIR:-/tmp}/thinkgo-ci.XXXXXXXX")"
trap 'rm -rf -- "$workspace_root"' EXIT
git clone --quiet --shared --no-checkout "$source_root" "$workspace_root/source"
git -C "$workspace_root/source" -c advice.detachedHead=false checkout --quiet --detach "$source_sha"
cd "$workspace_root/source"
test "$(git rev-parse HEAD)" = "$source_sha"

go version
# 不允许 runner 悄悄使用低于 go.mod 要求的工具链。
declared="$(awk '$1 == "go" { print $2; exit }' go.mod)"
actual="$(go env GOVERSION)"
if [[ "$actual" != "go${declared}" ]]; then
  printf 'Toolchain mismatch: got %s, require go%s\n' "$actual" "$declared" >&2
  exit 1
fi
printf 'commit=%s\ngo=%s\nCGO_ENABLED=%s\n' "$(git rev-parse HEAD)" "$actual" "${CGO_ENABLED:-}" > "$report_dir/environment.txt"

require_symbols() {
  local package="$1" symbols="$2" symbol
  shift 2
  go test -mod=readonly "$@" -list . "$package" > "$report_dir/listed-tests.txt"
  for symbol in $symbols; do
    if ! grep -Fxq "$symbol" "$report_dir/listed-tests.txt"; then
      printf 'Required test or benchmark not found: %s\n' "$symbol" >&2
      exit 1
    fi
  done
}

assert_passed() {
  local report="$1"
  shift
  go run -mod=readonly .gitlab/ci/assert_tests.go "$@" < "$report"
}

case "${1:-}" in
  verify)
    git ls-files -z -- '*.go' | xargs -0 gofmt -l > "$report_dir/gofmt.txt"
    if [[ -s "$report_dir/gofmt.txt" ]]; then
      cat "$report_dir/gofmt.txt"
      exit 1
    fi
    bash .gitlab/ci/workspace_test.sh
    go mod download
    go mod verify
    go mod tidy -diff
    go build -mod=readonly ./...
    go vet -mod=readonly ./...
    # 隐藏目录不在 ./... 内，显式测试用于判定 CI 是否完整的校验器。
    go vet -mod=readonly .gitlab/ci/assert_tests.go .gitlab/ci/assert_tests_test.go
    go test -race -mod=readonly -count=1 .gitlab/ci/assert_tests.go .gitlab/ci/assert_tests_test.go
    require_symbols . 'TestModuleReleaseArchive'
    go test -json -mod=readonly -run '^TestModuleReleaseArchive$' -count=1 . | tee "$report_dir/archive.jsonl"
    assert_passed "$report_dir/archive.jsonl" TestModuleReleaseArchive
    ;;
  unit)
    go test -json -mod=readonly -shuffle=on -count=1 -timeout=20m ./... | tee "$report_dir/unit.jsonl"
    ;;
  race)
    go test -json -race -mod=readonly -shuffle=on -count=1 -timeout=30m ./... | tee "$report_dir/race.jsonl"
    ;;
  coverage)
    # 先检查 go list 的退出码，不能让进程替换掩盖空包列表或编译错误。
    go list ./... > "$report_dir/packages.txt"
    test -s "$report_dir/packages.txt"
    while IFS= read -r package; do
      profile="$report_dir/${package//\//_}.out"
      go test -mod=readonly -count=1 -timeout=20m -coverprofile="$profile" "$package"
      statements="$(awk 'NR > 1 { count++ } END { print count + 0 }' "$profile")"
      if [[ "$statements" == '0' ]]; then
        printf '%s has no executable statements\n' "$package"
        continue
      fi
      go tool cover -func="$profile" > "$report_dir/current-coverage.txt"
      coverage="$(awk '/^total:/ {gsub(/%/, "", $3); print $3}' "$report_dir/current-coverage.txt")"
      [[ "$coverage" =~ ^[0-9]+([.][0-9]+)?$ ]]
      if ! awk -v actual="$coverage" 'BEGIN { exit !(actual + 0 >= 80) }'; then
        printf '%s coverage %s%% is below 80%%\n' "$package" "$coverage" >&2
        exit 1
      fi
    done < "$report_dir/packages.txt"
    ;;
  security)
    go run -mod=readonly honnef.co/go/tools/cmd/staticcheck@v0.7.0 ./...
    go run -mod=readonly github.com/securego/gosec/v2/cmd/gosec@v2.25.0 -fmt=json -out="$report_dir/gosec.json" -stdout -verbose=text -color=false -exclude-dir=tmp -exclude-dir=bin ./...
    go list -deps ./... > "$report_dir/dependencies.txt"
    if grep -Fxq 'golang.org/x/crypto/openpgp' "$report_dir/dependencies.txt"; then
      echo 'Unsupported openpgp dependency entered the framework' >&2
      exit 1
    fi
    go run -mod=readonly golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...
    ;;
  protocol)
    builder_symbols=(TestRebindNumberedPostgresArrays TestRebindNumberedRetainsSQLServerBracketIdentifiers TestRebindNumberedPostgresLexicalBoundaries)
    middleware_symbols=(TestCSRFDirectWriterGetsCookieBeforeCommit TestCSRFFailedCommitRegistrationStopsDownstream TestCSRFFailedTokenGenerationStopsDownstream)
    http_symbols=(TestCompressionDeflateUsesZlibWrapper TestCompressionImplicitStatusMatchesNetHTTP TestCompressionImplicitStatusCanBeResetBeforePhysicalCommit TestCompressionFlushLocksImplicitStatus TestCSRFHTTPCommitRoundTrip)
    require_symbols ./db/builder "${builder_symbols[*]} FuzzPostgresRebindLexicalIsolation"
    require_symbols ./middleware "${middleware_symbols[*]}"
    require_symbols ./http "${http_symbols[*]} TestHTTPHealthAllocationBudgets BenchmarkNativeHTTP BenchmarkSingleAppHTTP BenchmarkMultiAppHTTP"
    required=("${builder_symbols[@]}" "${middleware_symbols[@]}" "${http_symbols[@]}")
    # 父测试通过仍可能漏掉子场景；逐一要求 CSRF 的 15 种组合确实运行并通过。
    for compression in plain buffered compressed; do
      for kind in standard empty framework stream head; do
        required+=("TestCSRFHTTPCommitRoundTrip/${compression}/${kind}")
      done
    done
    go test -json -race -mod=readonly -count=1 -run '^Test(RebindNumbered|CSRF|Compression)' ./db/builder ./middleware ./http | tee "$report_dir/protocol.jsonl"
    assert_passed "$report_dir/protocol.jsonl" "${required[@]}"
    go test -json -mod=readonly -count=1 -run '^TestHTTPHealthAllocationBudgets$' ./http | tee "$report_dir/allocation.jsonl"
    assert_passed "$report_dir/allocation.jsonl" TestHTTPHealthAllocationBudgets
    go test -mod=readonly -run '^$' -bench '^Benchmark(NativeHTTP|SingleAppHTTP|MultiAppHTTP)$' -benchmem -benchtime=100ms -count=1 ./http
    require_symbols ./route 'FuzzRouteIndexParity'
    go test -mod=readonly -run '^$' -fuzz '^FuzzRouteIndexParity$' -fuzztime=20s ./route
    go test -mod=readonly -run '^$' -fuzz '^FuzzPostgresRebindLexicalIsolation$' -fuzztime=20s ./db/builder
    ;;
  live)
    packages=(. ./cache ./cache/driver/redis ./session/driver ./db ./db/builder ./migration ./queue/asynq)
    symbols=(RedisOperationContract RedisConditionalClear RedisScopedInvalidation RedisAtomicReservationOrder RedisAtomicTTLPolicy RedisSessionDriver RedisQueueRetriesFailedTask RedisQueueShutdownRestartPreservesTask MySQLQueryAndTransactionContract MySQLColdStatementIsolation MySQLReadinessRecovery MySQLMigrationRecovery PostgreSQLQueryAndTransactionContract PostgresRebindLexicalContract MongoOperationContract MongoStreamingContract NeoStrictAndDetachDelete)
    go test -mod=readonly -tags integration -list '^TestLive' "${packages[@]}" > "$report_dir/listed-live-tests.txt"
    required=()
    for symbol in "${symbols[@]}"; do
      grep -Fxq "TestLive${symbol}" "$report_dir/listed-live-tests.txt" || { echo "Missing live test: $symbol" >&2; exit 1; }
      required+=("TestLive${symbol}")
    done
    # 服务未就绪时显式失败，不能通过清空 THINKGO_LIVE_* 把契约测试变成 Skip。
    for endpoint in redis:6379 mysql:3306 postgres:5432 mongo:27017 neo4j:7687; do
      host="${endpoint%:*}"
      port="${endpoint##*:}"
      ready=false
      for ((attempt=0; attempt<60; attempt++)); do
        if timeout 2 bash -c 'exec 3<>/dev/tcp/"$1"/"$2"' _ "$host" "$port" 2>/dev/null; then
          ready=true
          break
        fi
        sleep 2
      done
      if [[ "$ready" != true ]]; then
        printf 'Service unavailable: %s\n' "$endpoint" >&2
        exit 1
      fi
    done
    pattern="$(IFS='|'; echo "${symbols[*]}")"
    go test -json -tags integration -race -mod=readonly -run "^TestLive(${pattern})$" -shuffle=on -count=1 -timeout=30m "${packages[@]}" | tee "$report_dir/live.jsonl"
    assert_passed "$report_dir/live.jsonl" "${required[@]}"
    ;;
  *)
    echo 'Usage: check.sh {verify|unit|race|coverage|security|protocol|live}' >&2
    exit 2
    ;;
esac
