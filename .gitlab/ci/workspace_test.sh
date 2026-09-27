#!/usr/bin/env bash
# 以真实 git 和 go 验证 CI 工作区边界，不使用伪造的 go 命令或跳过测试。
set -euo pipefail
script_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
fixture_root="$(mktemp -d "${TMPDIR:-/tmp}/thinkgo-ci-layout.XXXXXXXX")"
trap 'rm -rf -- "$fixture_root"' EXIT
origin="$fixture_root/origin with spaces"
mkdir -p "$origin/.gitlab/ci"
cp "$script_dir/check.sh" "$script_dir/assert_tests.go" "$origin/.gitlab/ci/"
version="$(go env GOVERSION)"
printf 'module example.com/thinkgo-ci-workspace\n\ngo %s\n' "${version#go}" > "$origin/go.mod"
printf 'committed\n' > "$origin/payload.txt"
cat > "$origin/workspace_test.go" <<'GO'
package workspace

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

func TestCommittedWorkspaceIsIsolated(t *testing.T) {
	for _, name := range []string{".cache", ".ci-reports", "untracked.txt"} {
		if _, err := os.Stat(name); !os.IsNotExist(err) {
			t.Fatalf("CI-only path %s entered the tested source tree: %v", name, err)
		}
	}
	payload, err := os.ReadFile("payload.txt")
	if err != nil || string(payload) != "committed\n" {
		t.Fatalf("tested uncommitted source: %q %v", payload, err)
	}
	sha, err := exec.Command("git", "rev-parse", "HEAD").Output()
	if err != nil || strings.TrimSpace(string(sha)) != os.Getenv("THINKGO_TEST_SOURCE_SHA") {
		t.Fatalf("wrong source commit: %q %v", sha, err)
	}
}
GO
gofmt -w "$origin/workspace_test.go"
git -C "$origin" init --quiet --initial-branch=main
git -C "$origin" add .
git -C "$origin" -c user.name='CI workspace test' -c user.email='ci-workspace@example.invalid' commit --quiet -m fixture
sha="$(git -C "$origin" rev-parse HEAD)"
# 覆盖 GitLab 常见的浅克隆、detached HEAD 以及路径含空格的情况。
shallow="$fixture_root/shallow"
git clone --quiet --depth=1 "file://$origin" "$shallow"
git -C "$shallow" checkout --quiet --detach "$sha"
test "$(git -C "$shallow" rev-parse --is-shallow-repository)" = true
for fixture in "$origin" "$shallow"; do
  mkdir -p "$fixture/.cache/go-build" "$fixture/.ci-reports"
  # 稀疏文件不会分配 500 MiB 数据块，但足以触发模块归档的未压缩大小限制。
  truncate -s 524288000 "$fixture/.cache/go-build/cache-only"
  printf 'old report\n' > "$fixture/.ci-reports/stale.log"
  printf 'untracked\n' > "$fixture/untracked.txt"
  printf 'dirty\n' > "$fixture/payload.txt"
  THINKGO_TEST_SOURCE_SHA="$sha" GOWORK=off GOTOOLCHAIN=local bash "$fixture/.gitlab/ci/check.sh" unit
  grep -Fxq "commit=$sha" "$fixture/.ci-reports/environment.txt"
  go run "$script_dir/assert_tests.go" TestCommittedWorkspaceIsIsolated < "$fixture/.ci-reports/unit.jsonl"
  # 隔离不能破坏原始 checkout，也不能为了通过检查删除缓存和历史报告。
  test -f "$fixture/.cache/go-build/cache-only"
  grep -Fxq 'old report' "$fixture/.ci-reports/stale.log"
  grep -Fxq 'dirty' "$fixture/payload.txt"
done
# 验证失败的 go test 不能被日志 tee 或 EXIT 清理掩盖。
cat > "$shallow/failure_test.go" <<'GO'
package workspace

import "testing"

func TestExpectedFailure(t *testing.T) { t.Fatal("intentional workspace regression probe") }
GO
gofmt -w "$shallow/failure_test.go"
git -C "$shallow" add failure_test.go
git -C "$shallow" -c user.name='CI workspace test' -c user.email='ci-workspace@example.invalid' commit --quiet -m failure-probe
failure_sha="$(git -C "$shallow" rev-parse HEAD)"
if THINKGO_TEST_SOURCE_SHA="$failure_sha" GOWORK=off GOTOOLCHAIN=local bash "$shallow/.gitlab/ci/check.sh" unit; then
  echo 'CI swallowed the intentional test failure' >&2
  exit 1
fi
grep -Fq '"Action":"fail"' "$shallow/.ci-reports/unit.jsonl"
printf 'CI workspace isolation and failure propagation passed.\n'
