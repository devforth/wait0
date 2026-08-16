package urlpersist

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// fileHeader is the first YAML document in the persisted file. Every document
// after it is a single Record, which keeps both encoding and decoding
// streaming: neither side ever holds more than one record of YAML nodes.
type fileHeader struct {
	Version     int    `yaml:"version"`
	GeneratedAt string `yaml:"generatedAt"`
	Origin      string `yaml:"origin"`
	Count       int    `yaml:"count"`
}

// maxCountHint bounds how much the file's own record count may pre-allocate.
const maxCountHint = 1 << 20

func backupPath(path string) string { return path + ".bak" }

// writeFile replaces path atomically. The temporary file is created in the
// target's own directory because os.Rename is only atomic within a filesystem.
func writeFile(path, origin string, now time.Time, records []Record) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}

	tmp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() {
		tmp.Close()
		os.Remove(tmpName)
	}()

	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := encodeStream(tmp, origin, now, records); err != nil {
		return err
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}

	// Keep the previous generation so a torn or truncated primary is
	// recoverable. A missing primary on first write is not an error, but
	// anything that is not a regular file is: renaming a misconfigured
	// directory out of the way would quietly move whatever it contains.
	switch info, err := os.Stat(path); {
	case err == nil && !info.Mode().IsRegular():
		return fmt.Errorf("%s exists and is not a regular file", path)
	case err == nil:
		if err := os.Rename(path, backupPath(path)); err != nil {
			return err
		}
	case !errors.Is(err, os.ErrNotExist):
		return err
	}

	if err := os.Rename(tmpName, path); err != nil {
		return err
	}
	syncDir(dir)
	return nil
}

func encodeStream(w io.Writer, origin string, now time.Time, records []Record) error {
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	header := fileHeader{
		Version:     FileVersion,
		GeneratedAt: now.UTC().Format(time.RFC3339),
		Origin:      origin,
		Count:       len(records),
	}
	if err := enc.Encode(header); err != nil {
		return err
	}
	for i := range records {
		if err := enc.Encode(records[i]); err != nil {
			return err
		}
	}
	return enc.Close()
}

// readFile loads the registry, falling back to the backup when the primary
// cannot be parsed. A file that cannot be read at all yields no records and no
// error: losing the list must never stop wait0 from starting.
func readFile(path, origin string, logger Logger) []Record {
	records, err := decodeFile(path, origin)
	if err == nil {
		return records
	}
	if !errors.Is(err, os.ErrNotExist) && logger != nil {
		logger.Printf("urlPersister: cannot read %q (%v), trying %q", path, err, backupPath(path))
	}

	records, backupErr := decodeFile(backupPath(path), origin)
	if backupErr == nil {
		return records
	}
	// Stay silent only when neither file exists, which is a normal first boot.
	// If either one existed and was unusable, say so.
	if logger != nil && (!errors.Is(err, os.ErrNotExist) || !errors.Is(backupErr, os.ErrNotExist)) {
		logger.Printf("urlPersister: cannot read %q either (%v), starting with an empty list", backupPath(path), backupErr)
	}
	return nil
}

func decodeFile(path, origin string) ([]Record, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	dec := yaml.NewDecoder(f)

	var header fileHeader
	if err := dec.Decode(&header); err != nil {
		if errors.Is(err, io.EOF) {
			return nil, nil
		}
		return nil, err
	}
	if header.Version != FileVersion {
		return nil, fmt.Errorf("unsupported version %d, want %d", header.Version, FileVersion)
	}
	// A file written against a different origin describes URLs this instance
	// has never served. Restoring them would seed the cache with paths that do
	// not exist here and warm them against the wrong backend.
	if origin != "" && header.Origin != "" && header.Origin != origin {
		return nil, fmt.Errorf("origin %q does not match configured origin %q", header.Origin, origin)
	}

	// Count is a hint from an untrusted file, so it sizes the slice but never
	// dictates the allocation: a negative value panics make, a huge one is an
	// out-of-memory vector.
	capacity := header.Count
	if capacity < 0 || capacity > maxCountHint {
		capacity = 0
	}
	out := make([]Record, 0, capacity)
	for {
		var rec Record
		err := dec.Decode(&rec)
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		if rec.Path == "" {
			continue
		}
		out = append(out, rec)
	}
}

func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	defer d.Close()
	_ = d.Sync()
}
