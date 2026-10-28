package adminui

import (
	"errors"
	"io"
	"net/http"

	"github.com/aethons-tools/cove/internal/kittree"
)

// uploadedFile is the multipart form's "file" part: its bytes, read up to
// kittree.MaxFile+1 so an oversize file meets jam's own 413, and whether one
// was sent. Its file name is never read: the path comes from the form.
func uploadedFile(r *http.Request) ([]byte, bool, error) {
	f, _, err := r.FormFile("file")
	if errors.Is(err, http.ErrMissingFile) {
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, kittree.MaxFile+1))
	return b, true, err
}
