package config

import (
	"strings"
	"testing"
	"time"
)

func TestHistoryConfigParses(t *testing.T) {
	for name, data := range map[string]string{
		"legacy": "port: 8317\nhistory:\n  enabled: true\n  interval: 10m\n  db-path: /data/history.db\n  key: secret\n",
		"v8":     "config-version: 8\nserver:\n  port: 8317\nhistory:\n  enabled: true\n  interval: 10m\n  db-path: /data/history.db\n  key: secret\n",
	} {
		t.Run(name, func(t *testing.T) {
			cfg, errParse := ParseConfigBytes([]byte(data))
			if errParse != nil {
				t.Fatalf("ParseConfigBytes: %v", errParse)
			}
			want := HistoryConfig{Enabled: true, Interval: 10 * time.Minute, DBPath: "/data/history.db", Key: "secret"}
			if cfg.History != want {
				t.Fatalf("History = %+v, want %+v", cfg.History, want)
			}
		})
	}
}

func TestHistoryConfigIsAKnownV8Section(t *testing.T) {
	data := []byte("config-version: 8\nserver:\n  port: 8317\nhistory:\n  enabled: true\n  db-path: /data/history.db\n  key: secret\n")
	if errValidate := ValidateV8Config(data); errValidate != nil {
		t.Fatalf("ValidateV8Config: %v", errValidate)
	}
	migrated, _, errNormalize := NormalizeConfigLayout([]byte("port: 8317\nhistory:\n  enabled: true\n  key: secret\n"), true)
	if errNormalize != nil {
		t.Fatalf("NormalizeConfigLayout: %v", errNormalize)
	}
	if !strings.Contains("\n"+string(migrated), "\nhistory:\n") || strings.Contains(string(migrated), "# history") {
		t.Fatalf("migration did not keep the history block:\n%s", migrated)
	}
}
