package lock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseLockFieldsRetriesMandatoryReclaimLock(t *testing.T) {
	path := filepath.Join(t.TempDir(), "metadata.lock")
	if err := os.WriteFile(path, []byte("pid=123\nop=fixture\n"), 0600); err != nil {
		t.Fatal(err)
	}
	f, err := openReclaim(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err := lockReclaim(f); err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		time.Sleep(10 * time.Millisecond)
		f.Close() // release the mandatory range lock
	}()
	fields, err := parseLockFields(path)
	<-done
	if err != nil || fields["op"] != "fixture" {
		t.Fatalf("metadata read during reclamation: fields=%v err=%v", fields, err)
	}
}
