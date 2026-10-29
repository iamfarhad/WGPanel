package events

import (
	"testing"
	"time"
)

func TestEmitNeverBlocksWhenFull(t *testing.T) {
	b := NewBus(2, nil)
	done := make(chan struct{})
	go func() {
		for i := 0; i < 100; i++ {
			b.Emit(NodeDown, "", nil)
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Emit blocked on a full queue")
	}
	if len(b.C()) != 2 {
		t.Fatalf("queue len = %d, want 2", len(b.C()))
	}
}

func TestNilBusEmitIsNoop(t *testing.T) {
	var b *Bus
	b.Emit(NodeDown, "", nil)
}

func TestCatalog(t *testing.T) {
	if !Valid(AccountQuota80) || Valid("nope") || Valid(Ping) {
		t.Fatal("catalog validity wrong")
	}
	m, _ := Lookup(BackupFailed)
	if m.Severity != SeverityCritical {
		t.Fatal("backup.failed should be critical")
	}
	if len(All()) != len(catalog) {
		t.Fatal("All mismatch")
	}
}
