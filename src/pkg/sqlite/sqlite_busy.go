package sqlite

import (
	"errors"
	"reflect"
)

const (
	sqliteBusyCode   = 5
	sqliteLockedCode = 6
)

// IsBusy reports SQLite's transient writer/table contention without relying on
// driver-specific error strings. modernc exposes Code() while go-sqlite3 uses
// an exported Code field; reflecting only that exported integer keeps the
// classifier available in pure-Go, CGO, and go-sqlite3's !cgo stub builds.
func IsBusy(err error) bool {
	for current := err; current != nil; current = errors.Unwrap(current) {
		if coder, ok := current.(interface{ Code() int }); ok {
			if isSQLiteBusyCode(coder.Code()) {
				return true
			}
		}
		value := reflect.ValueOf(current)
		if value.Kind() == reflect.Pointer {
			if value.IsNil() {
				continue
			}
			value = value.Elem()
		}
		if value.Kind() != reflect.Struct {
			continue
		}
		code := value.FieldByName("Code")
		if code.IsValid() && code.CanInt() && isSQLiteBusyCode(int(code.Int())) {
			return true
		}
	}
	return false
}

func isSQLiteBusyCode(code int) bool {
	primary := code & 0xff
	return primary == sqliteBusyCode || primary == sqliteLockedCode
}
