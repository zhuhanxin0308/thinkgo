package db_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	frameworkcontext "github.com/zhuhanxin0308/thinkgo/v3/context"
	"github.com/zhuhanxin0308/thinkgo/v3/db"
)

type publicErrorDriverFailure struct{ secret string }

func (e *publicErrorDriverFailure) Error() string    { return "driver error: " + e.secret }
func (e *publicErrorDriverFailure) SQLState() string { return "23505" }

// Every hook panics to prove the presentation conversion neither examines the
// cause nor executes third-party formatting or error-tree traversal code.
type publicErrorTrap struct{}

func (*publicErrorTrap) Error() string                { panic("Error must not be called") }
func (*publicErrorTrap) Unwrap() error                { panic("Unwrap must not be called") }
func (*publicErrorTrap) Is(error) bool                { panic("Is must not be called") }
func (*publicErrorTrap) As(any) bool                  { panic("As must not be called") }
func (*publicErrorTrap) Format(fmt.State, rune)       { panic("Format must not be called") }
func (*publicErrorTrap) MarshalJSON() ([]byte, error) { panic("MarshalJSON must not be called") }

type publicErrorSlice []string

func (publicErrorSlice) Error() string { panic("slice Error must not be called") }

func TestIssue34SanitizeErrorHasOnePublicContract(t *testing.T) {
	if db.SanitizeError(nil) != nil {
		t.Fatal("nil must remain nil")
	}
	driver := &publicErrorDriverFailure{secret: "private_users token=secret"}
	partial := &db.PartialWriteError{
		Result: db.InsertResult{Affected: 1, ID: "private-id", IDKnown: true},
		Cause:  driver,
	}
	inputs := []error{driver, fmt.Errorf("insert into private_users: %w", driver),
		errors.Join(driver, context.DeadlineExceeded), context.Canceled,
		db.ErrInvalidQuery, db.ErrDatabaseUnavailable, db.ErrInsertIDUnavailable,
		partial, db.ErrDatabaseOperationFailed}
	for index, raw := range inputs {
		safe := db.SanitizeError(raw)
		if safe != db.ErrDatabaseOperationFailed || safe.Error() != "数据库操作失败" {
			t.Fatalf("case %d: unexpected public result", index)
		}
		if errors.Unwrap(safe) != nil || errors.Is(safe, driver) || errors.Is(safe, db.ErrPartialWrite) {
			t.Fatalf("case %d: original error reachable from public result", index)
		}
		var exposed *publicErrorDriverFailure
		if errors.As(safe, &exposed) {
			t.Fatalf("case %d: driver error escaped", index)
		}
		if db.SanitizeError(safe) != safe {
			t.Fatal("sanitizing an already public result must be idempotent")
		}
	}
	if !errors.Is(partial, db.ErrPartialWrite) || !errors.Is(partial, driver) || partial.Result.ID != "private-id" {
		t.Fatal("original partial-write reconciliation data changed")
	}
}

func TestIssue34SanitizeErrorNeverInvokesErrorHooks(t *testing.T) {
	var typedNil *publicErrorTrap
	for _, raw := range []error{typedNil, &publicErrorTrap{}, publicErrorSlice{"private"}} {
		safe := db.SanitizeError(raw)
		if safe != db.ErrDatabaseOperationFailed || errors.Unwrap(safe) != nil {
			t.Fatal("non-nil error interface must become opaque")
		}
		for _, format := range []string{"%s", "%v", "%+v", "%#v"} {
			if strings.Contains(fmt.Sprintf(format, safe), "private") {
				t.Fatal("public formatting retained input")
			}
		}
		if _, err := json.Marshal(safe); err != nil {
			t.Fatal(err)
		}
	}
}

// The stub implements only the exercised operation; an unexpected operation
// calls the nil embedded Connection and fails rather than silently succeeding.
type publicErrorConnection struct {
	db.Connection
	id    db.ConnectionID
	err   error
	calls int
}

func (c *publicErrorConnection) ConnectionID() db.ConnectionID { return c.id }
func (c *publicErrorConnection) Close() error                  { return nil }
func (c *publicErrorConnection) Insert(context.Context, db.InsertRequest) (db.InsertResult, error) {
	c.calls++
	return db.InsertResult{}, c.err
}

func TestIssue34PublicMappingPreservesInternalDriverError(t *testing.T) {
	driver := &publicErrorDriverFailure{secret: "private_users api_key=secret"}
	connection := &publicErrorConnection{id: db.NewConnectionID("issue34"), err: driver}
	database := db.NewDB(connection)
	t.Cleanup(func() { _ = database.Close() })
	affected, raw := database.Name("users").Insert(map[string]any{"name": "Ada"})
	if affected != 0 || raw != driver || connection.calls != 1 {
		t.Fatalf("database contract changed: affected=%d same_error=%v calls=%d", affected, raw == driver, connection.calls)
	}
	var original *publicErrorDriverFailure
	if !errors.As(raw, &original) || original != driver || !errors.Is(raw, driver) {
		t.Fatal("internal driver identity lost")
	}
	if db.SanitizeError(raw) != db.ErrDatabaseOperationFailed {
		t.Fatal("presentation conversion failed")
	}
	if connection.calls != 1 || original.secret != "private_users api_key=secret" || raw != driver {
		t.Fatal("presentation conversion retried or mutated original error")
	}
}

func TestIssue34PublicErrorResponseDoesNotExposeDriverDetails(t *testing.T) {
	var baseline string
	for _, secret := range []string{
		"SQLSTATE 23505 constraint users_email_key customer@example.test",
		"mysql://root:secret@internal-db/private_db SELECT * FROM private_users",
		"syntax error near 'private_token'\r\nX-Secret: password",
	} {
		raw := &publicErrorDriverFailure{secret: secret}
		public := db.SanitizeError(raw)
		response := frameworkcontext.NewResponse().Code(http.StatusInternalServerError).Json(map[string]string{
			"code": "database_error", "message": public.Error(),
		})
		recorder := httptest.NewRecorder()
		if err := response.Send(recorder); err != nil {
			t.Fatal(err)
		}
		if recorder.Code != http.StatusInternalServerError || !strings.HasPrefix(recorder.Header().Get("Content-Type"), "application/json") {
			t.Fatal("expected explicit JSON failure response")
		}
		var payload map[string]string
		if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		if len(payload) != 2 || payload["code"] != "database_error" || payload["message"] != "数据库操作失败" {
			t.Fatalf("unexpected public response: %q", recorder.Body.String())
		}
		if baseline == "" {
			baseline = recorder.Body.String()
		} else if recorder.Body.String() != baseline {
			t.Fatal("different driver details created different public responses")
		}
	}
}

func TestIssue34SanitizeErrorConcurrent(t *testing.T) {
	raw := &publicErrorTrap{}
	var wg sync.WaitGroup
	for range 16 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				if db.SanitizeError(raw) != db.ErrDatabaseOperationFailed || db.SanitizeError(nil) != nil {
					t.Error("inconsistent concurrent conversion")
					return
				}
			}
		}()
	}
	wg.Wait()
}

func FuzzSanitizeErrorDoesNotRetainText(f *testing.F) {
	for _, seed := range []string{"", "password=secret", "SELECT * FROM private_users", "\x00\xff\r\n"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, text string) {
		result := db.SanitizeError(errors.New(text))
		if result != db.ErrDatabaseOperationFailed || result.Error() != "数据库操作失败" || errors.Unwrap(result) != nil {
			t.Fatal("arbitrary driver text changed the public result")
		}
	})
}
