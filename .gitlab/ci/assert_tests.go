// assert_tests 检查 go test -json 的实际执行结果；测试名称被发现或被 Skip 都不等于通过。
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
)

type testEvent struct {
	Action  string
	Package string
	Test    string
}

type testKey struct {
	pkg  string
	name string
}

type testOutcome struct {
	ran    bool
	result string
}

func assertTests(reader io.Reader, required []string) error {
	if len(required) == 0 {
		return errors.New("at least one required test is needed")
	}
	seen := make(map[string]bool, len(required))
	for _, name := range required {
		if strings.TrimSpace(name) == "" || seen[name] {
			return fmt.Errorf("empty or duplicate required test: %q", name)
		}
		seen[name] = true
	}
	outcomes := make(map[testKey]testOutcome)
	packages := make(map[string]string)
	decoder := json.NewDecoder(reader)
	for {
		var event testEvent
		if err := decoder.Decode(&event); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("invalid or truncated go test JSON: %w", err)
		}
		if event.Action == "fail" || event.Action == "build-fail" {
			return fmt.Errorf("test/build failed: package=%s test=%s", event.Package, event.Test)
		}
		if event.Action != "run" && event.Action != "pass" && event.Action != "skip" {
			continue
		}
		if event.Package == "" {
			return errors.New("test result is missing its package")
		}
		if event.Test == "" {
			packages[event.Package] = event.Action
			continue
		}
		key := testKey{pkg: event.Package, name: event.Test}
		outcome := outcomes[key]
		if event.Action == "run" {
			outcome.ran = true
		} else {
			if outcome.result != "" {
				return fmt.Errorf("duplicate terminal result: %s/%s", key.pkg, key.name)
			}
			outcome.result = event.Action
		}
		outcomes[key] = outcome
	}

	// 固定诊断顺序，避免 map 遍历顺序让 CI 错误输出漂移。
	keys := make([]testKey, 0, len(outcomes))
	for key := range outcomes {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].pkg != keys[j].pkg {
			return keys[i].pkg < keys[j].pkg
		}
		return keys[i].name < keys[j].name
	})
	for _, name := range required {
		matches := 0
		for _, key := range keys {
			if key.name != name && !strings.HasPrefix(key.name, name+"/") {
				continue
			}
			outcome := outcomes[key]
			if !outcome.ran || outcome.result != "pass" || packages[key.pkg] != "pass" {
				return fmt.Errorf("required test did not complete: %s/%s (ran=%t result=%q package=%q)",
					key.pkg, key.name, outcome.ran, outcome.result, packages[key.pkg])
			}
			if key.name == name {
				matches++
			}
		}
		if matches != 1 {
			return fmt.Errorf("required test %q must occur in exactly one package; found %d", name, matches)
		}
	}
	return nil
}

// checkReport 只消费调用方提供的输入流；命令行参数仅用于声明必需测试，不用于打开文件。
func checkReport(reader io.Reader, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("usage: assert_tests TestName [TestName/subtest ...] < REPORT.jsonl")
	}
	return assertTests(reader, arguments)
}

func main() {
	if err := checkReport(os.Stdin, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	fmt.Println("All required tests and their reported subtests ran and passed.")
}
