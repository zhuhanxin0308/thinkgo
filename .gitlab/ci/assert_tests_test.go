package main

import (
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func eventLog(events ...testEvent) string {
	var result strings.Builder
	encoder := json.NewEncoder(&result)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			panic(err)
		}
	}
	return result.String()
}

func TestAssertTestsRequiresExecutedPassingTests(t *testing.T) {
	passed := []testEvent{{"run", "p", "TestRequired"}, {"pass", "p", "TestRequired"}, {"pass", "p", ""}}
	for _, test := range []struct {
		name     string
		log      string
		required []string
		wantErr  bool
	}{
		{"pass", eventLog(passed...), []string{"TestRequired"}, false},
		{"empty", "", []string{"TestRequired"}, true},
		{"discovery only", eventLog(testEvent{"output", "p", "TestRequired"}, testEvent{"pass", "p", ""}), []string{"TestRequired"}, true},
		{"missing test", eventLog(passed...), []string{"TestMissing"}, true},
		{"missing run", eventLog(passed[1:]...), []string{"TestRequired"}, true},
		{"test skipped", eventLog(testEvent{"run", "p", "TestRequired"}, testEvent{"skip", "p", "TestRequired"}, testEvent{"pass", "p", ""}), []string{"TestRequired"}, true},
		{"package incomplete", eventLog(passed[:2]...), []string{"TestRequired"}, true},
		{"test incomplete", eventLog(passed[0], passed[2]), []string{"TestRequired"}, true},
		{"test failed", eventLog(testEvent{"fail", "p", "TestRequired"}), []string{"TestRequired"}, true},
		{"package failed", eventLog(append(passed, testEvent{"fail", "p", ""})...), []string{"TestRequired"}, true},
		{"build failed", eventLog(testEvent{"build-fail", "p", ""}), []string{"TestRequired"}, true},
		{"missing package", eventLog(testEvent{"pass", "", "TestRequired"}), []string{"TestRequired"}, true},
		{"truncated", eventLog(passed...) + `{"Action":`, []string{"TestRequired"}, true},
		{"malformed", "not json", []string{"TestRequired"}, true},
		{"no requirements", eventLog(passed...), nil, true},
		{"empty requirement", eventLog(passed...), []string{""}, true},
		{"duplicate requirement", eventLog(passed...), []string{"TestRequired", "TestRequired"}, true},
		{"duplicate result", eventLog(append(passed, passed[1])...), []string{"TestRequired"}, true},
		{"package ambiguous", eventLog(append(passed, testEvent{"run", "q", "TestRequired"}, testEvent{"pass", "q", "TestRequired"}, testEvent{"pass", "q", ""})...), []string{"TestRequired"}, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := assertTests(strings.NewReader(test.log), test.required)
			if (err != nil) != test.wantErr {
				t.Fatalf("error = %v; want error=%t", err, test.wantErr)
			}
		})
	}
}

func TestAssertTestsChecksRequiredSubtests(t *testing.T) {
	for _, outcome := range []string{"pass", "skip", ""} {
		t.Run(outcome, func(t *testing.T) {
			events := []testEvent{{"run", "p", "TestRequired"}, {"run", "p", "TestRequired/case"}}
			if outcome != "" {
				events = append(events, testEvent{outcome, "p", "TestRequired/case"})
			}
			events = append(events, testEvent{"pass", "p", "TestRequired"}, testEvent{"pass", "p", ""})
			err := assertTests(strings.NewReader(eventLog(events...)), []string{"TestRequired", "TestRequired/case"})
			if (err == nil) != (outcome == "pass") {
				t.Fatalf("child outcome %q: error=%v", outcome, err)
			}
		})
	}
	log := eventLog(testEvent{"run", "p", "TestRequired"}, testEvent{"pass", "p", "TestRequired"}, testEvent{"pass", "p", ""})
	if err := assertTests(strings.NewReader(log), []string{"TestRequired/missing"}); err == nil {
		t.Fatal("omitted matrix case must fail")
	}
}

func TestCheckReport(t *testing.T) {
	if err := checkReport(strings.NewReader(""), nil); err == nil {
		t.Fatal("missing arguments must fail")
	}
	if err := checkReport(strings.NewReader(""), []string{"TestRequired"}); err == nil {
		t.Fatal("missing report must fail")
	}
	content := eventLog(testEvent{"run", "p", "TestRequired"}, testEvent{"pass", "p", "TestRequired"}, testEvent{"pass", "p", ""})
	if err := checkReport(strings.NewReader(content), []string{"TestRequired"}); err != nil {
		t.Fatal(err)
	}
	if err := checkReport(strings.NewReader(content), []string{"report.jsonl", "TestRequired"}); err == nil {
		t.Fatal("a path argument must be treated as a missing test, not a file to open")
	}
}

type brokenReportReader struct{}

func (brokenReportReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestCheckReportPreservesInputFailure(t *testing.T) {
	if err := checkReport(brokenReportReader{}, []string{"TestRequired"}); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("input failure must be preserved: %v", err)
	}
}
