package db

import (
	"context"
	"errors"
	"fmt"
	"net"
	"syscall"
	"testing"

	"github.com/go-sql-driver/mysql"
)

func TestClassify(t *testing.T) {
	cases := []struct {
		name  string
		err   error
		tag   string
		retry bool
	}{
		{"read-only old primary", &mysql.MySQLError{Number: 1290, Message: "--super-read-only"}, "1290", true},
		{"GR rollback", &mysql.MySQLError{Number: 3101}, "3101", true},
		{"server shutdown 08S01", &mysql.MySQLError{Number: 1053, SQLState: [5]byte{'0', '8', 'S', '0', '1'}}, "1053", true},
		{"access denied", &mysql.MySQLError{Number: 1045}, "1045", false},
		{"table missing", fmt.Errorf("wrapped: %w", &mysql.MySQLError{Number: 1146}), "1146", false},
		{"connection lost", fmt.Errorf("query: %w", mysql.ErrInvalidConn), "2013", true},
		{"connect refused", &net.OpError{Op: "dial", Net: "tcp", Err: syscall.ECONNREFUSED}, "2003", true},
		{"deadline", context.DeadlineExceeded, "timeout", true},
		{"shutdown", context.Canceled, "canceled", false},
		{"unknown", errors.New("boom"), "other", false},
	}
	for _, c := range cases {
		tag, retry := Classify(c.err)
		if tag != c.tag || retry != c.retry {
			t.Errorf("%s: got (%s, %v), want (%s, %v)", c.name, tag, retry, c.tag, c.retry)
		}
	}
}

func TestIsDuplicateKey(t *testing.T) {
	if !IsDuplicateKey(fmt.Errorf("x: %w", &mysql.MySQLError{Number: 1062})) {
		t.Error("1062 must be a duplicate key")
	}
	if IsDuplicateKey(&mysql.MySQLError{Number: 1290}) {
		t.Error("1290 is not a duplicate key")
	}
}
