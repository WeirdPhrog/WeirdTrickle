//go:build testing

package iptables

import (
	"errors"
	"testing"
)

type contendedExecutable struct {
	*FakeIPTables
	saves, restores int
	failures        int
	failure         error
}

func (e *contendedExecutable) Save() ([]byte, error) {
	e.saves++
	return e.FakeIPTables.Save()
}

func (e *contendedExecutable) Restore(data []byte) error {
	e.restores++
	if e.restores <= e.failures {
		// NDM changed a foreign rule during the conflicting transaction.
		e.SetInitialRules("mangle", "PREROUTING", [][]string{{"-j", "VENDOR"}})
		return e.failure
	}
	return e.FakeIPTables.Restore(data)
}

func TestCommitRetriesLockConflictFromFreshSnapshot(t *testing.T) {
	e := &contendedExecutable{FakeIPTables: NewFakeIPTables(ProtocolIPv4), failures: 1,
		failure: &commandFailure{"iptables-restore", errors.New("exit 4"), "Another app is currently holding the xtables lock"}}
	e.SetInitialRules("mangle", "PREROUTING", nil)
	ipt := NewIPTables(e)
	if err := ipt.RegisterChainPatch("mangle", "PREROUTING"); err != nil {
		t.Fatal(err)
	}
	if err := ipt.Append("mangle", "PREROUTING", "-j", "MT_ROUTE"); err != nil {
		t.Fatal(err)
	}
	if err := ipt.Commit(); err != nil {
		t.Fatal(err)
	}
	if e.saves != 2 || e.restores != 2 {
		t.Fatalf("saves=%d restores=%d", e.saves, e.restores)
	}
	rules := e.GetRules("mangle", "PREROUTING")
	if len(rules) != 2 || rules[0][1] != "VENDOR" || rules[1][1] != "MT_ROUTE" {
		t.Fatalf("foreign rule lost: %v", rules)
	}
}

func TestCommitRetryIsBoundedAndDoesNotRetrySyntaxErrors(t *testing.T) {
	for _, tc := range []struct {
		message string
		calls   int
	}{
		{"Resource temporarily unavailable", 3}, {"line 8 failed: invalid target", 1},
	} {
		e := &contendedExecutable{FakeIPTables: NewFakeIPTables(ProtocolIPv4), failures: 10,
			failure: &commandFailure{"iptables-restore", errors.New("exit 4"), tc.message}}
		e.SetInitialRules("mangle", "PREROUTING", nil)
		ipt := NewIPTables(e)
		if err := ipt.RegisterChainOverride("mangle", "MT_ROUTE"); err != nil {
			t.Fatal(err)
		}
		if err := ipt.Append("mangle", "MT_ROUTE", "-j", "RETURN"); err != nil {
			t.Fatal(err)
		}
		if err := ipt.Commit(); err == nil || e.restores != tc.calls {
			t.Fatalf("error=%v calls=%d", err, e.restores)
		}
	}
}
