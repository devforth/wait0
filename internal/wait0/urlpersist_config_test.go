package wait0

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func urlPersistBoolPtr(v bool) *bool { return &v }

func urlPersistIntPtr(v int) *int { return &v }

// A disabled persister is never validated: the block may hold anything, because
// nothing in it is ever read.
func TestURLPersisterConfig_DisabledSkipsValidation(t *testing.T) {
	cfg := URLPersisterConfig{
		Enabled:    false,
		File:       "./data/leveldb/urls.yaml",
		FlushEvery: "garbage",
		MaxURLs:    -7,
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		t.Fatalf("applyDefaultsAndValidate() = %v, want nil for a disabled persister", err)
	}
	if cfg.File != "./data/leveldb/urls.yaml" {
		t.Fatalf("file = %q, want the value left untouched", cfg.File)
	}
	if cfg.FlushEvery != "garbage" || cfg.flushEveryDur != 0 {
		t.Fatalf("flushEvery = %q / %s, want the value left untouched and uncompiled", cfg.FlushEvery, cfg.flushEveryDur)
	}
	if cfg.RestoreOnStart != nil {
		t.Fatalf("restoreOnStart = %v, want nil", *cfg.RestoreOnStart)
	}
	if cfg.ForgetAfterFailures != nil {
		t.Fatalf("forgetAfterFailures = %v, want nil", *cfg.ForgetAfterFailures)
	}
	if cfg.MaxURLs != -7 {
		t.Fatalf("maxUrls = %d, want -7 left untouched", cfg.MaxURLs)
	}
}

func TestURLPersisterConfig_AppliesDefaults(t *testing.T) {
	cfg := URLPersisterConfig{Enabled: true}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		t.Fatalf("applyDefaultsAndValidate: %v", err)
	}

	if cfg.File != "./data/urls.yaml" {
		t.Fatalf("file = %q, want ./data/urls.yaml", cfg.File)
	}
	if cfg.FlushEvery != "30s" {
		t.Fatalf("flushEvery = %q, want 30s", cfg.FlushEvery)
	}
	if cfg.flushEveryDur != 30*time.Second {
		t.Fatalf("flushEveryDur = %s, want 30s", cfg.flushEveryDur)
	}
	if cfg.RestoreOnStart == nil || !*cfg.RestoreOnStart {
		t.Fatalf("restoreOnStart = %v, want a pointer to true", cfg.RestoreOnStart)
	}
	if cfg.MaxURLs != 50000 {
		t.Fatalf("maxUrls = %d, want 50000", cfg.MaxURLs)
	}
	if cfg.ForgetAfterFailures == nil || *cfg.ForgetAfterFailures != 3 {
		t.Fatalf("forgetAfterFailures = %v, want a pointer to 3", cfg.ForgetAfterFailures)
	}
}

// restoreOnStart and forgetAfterFailures are pointers precisely so that an
// explicit false/0 is distinguishable from an omitted field.
func TestURLPersisterConfig_KeepsExplicitFalseAndZero(t *testing.T) {
	cfg := URLPersisterConfig{
		Enabled:             true,
		File:                "  ./var/lib/urls.yaml  ",
		FlushEvery:          "  1m  ",
		RestoreOnStart:      urlPersistBoolPtr(false),
		MaxURLs:             7,
		ForgetAfterFailures: urlPersistIntPtr(0),
	}
	if err := cfg.applyDefaultsAndValidate(); err != nil {
		t.Fatalf("applyDefaultsAndValidate: %v", err)
	}

	if cfg.RestoreOnStart == nil || *cfg.RestoreOnStart {
		t.Fatalf("restoreOnStart = %v, want an explicit false to survive", cfg.RestoreOnStart)
	}
	if cfg.ForgetAfterFailures == nil || *cfg.ForgetAfterFailures != 0 {
		t.Fatalf("forgetAfterFailures = %v, want an explicit 0 to survive", cfg.ForgetAfterFailures)
	}
	if cfg.File != "./var/lib/urls.yaml" {
		t.Fatalf("file = %q, want it trimmed", cfg.File)
	}
	if cfg.FlushEvery != "1m" || cfg.flushEveryDur != time.Minute {
		t.Fatalf("flushEvery = %q / %s, want 1m trimmed and compiled", cfg.FlushEvery, cfg.flushEveryDur)
	}
	if cfg.MaxURLs != 7 {
		t.Fatalf("maxUrls = %d, want 7", cfg.MaxURLs)
	}
}

func TestURLPersisterConfig_ValidationErrors(t *testing.T) {
	tests := []struct {
		name string
		cfg  URLPersisterConfig
		want string
	}{
		{
			name: "file inside the leveldb directory",
			cfg:  URLPersisterConfig{Enabled: true, File: "./data/leveldb/urls.yaml"},
			want: `file: must not be inside the disk cache directory "./data/leveldb", which is deleted on start`,
		},
		{
			name: "file is the leveldb directory itself",
			cfg:  URLPersisterConfig{Enabled: true, File: "data/leveldb"},
			want: `file: must not be inside the disk cache directory "./data/leveldb", which is deleted on start`,
		},
		{
			name: "unparseable flushEvery",
			cfg:  URLPersisterConfig{Enabled: true, FlushEvery: "soon"},
			want: `flushEvery: time: invalid duration "soon"`,
		},
		{
			name: "zero flushEvery",
			cfg:  URLPersisterConfig{Enabled: true, FlushEvery: "0s"},
			want: "flushEvery: must be > 0",
		},
		{
			name: "negative flushEvery",
			cfg:  URLPersisterConfig{Enabled: true, FlushEvery: "-5s"},
			want: "flushEvery: must be > 0",
		},
		{
			name: "negative maxUrls",
			cfg:  URLPersisterConfig{Enabled: true, MaxURLs: -1},
			want: "maxUrls: must be > 0",
		},
		{
			name: "negative forgetAfterFailures",
			cfg:  URLPersisterConfig{Enabled: true, ForgetAfterFailures: urlPersistIntPtr(-1)},
			want: "forgetAfterFailures: must be >= 0",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfg := tc.cfg
			err := cfg.applyDefaultsAndValidate()
			if err == nil {
				t.Fatalf("applyDefaultsAndValidate() = nil, want %q", tc.want)
			}
			if err.Error() != tc.want {
				t.Fatalf("error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}

func TestWithinDir_DiskCacheDirectory(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{path: "./data/leveldb/x.yaml", want: true},
		{path: "data/leveldb/nested/deep.yaml", want: true},
		{path: "./data/leveldb", want: true},
		{path: "./data/urls.yaml", want: false},
		{path: "./data/leveldb/../urls.yaml", want: false},
		{path: "./data/leveldbextra/f", want: false},
		{path: "/etc/wait0/urls.yaml", want: false},
	}

	for _, tc := range tests {
		if got := withinDir(tc.path, diskCacheDir); got != tc.want {
			t.Fatalf("withinDir(%q, %q) = %v, want %v", tc.path, diskCacheDir, got, tc.want)
		}
	}
}

func TestLoadConfig_URLPersisterBlock(t *testing.T) {
	t.Run("explicit values", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "wait0.yaml")
		yaml := `storage:
  ram: {max: "1m"}
  disk: {max: "1m"}
server:
  origin: "http://x"
urlPersister:
  enabled: true
  file: "./data/remembered.yaml"
  flushEvery: "5s"
  restoreOnStart: false
  maxUrls: 12
  forgetAfterFailures: 0
rules: []
`
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		cfg, err := LoadConfig(cfgPath)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		up := cfg.URLPersister
		if !up.Enabled {
			t.Fatalf("enabled = false, want true")
		}
		if up.File != "./data/remembered.yaml" {
			t.Fatalf("file = %q", up.File)
		}
		if up.flushEveryDur != 5*time.Second {
			t.Fatalf("flushEveryDur = %s, want 5s", up.flushEveryDur)
		}
		if up.RestoreOnStart == nil || *up.RestoreOnStart {
			t.Fatalf("restoreOnStart = %v, want an explicit false", up.RestoreOnStart)
		}
		if up.MaxURLs != 12 {
			t.Fatalf("maxUrls = %d, want 12", up.MaxURLs)
		}
		if up.ForgetAfterFailures == nil || *up.ForgetAfterFailures != 0 {
			t.Fatalf("forgetAfterFailures = %v, want an explicit 0", up.ForgetAfterFailures)
		}
	})

	t.Run("defaults", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "wait0.yaml")
		yaml := `storage:
  ram: {max: "1m"}
  disk: {max: "1m"}
server:
  origin: "http://x"
urlPersister:
  enabled: true
rules: []
`
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		cfg, err := LoadConfig(cfgPath)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		up := cfg.URLPersister
		if up.File != "./data/urls.yaml" || up.flushEveryDur != 30*time.Second || up.MaxURLs != 50000 {
			t.Fatalf("defaults not applied: %+v", up)
		}
		if up.RestoreOnStart == nil || !*up.RestoreOnStart {
			t.Fatalf("restoreOnStart = %v, want a pointer to true", up.RestoreOnStart)
		}
		if up.ForgetAfterFailures == nil || *up.ForgetAfterFailures != 3 {
			t.Fatalf("forgetAfterFailures = %v, want a pointer to 3", up.ForgetAfterFailures)
		}
	})

	t.Run("omitted block stays disabled", func(t *testing.T) {
		cfgPath := filepath.Join(t.TempDir(), "wait0.yaml")
		yaml := `storage:
  ram: {max: "1m"}
  disk: {max: "1m"}
server:
  origin: "http://x"
rules: []
`
		if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
			t.Fatalf("WriteFile: %v", err)
		}

		cfg, err := LoadConfig(cfgPath)
		if err != nil {
			t.Fatalf("LoadConfig: %v", err)
		}
		if cfg.URLPersister.Enabled || cfg.URLPersister.File != "" || cfg.URLPersister.ForgetAfterFailures != nil {
			t.Fatalf("omitted block was touched: %+v", cfg.URLPersister)
		}
	})
}

func TestLoadConfig_URLPersisterErrorsArePrefixed(t *testing.T) {
	tests := []struct {
		name  string
		block string
		want  string
	}{
		{
			name:  "bad flushEvery",
			block: "urlPersister:\n  enabled: true\n  flushEvery: \"soon\"\n",
			want:  `urlPersister.flushEvery: time: invalid duration "soon"`,
		},
		{
			name:  "file in the wiped directory",
			block: "urlPersister:\n  enabled: true\n  file: \"./data/leveldb/urls.yaml\"\n",
			want:  `urlPersister.file: must not be inside the disk cache directory "./data/leveldb", which is deleted on start`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cfgPath := filepath.Join(t.TempDir(), "wait0.yaml")
			yaml := "storage:\n  ram: {max: \"1m\"}\n  disk: {max: \"1m\"}\nserver:\n  origin: \"http://x\"\n" + tc.block + "rules: []\n"
			if err := os.WriteFile(cfgPath, []byte(yaml), 0o644); err != nil {
				t.Fatalf("WriteFile: %v", err)
			}

			_, err := LoadConfig(cfgPath)
			if err == nil {
				t.Fatalf("LoadConfig() = nil error, want %q", tc.want)
			}
			if !strings.HasPrefix(err.Error(), "urlPersister.") {
				t.Fatalf("error = %q, want the urlPersister. prefix", err.Error())
			}
			if err.Error() != tc.want {
				t.Fatalf("error = %q, want %q", err.Error(), tc.want)
			}
		})
	}
}
