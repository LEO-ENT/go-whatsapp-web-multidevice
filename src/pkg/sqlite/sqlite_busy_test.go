package sqlite

import (
	"fmt"
	"testing"
)

type sqliteMethodCodeError struct{ code int }

func (e sqliteMethodCodeError) Error() string { return "sqlite method-code test error" }
func (e sqliteMethodCodeError) Code() int     { return e.code }

type sqliteFieldCodeError struct{ Code int }

func (e sqliteFieldCodeError) Error() string { return "sqlite field-code test error" }

func TestIsBusySupportsBothSQLiteDriverErrorShapes(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "modernc busy method", err: sqliteMethodCodeError{code: sqliteBusyCode}, want: true},
		{name: "modernc extended locked method", err: sqliteMethodCodeError{code: sqliteLockedCode | 1<<8}, want: true},
		{name: "go-sqlite3 busy field", err: sqliteFieldCodeError{Code: sqliteBusyCode}, want: true},
		{name: "wrapped go-sqlite3 locked field", err: fmt.Errorf("claim: %w", sqliteFieldCodeError{Code: sqliteLockedCode}), want: true},
		{name: "unrelated sqlite code", err: sqliteFieldCodeError{Code: 19}, want: false},
		{name: "non-sqlite error", err: fmt.Errorf("storage unavailable"), want: false},
		{name: "nil", err: nil, want: false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := IsBusy(tc.err); got != tc.want {
				t.Fatalf("IsBusy(%v)=%v want %v", tc.err, got, tc.want)
			}
		})
	}
}
