package images

import (
	"fmt"
	"testing"
)

// The refusal's text is the API's error key (api_errors.go), so it is a literal;
// this keeps it in step with the limit.
func TestFileSizeMessageNamesTheLimit(t *testing.T) {
	if want := fmt.Sprintf("file too large (max %dMB)", MaxUploadBytes>>20); ErrFileSize.Error() != want {
		t.Fatalf("ErrFileSize = %q, want %q", ErrFileSize, want)
	}
}
