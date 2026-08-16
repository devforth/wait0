package urlpersist

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"gopkg.in/yaml.v3"
)

// fileCaptureLogger is the Logger fake used by the file tests. It is
// mutex-guarded because readFile may be reached from goroutines under
// `go test -race`.
type fileCaptureLogger struct {
	mu    sync.Mutex
	lines []string
}

func (l *fileCaptureLogger) Printf(format string, v ...any) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, fmt.Sprintf(format, v...))
}

func (l *fileCaptureLogger) count() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return len(l.lines)
}

func (l *fileCaptureLogger) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.lines...)
}

func (l *fileCaptureLogger) joined() string {
	return strings.Join(l.snapshot(), "\n")
}

// fileTestOrigin is written into every header document produced by these tests.
const fileTestOrigin = "https://origin.test"

// fileTestNow is deliberately in a non-UTC zone so the header assertions prove
// generatedAt is normalized to UTC.
var fileTestNow = time.Unix(1700000000, 0).In(time.FixedZone("UTC+7", 7*3600))

func fileTestWriteRaw(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func fileTestRead(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %q: %v", path, err)
	}
	return string(raw)
}

func fileTestParseHeader(t *testing.T, raw string) fileHeader {
	t.Helper()
	first := raw
	if idx := strings.Index(raw, "\n---\n"); idx >= 0 {
		first = raw[:idx]
	}
	var h fileHeader
	if err := yaml.Unmarshal([]byte(first), &h); err != nil {
		t.Fatalf("parse header document %q: %v", first, err)
	}
	return h
}

// fileTestValidFile is a hand written, well formed file used wherever a test
// needs a readable generation without going through writeFile.
const fileTestValidFile = "version: 1\n" +
	"generatedAt: \"2023-11-14T22:13:20Z\"\n" +
	"origin: https://origin.test\n" +
	"count: 1\n" +
	"---\n" +
	"path: /from-backup\n" +
	"lastSeen: 7\n" +
	"seen: 3\n"

// fileTestCorrupt is not parseable YAML: a tab can never start a token.
const fileTestCorrupt = "\tversion: 1\n\tthis is not: [yaml\n"

func fileTestPaths(t *testing.T) (path, backup string) {
	t.Helper()
	path = filepath.Join(t.TempDir(), "urls.yaml")
	return path, backupPath(path)
}

func fileTestRecords() []Record {
	return []Record{
		{
			Path:         "/",
			LastSeen:     1700000001,
			Seen:         3,
			DiscoveredBy: "sitemap",
		},
		{
			Path:     "/blog/index.html",
			Query:    "page=2&sort=asc",
			LastSeen: 1700000002,
			Seen:     1,
			Failures: 2,
		},
		{
			// Unicode, percent escapes, emoji and characters YAML has to quote.
			Path:     "/статья/hello world/🚀?not-a-query",
			Query:    "q=%D0%BF%D1%80%D0%B8%D0%B2%D0%B5%D1%82&tag=a+b",
			LastSeen: 1700000003,
			Seen:     11,
		},
		{
			Path:     "/tricky: path #hash \"quoted\"\tand-tab",
			LastSeen: 1700000004,
			Seen:     1,
		},
		{
			Path:         "/variant",
			Query:        "lang=de",
			LastSeen:     1700000005,
			Seen:         9,
			DiscoveredBy: "user",
			Variant: &Variant{
				Fingerprint: "fp-generation-1",
				Values:      []string{"mobile", "CA"},
				Headers: map[string][]string{
					"User-Agent":      {"iPad", "Mozilla/5.0 (X11; Linux x86_64)"},
					"Cf-Ipcountry":    {"CA"},
					"Accept-Language": {"de-DE", "de;q=0.9", "en;q=0.1"},
				},
			},
		},
	}
}

func TestWriteFileReadFileRoundTrip(t *testing.T) {
	path, _ := fileTestPaths(t)
	want := fileTestRecords()

	if err := writeFile(path, fileTestOrigin, fileTestNow, want); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	log := &fileCaptureLogger{}
	got := readFile(path, "", log)
	if n := log.count(); n != 0 {
		t.Fatalf("clean read logged %d lines: %v", n, log.snapshot())
	}
	if len(got) != len(want) {
		t.Fatalf("records = %d, want %d (%+v)", len(got), len(want), got)
	}
	for i := range want {
		if !reflect.DeepEqual(got[i], want[i]) {
			t.Fatalf("record %d round trip mismatch:\n got %#v\nwant %#v", i, got[i], want[i])
		}
		if got[i].Variant != nil && want[i].Variant != nil && !reflect.DeepEqual(*got[i].Variant, *want[i].Variant) {
			t.Fatalf("record %d variant mismatch:\n got %#v\nwant %#v", i, *got[i].Variant, *want[i].Variant)
		}
		// Record identity is the cache key: it must survive the file verbatim.
		if got[i].CacheKey() != want[i].CacheKey() {
			t.Fatalf("record %d cache key = %q, want %q", i, got[i].CacheKey(), want[i].CacheKey())
		}
		if got[i].BaseCacheKey() != want[i].BaseCacheKey() {
			t.Fatalf("record %d base key = %q, want %q", i, got[i].BaseCacheKey(), want[i].BaseCacheKey())
		}
	}
}

func TestWriteFileReadFileRoundTripEmptyList(t *testing.T) {
	path, _ := fileTestPaths(t)

	if err := writeFile(path, fileTestOrigin, fileTestNow, nil); err != nil {
		t.Fatalf("writeFile: %v", err)
	}

	raw := fileTestRead(t, path)
	if strings.Contains(raw, "---") {
		t.Fatalf("header only file must not contain a document separator: %q", raw)
	}
	header := fileTestParseHeader(t, raw)
	if header.Count != 0 {
		t.Fatalf("count = %d, want 0", header.Count)
	}

	log := &fileCaptureLogger{}
	if got := readFile(path, "", log); len(got) != 0 {
		t.Fatalf("records = %+v, want none", got)
	}
	if n := log.count(); n != 0 {
		t.Fatalf("header only read logged %d lines: %v", n, log.snapshot())
	}
}

func TestWriteFileVariantHeaderAbsenceIsPreserved(t *testing.T) {
	path, _ := fileTestPaths(t)

	// The projection only carries headers the request actually sent. A header
	// the Cache-Variant expression references but the request omitted must come
	// back missing, because an empty value would change the evaluated tuple.
	records := []Record{{
		Path:     "/vary",
		LastSeen: 1700000010,
		Seen:     1,
		Variant: &Variant{
			Fingerprint: "fp",
			Values:      []string{"desktop", ""},
			Headers: map[string][]string{
				"User-Agent": {"curl/8.0"},
			},
		},
	}}

	if err := writeFile(path, fileTestOrigin, fileTestNow, records); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	raw := fileTestRead(t, path)
	if strings.Contains(raw, "Cf-Ipcountry") {
		t.Fatalf("absent header must not be written: %q", raw)
	}

	got := readFile(path, "", &fileCaptureLogger{})
	if len(got) != 1 || got[0].Variant == nil {
		t.Fatalf("records = %+v, want one variant record", got)
	}
	headers := got[0].Variant.Headers
	if _, ok := headers["Cf-Ipcountry"]; ok {
		t.Fatalf("absent header round tripped as present: %#v", headers)
	}
	if len(headers) != 1 {
		t.Fatalf("headers = %#v, want only User-Agent", headers)
	}
	if !reflect.DeepEqual(headers["User-Agent"], []string{"curl/8.0"}) {
		t.Fatalf("User-Agent = %#v", headers["User-Agent"])
	}
	// An empty values entry is still a value: it must not be dropped.
	if !reflect.DeepEqual(got[0].Variant.Values, []string{"desktop", ""}) {
		t.Fatalf("values = %#v", got[0].Variant.Values)
	}
	if got[0].CacheKey() != records[0].CacheKey() {
		t.Fatalf("cache key = %q, want %q", got[0].CacheKey(), records[0].CacheKey())
	}
}

func TestWriteFileVariantWithoutHeadersRoundTripsAsNil(t *testing.T) {
	path, _ := fileTestPaths(t)

	records := []Record{
		{Path: "/nil-headers", LastSeen: 1, Seen: 1, Variant: &Variant{Fingerprint: "fp", Values: []string{"a"}}},
		{Path: "/empty-headers", LastSeen: 1, Seen: 1, Variant: &Variant{Fingerprint: "fp", Values: []string{"a"}, Headers: map[string][]string{}}},
	}
	if err := writeFile(path, fileTestOrigin, fileTestNow, records); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	if raw := fileTestRead(t, path); strings.Contains(raw, "headers:") {
		t.Fatalf("empty header projection must be omitted: %q", raw)
	}

	got := readFile(path, "", &fileCaptureLogger{})
	if len(got) != 2 {
		t.Fatalf("records = %+v, want 2", got)
	}
	for i, rec := range got {
		if rec.Variant == nil {
			t.Fatalf("record %d lost its variant", i)
		}
		if rec.Variant.Headers != nil {
			t.Fatalf("record %d headers = %#v, want nil", i, rec.Variant.Headers)
		}
	}
}

func TestWriteFileWritesMultiDocumentStream(t *testing.T) {
	path, _ := fileTestPaths(t)
	records := fileTestRecords()

	if err := writeFile(path, fileTestOrigin, fileTestNow, records); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	raw := fileTestRead(t, path)

	if strings.HasPrefix(raw, "---") {
		t.Fatalf("stream must start with the header document, not a separator: %q", raw)
	}
	if got, want := strings.Count(raw, "\n---\n"), len(records); got != want {
		t.Fatalf("document separators = %d, want %d\n%s", got, want, raw)
	}

	header := fileTestParseHeader(t, raw)
	if header.Version != FileVersion {
		t.Fatalf("version = %d, want %d", header.Version, FileVersion)
	}
	if header.Origin != fileTestOrigin {
		t.Fatalf("origin = %q, want %q", header.Origin, fileTestOrigin)
	}
	if header.Count != len(records) {
		t.Fatalf("count = %d, want %d", header.Count, len(records))
	}
	wantGeneratedAt := fileTestNow.UTC().Format(time.RFC3339)
	if header.GeneratedAt != wantGeneratedAt {
		t.Fatalf("generatedAt = %q, want %q (UTC normalized)", header.GeneratedAt, wantGeneratedAt)
	}
	if !strings.HasSuffix(header.GeneratedAt, "Z") {
		t.Fatalf("generatedAt must be UTC: %q", header.GeneratedAt)
	}

	// Every document after the header must be exactly one record.
	docs := strings.Split(raw, "\n---\n")
	if len(docs) != len(records)+1 {
		t.Fatalf("documents = %d, want %d", len(docs), len(records)+1)
	}
	for i, doc := range docs[1:] {
		var rec Record
		if err := yaml.Unmarshal([]byte(doc), &rec); err != nil {
			t.Fatalf("document %d is not a record: %v\n%s", i+1, err, doc)
		}
		if rec.Path != records[i].Path {
			t.Fatalf("document %d path = %q, want %q", i+1, rec.Path, records[i].Path)
		}
	}
}

func TestWriteFileIsAtomicAndKeepsPreviousGeneration(t *testing.T) {
	path, backup := fileTestPaths(t)

	gen1 := []Record{{Path: "/gen1", LastSeen: 1, Seen: 1}}
	gen2 := []Record{{Path: "/gen2a", LastSeen: 2, Seen: 1}, {Path: "/gen2b", LastSeen: 3, Seen: 1}}

	if err := writeFile(path, fileTestOrigin, fileTestNow, gen1); err != nil {
		t.Fatalf("writeFile gen1: %v", err)
	}
	if _, err := os.Stat(backup); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("first write must not create a backup, stat err = %v", err)
	}

	if err := writeFile(path, fileTestOrigin, fileTestNow, gen2); err != nil {
		t.Fatalf("writeFile gen2: %v", err)
	}

	primary := readFile(path, "", &fileCaptureLogger{})
	if len(primary) != 2 || primary[0].Path != "/gen2a" || primary[1].Path != "/gen2b" {
		t.Fatalf("primary = %+v, want gen2", primary)
	}

	if _, err := os.Stat(backup); err != nil {
		t.Fatalf("backup missing after second write: %v", err)
	}
	previous := readFile(backup, "", &fileCaptureLogger{})
	if len(previous) != 1 || previous[0].Path != "/gen1" {
		t.Fatalf("backup = %+v, want gen1", previous)
	}

	// A third write rotates again, and no temporary file survives.
	gen3 := []Record{{Path: "/gen3", LastSeen: 4, Seen: 1}}
	if err := writeFile(path, fileTestOrigin, fileTestNow, gen3); err != nil {
		t.Fatalf("writeFile gen3: %v", err)
	}
	if got := readFile(backup, "", &fileCaptureLogger{}); len(got) != 2 {
		t.Fatalf("backup after third write = %+v, want gen2", got)
	}

	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temporary file left behind: %q", entry.Name())
		}
	}
	if len(entries) != 2 {
		t.Fatalf("directory entries = %d, want primary + backup: %v", len(entries), entries)
	}
}

func TestWriteFileUsesMode0600(t *testing.T) {
	path, backup := fileTestPaths(t)

	if err := writeFile(path, fileTestOrigin, fileTestNow, []Record{{Path: "/a", LastSeen: 1, Seen: 1}}); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o600 {
		t.Fatalf("mode = %04o, want 0600", perm)
	}

	if err := writeFile(path, fileTestOrigin, fileTestNow, []Record{{Path: "/b", LastSeen: 2, Seen: 1}}); err != nil {
		t.Fatalf("second writeFile: %v", err)
	}
	for _, p := range []string{path, backup} {
		info, err := os.Stat(p)
		if err != nil {
			t.Fatalf("stat %q: %v", p, err)
		}
		if perm := info.Mode().Perm(); perm != 0o600 {
			t.Fatalf("%q mode = %04o, want 0600", p, perm)
		}
	}
}

func TestWriteFileCreatesMissingParentDirectory(t *testing.T) {
	path := filepath.Join(t.TempDir(), "deeply", "nested", "state", "urls.yaml")

	if err := writeFile(path, fileTestOrigin, fileTestNow, []Record{{Path: "/a", LastSeen: 1, Seen: 1}}); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	if got := readFile(path, "", &fileCaptureLogger{}); len(got) != 1 || got[0].Path != "/a" {
		t.Fatalf("records = %+v", got)
	}
}

func TestWriteFileErrorsWhenParentIsAFile(t *testing.T) {
	dir := t.TempDir()
	blocker := filepath.Join(dir, "blocker")
	fileTestWriteRaw(t, blocker, "not a directory\n")

	err := writeFile(filepath.Join(blocker, "urls.yaml"), fileTestOrigin, fileTestNow, []Record{{Path: "/a"}})
	if err == nil {
		t.Fatal("expected an error when the parent path is a regular file")
	}
}

func TestWriteFileErrorsWhenDirectoryIsNotWritable(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root ignores directory permissions")
	}
	dir := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(dir, "urls.yaml")

	if err := writeFile(path, fileTestOrigin, fileTestNow, []Record{{Path: "/a", LastSeen: 1, Seen: 1}}); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	if err := os.Chmod(dir, 0o500); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	// Registered after t.TempDir's own cleanup, so it runs before it.
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := writeFile(path, fileTestOrigin, fileTestNow, []Record{{Path: "/b", LastSeen: 2, Seen: 1}}); err == nil {
		t.Fatal("expected an error when the temporary file cannot be created")
	}
	// The last good generation must survive a failed write untouched.
	if got := readFile(path, "", &fileCaptureLogger{}); len(got) != 1 || got[0].Path != "/a" {
		t.Fatalf("primary = %+v, want the untouched first generation", got)
	}
}

func TestWriteFileErrorsWhenBackupPathIsADirectory(t *testing.T) {
	path, backup := fileTestPaths(t)

	if err := writeFile(path, fileTestOrigin, fileTestNow, []Record{{Path: "/a", LastSeen: 1, Seen: 1}}); err != nil {
		t.Fatalf("writeFile: %v", err)
	}
	if err := os.Mkdir(backup, 0o755); err != nil {
		t.Fatalf("mkdir backup: %v", err)
	}

	if err := writeFile(path, fileTestOrigin, fileTestNow, []Record{{Path: "/b", LastSeen: 2, Seen: 1}}); err == nil {
		t.Fatal("expected an error when the previous generation cannot be rotated")
	}

	// The primary must still hold the last good generation.
	if got := readFile(path, "", &fileCaptureLogger{}); len(got) != 1 || got[0].Path != "/a" {
		t.Fatalf("primary = %+v, want the untouched first generation", got)
	}
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read dir: %v", err)
	}
	for _, entry := range entries {
		if strings.Contains(entry.Name(), ".tmp") {
			t.Fatalf("temporary file left behind after a failed write: %q", entry.Name())
		}
	}
}

func TestReadFileMissingFileReturnsNil(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does", "not", "exist.yaml")
	log := &fileCaptureLogger{}

	got := readFile(path, "", log)
	if got != nil {
		t.Fatalf("records = %+v, want nil", got)
	}
	// A first boot has no file yet, so that must stay silent.
	if n := log.count(); n != 0 {
		t.Fatalf("missing file logged %d lines: %v", n, log.snapshot())
	}

	if got := readFile(path, "", nil); got != nil {
		t.Fatalf("records with nil logger = %+v, want nil", got)
	}
}

func TestReadFileFallsBackToBackupWhenPrimaryIsCorrupt(t *testing.T) {
	path, backup := fileTestPaths(t)
	fileTestWriteRaw(t, path, fileTestCorrupt)
	fileTestWriteRaw(t, backup, fileTestValidFile)

	log := &fileCaptureLogger{}
	got := readFile(path, "", log)
	if len(got) != 1 || got[0].Path != "/from-backup" {
		t.Fatalf("records = %+v, want the backup generation", got)
	}
	if got[0].LastSeen != 7 || got[0].Seen != 3 {
		t.Fatalf("backup record fields = %+v", got[0])
	}
	if log.count() != 1 {
		t.Fatalf("log lines = %v, want exactly the primary warning", log.snapshot())
	}
	line := log.snapshot()[0]
	if !strings.Contains(line, "urlPersister") || !strings.Contains(line, "cannot read") {
		t.Fatalf("unexpected warning %q", line)
	}
	if !strings.Contains(line, path) || !strings.Contains(line, backup) {
		t.Fatalf("warning should name both files, got %q", line)
	}

	if got := readFile(path, "", nil); len(got) != 1 {
		t.Fatalf("nil logger fallback = %+v", got)
	}
}

func TestReadFileFallsBackWhenARecordDocumentIsCorrupt(t *testing.T) {
	path, backup := fileTestPaths(t)
	// The header parses, a later document does not.
	fileTestWriteRaw(t, path, "version: 1\ncount: 2\n---\npath: /ok\nseen: 1\n---\n\tbroken: [\n")
	fileTestWriteRaw(t, backup, fileTestValidFile)

	log := &fileCaptureLogger{}
	got := readFile(path, "", log)
	if len(got) != 1 || got[0].Path != "/from-backup" {
		t.Fatalf("records = %+v, want the backup generation", got)
	}
	if log.count() != 1 {
		t.Fatalf("log lines = %v", log.snapshot())
	}
}

func TestReadFileBothCorruptReturnsNil(t *testing.T) {
	path, backup := fileTestPaths(t)
	fileTestWriteRaw(t, path, fileTestCorrupt)
	fileTestWriteRaw(t, backup, "just a scalar, not a header\n")

	log := &fileCaptureLogger{}
	got := readFile(path, "", log)
	if got != nil {
		t.Fatalf("records = %+v, want nil", got)
	}
	if log.count() != 2 {
		t.Fatalf("log lines = %v, want a warning per file", log.snapshot())
	}
	if !strings.Contains(log.joined(), "empty list") {
		t.Fatalf("expected the give-up warning, got %v", log.snapshot())
	}

	// Losing both generations must never take the process down.
	if got := readFile(path, "", nil); got != nil {
		t.Fatalf("records with nil logger = %+v, want nil", got)
	}
}

func TestReadFileCorruptPrimaryWithoutBackupReturnsNil(t *testing.T) {
	path, _ := fileTestPaths(t)
	fileTestWriteRaw(t, path, fileTestCorrupt)

	log := &fileCaptureLogger{}
	if got := readFile(path, "", log); got != nil {
		t.Fatalf("records = %+v, want nil", got)
	}
	if log.count() != 2 {
		t.Fatalf("log lines = %v, want both warnings", log.snapshot())
	}
}

func TestReadFileRejectsUnsupportedVersion(t *testing.T) {
	t.Run("falls back to the backup", func(t *testing.T) {
		path, backup := fileTestPaths(t)
		fileTestWriteRaw(t, path, "version: 2\ncount: 1\n---\npath: /future\nseen: 1\n")
		fileTestWriteRaw(t, backup, fileTestValidFile)

		log := &fileCaptureLogger{}
		got := readFile(path, "", log)
		if len(got) != 1 || got[0].Path != "/from-backup" {
			t.Fatalf("records = %+v, want the backup generation", got)
		}
		if !strings.Contains(log.joined(), "unsupported version 2") {
			t.Fatalf("expected an unsupported version warning, got %v", log.snapshot())
		}
	})

	t.Run("missing version field", func(t *testing.T) {
		path, _ := fileTestPaths(t)
		fileTestWriteRaw(t, path, "origin: https://origin.test\ncount: 1\n---\npath: /a\nseen: 1\n")

		log := &fileCaptureLogger{}
		if got := readFile(path, "", log); got != nil {
			t.Fatalf("records = %+v, want nil", got)
		}
		if !strings.Contains(log.joined(), "unsupported version 0") {
			t.Fatalf("expected an unsupported version warning, got %v", log.snapshot())
		}
	})
}

func TestReadFileEmptyFileReturnsNil(t *testing.T) {
	t.Run("primary only", func(t *testing.T) {
		path, _ := fileTestPaths(t)
		fileTestWriteRaw(t, path, "")

		log := &fileCaptureLogger{}
		if got := readFile(path, "", log); got != nil {
			t.Fatalf("records = %+v, want nil", got)
		}
		if n := log.count(); n != 0 {
			t.Fatalf("empty file logged %d lines: %v", n, log.snapshot())
		}
	})

	t.Run("an empty primary is treated as an empty registry, not as damage", func(t *testing.T) {
		// writeFile only ever publishes a fully written file, so a zero byte
		// primary means "nothing was remembered" and the backup is not consulted.
		path, backup := fileTestPaths(t)
		fileTestWriteRaw(t, path, "")
		fileTestWriteRaw(t, backup, fileTestValidFile)

		log := &fileCaptureLogger{}
		if got := readFile(path, "", log); got != nil {
			t.Fatalf("records = %+v, want nil", got)
		}
		if n := log.count(); n != 0 {
			t.Fatalf("logged %d lines: %v", n, log.snapshot())
		}
	})
}

func TestReadFileSkipsRecordsWithoutAPath(t *testing.T) {
	path, _ := fileTestPaths(t)
	fileTestWriteRaw(t, path, "version: 1\n"+
		"generatedAt: \"2023-11-14T22:13:20Z\"\n"+
		"origin: https://origin.test\n"+
		"count: 4\n"+
		"---\n"+
		"path: \"\"\n"+
		"seen: 42\n"+
		"---\n"+
		"seen: 7\n"+
		"---\n"+
		"---\n"+
		"path: /kept\n"+
		"query: a=1\n"+
		"lastSeen: 5\n"+
		"seen: 2\n")

	log := &fileCaptureLogger{}
	got := readFile(path, "", log)
	if len(got) != 1 {
		t.Fatalf("records = %+v, want only the record that has a path", got)
	}
	if got[0].Path != "/kept" || got[0].Query != "a=1" || got[0].LastSeen != 5 || got[0].Seen != 2 {
		t.Fatalf("record = %+v", got[0])
	}
	if got[0].CacheKey() != "/kept?a=1" {
		t.Fatalf("cache key = %q, want /kept?a=1", got[0].CacheKey())
	}
	if n := log.count(); n != 0 {
		t.Fatalf("skipping records must not warn, got %v", log.snapshot())
	}
}

func TestReadFileHeaderOnlyFileReturnsNoRecords(t *testing.T) {
	path, _ := fileTestPaths(t)
	fileTestWriteRaw(t, path, "version: 1\ncount: 0\n")

	log := &fileCaptureLogger{}
	if got := readFile(path, "", log); len(got) != 0 {
		t.Fatalf("records = %+v, want none", got)
	}
	if n := log.count(); n != 0 {
		t.Fatalf("logged %d lines: %v", n, log.snapshot())
	}
}

func TestReadFileOnADirectoryFallsBack(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "urls.yaml")
	if err := os.Mkdir(path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fileTestWriteRaw(t, backupPath(path), fileTestValidFile)

	log := &fileCaptureLogger{}
	got := readFile(path, "", log)
	if len(got) != 1 || got[0].Path != "/from-backup" {
		t.Fatalf("records = %+v, want the backup generation", got)
	}
	if log.count() == 0 {
		t.Fatal("expected a warning when the primary cannot be read")
	}
}

func TestEncodeStreamIsDeterministic(t *testing.T) {
	records := fileTestRecords()
	// Many map keys, so a non-deterministic encoder would show up quickly.
	records = append(records, Record{
		Path:     "/many-headers",
		LastSeen: 1700000099,
		Seen:     1,
		Variant: &Variant{
			Fingerprint: "fp",
			Values:      []string{"a", "b", "c"},
			Headers: map[string][]string{
				"A-Header": {"1"}, "B-Header": {"2"}, "C-Header": {"3"},
				"D-Header": {"4"}, "E-Header": {"5"}, "F-Header": {"6"},
				"G-Header": {"7"}, "H-Header": {"8"}, "I-Header": {"9"},
			},
		},
	})

	var first bytes.Buffer
	if err := encodeStream(&first, fileTestOrigin, fileTestNow, records); err != nil {
		t.Fatalf("encodeStream: %v", err)
	}
	for i := 0; i < 16; i++ {
		var next bytes.Buffer
		if err := encodeStream(&next, fileTestOrigin, fileTestNow, records); err != nil {
			t.Fatalf("encodeStream #%d: %v", i, err)
		}
		if !bytes.Equal(first.Bytes(), next.Bytes()) {
			t.Fatalf("encoding #%d differs:\n%s\n---- vs ----\n%s", i, first.String(), next.String())
		}
	}

	// The same content written through writeFile must be byte identical too.
	pathA := filepath.Join(t.TempDir(), "urls.yaml")
	pathB := filepath.Join(t.TempDir(), "urls.yaml")
	if err := writeFile(pathA, fileTestOrigin, fileTestNow, records); err != nil {
		t.Fatalf("writeFile A: %v", err)
	}
	if err := writeFile(pathB, fileTestOrigin, fileTestNow, records); err != nil {
		t.Fatalf("writeFile B: %v", err)
	}
	if fileTestRead(t, pathA) != fileTestRead(t, pathB) {
		t.Fatal("writeFile output is not deterministic")
	}
	if fileTestRead(t, pathA) != first.String() {
		t.Fatal("writeFile output differs from encodeStream output")
	}
}

type fileTestFailWriter struct {
	afterBytes int
	written    int
}

func (w *fileTestFailWriter) Write(p []byte) (int, error) {
	if w.written >= w.afterBytes {
		return 0, errors.New("disk is on fire")
	}
	w.written += len(p)
	return len(p), nil
}

func TestEncodeStreamPropagatesWriterErrors(t *testing.T) {
	records := fileTestRecords()

	t.Run("header write fails", func(t *testing.T) {
		err := encodeStream(&fileTestFailWriter{}, fileTestOrigin, fileTestNow, records)
		if err == nil {
			t.Fatal("expected an error from the failing writer")
		}
		if !strings.Contains(err.Error(), "disk is on fire") {
			t.Fatalf("error = %v, want the writer error", err)
		}
	})

	t.Run("record write fails", func(t *testing.T) {
		if err := encodeStream(&fileTestFailWriter{afterBytes: 64}, fileTestOrigin, fileTestNow, records); err == nil {
			t.Fatal("expected an error from the failing writer")
		}
	})
}

func TestDecodeFileReturnsNotExistForMissingFiles(t *testing.T) {
	_, err := decodeFile(filepath.Join(t.TempDir(), "nope.yaml"), "")
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want os.ErrNotExist", err)
	}
}

func TestFileBackupPath(t *testing.T) {
	tests := []struct {
		in   string
		want string
	}{
		{in: "/data/urls.yaml", want: "/data/urls.yaml.bak"},
		{in: "urls.yaml", want: "urls.yaml.bak"},
		{in: "", want: ".bak"},
	}
	for _, tc := range tests {
		if got := backupPath(tc.in); got != tc.want {
			t.Fatalf("backupPath(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestSyncDirIgnoresMissingDirectories(t *testing.T) {
	// syncDir is best effort: an unopenable directory must not panic.
	syncDir(filepath.Join(t.TempDir(), "missing"))
}
