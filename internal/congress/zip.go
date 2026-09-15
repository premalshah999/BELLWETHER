package congress

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
)

// newZipXML extracts one named file from a ZIP archive held in memory.
func newZipXML(body []byte, filename string) ([]byte, error) {
	zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		return nil, fmt.Errorf("congress: open zip: %w", err)
	}
	for _, f := range zr.File {
		if f.Name != filename {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("congress: open %s in zip: %w", filename, err)
		}
		defer rc.Close()
		return io.ReadAll(rc)
	}
	return nil, fmt.Errorf("congress: %s not found in zip", filename)
}
