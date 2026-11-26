package atomicfile

import (
	"errors"
	"golang.org/x/sys/windows"
	"io"
	"os"
	"time"
)

// ReadFile shares deletion so an atomic namespace replacement does not block
// readers or force writers into a delete/copy fallback. Transient sharing
// conflicts are retried without ever accepting partial content.
func ReadFile(path string) ([]byte, error) {
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(time.Second)
	for {
		h, e := windows.CreateFile(p, windows.GENERIC_READ, windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE, nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
		if e == nil {
			f := os.NewFile(uintptr(h), path)
			defer f.Close()
			return io.ReadAll(f)
		}
		if (!errors.Is(e, windows.ERROR_SHARING_VIOLATION) && !errors.Is(e, windows.ERROR_ACCESS_DENIED)) || !time.Now().Before(deadline) {
			return nil, &os.PathError{Op: "open", Path: path, Err: e}
		}
		time.Sleep(time.Millisecond)
	}
}
