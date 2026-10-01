package browser

import (
	"crypto/md5" //nolint:gosec // assert_file_download compares against a user-supplied MD5; not used for security
	"encoding/hex"
	"io"
	"os"
)

// fileSizeAndMD5 measures a downloaded file for assert_file_download.
func fileSizeAndMD5(path string) (int64, string) {
	f, err := os.Open(path)
	if err != nil {
		return 0, ""
	}
	defer f.Close()
	h := md5.New() //nolint:gosec // see above
	n, err := io.Copy(h, f)
	if err != nil {
		return 0, ""
	}
	return n, hex.EncodeToString(h.Sum(nil))
}
