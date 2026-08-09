package sql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"testing"

	"ccLoad/internal/model"
)

type captureModelEntryExecutor struct {
	calls [][]any
}

func (e *captureModelEntryExecutor) ExecContext(_ context.Context, _ string, args ...any) (sql.Result, error) {
	e.calls = append(e.calls, append([]any(nil), args...))
	return driver.RowsAffected(1), nil
}

func TestSaveModelEntriesNormalizesDisabledForPostgresInt2(t *testing.T) {
	store := &SQLStore{driverName: "postgres"}
	exec := &captureModelEntryExecutor{}
	err := store.saveModelEntriesImpl(context.Background(), exec, 42, []model.ModelEntry{
		{Model: "enabled"},
		{Model: "disabled", Disabled: true},
	})
	if err != nil {
		t.Fatalf("saveModelEntriesImpl: %v", err)
	}
	if len(exec.calls) != 2 {
		t.Fatalf("ExecContext calls=%d, want delete and insert", len(exec.calls))
	}
	insertArgs := exec.calls[1]
	if len(insertArgs) != 10 {
		t.Fatalf("insert args=%d, want 10: %#v", len(insertArgs), insertArgs)
	}
	if got, ok := insertArgs[3].(int); !ok || got != 0 {
		t.Fatalf("enabled disabled arg=%#v (%T), want integer 0", insertArgs[3], insertArgs[3])
	}
	if got, ok := insertArgs[8].(int); !ok || got != 1 {
		t.Fatalf("disabled disabled arg=%#v (%T), want integer 1", insertArgs[8], insertArgs[8])
	}
}
