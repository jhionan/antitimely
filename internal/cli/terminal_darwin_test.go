package cli

import (
	"os"
	"testing"
)

func TestEnterCbreakFdRejectsNonTTY(t *testing.T) {
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	defer w.Close()

	if st, err := enterCbreakFd(int(r.Fd())); err == nil {
		st.restore()
		t.Fatal("expected error entering cbreak on a non-tty pipe, got nil")
	}
}
