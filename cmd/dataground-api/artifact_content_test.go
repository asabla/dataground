package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestArtifactContentConfigurationIsExplicitBoundedAndLoopback(t *testing.T) {
	valid := `{"contract":"dataground.api-artifact-content/v1","endpoint":"http://127.0.0.1:58333","bucket":"artifacts","maximumBytes":1048576}`
	lookup := func(path string) func(string) (string, bool) {
		return func(key string) (string, bool) {
			switch key {
			case artifactContentConfigurationEnvironment:
				return path, true
			case "DATAGROUND_DATABASE_URL":
				return "configured", true
			default:
				return "", false
			}
		}
	}
	tests := map[string]string{
		"case alias":     strings.Replace(valid, `"bucket":`, `"BUCKET":`, 1),
		"case duplicate": strings.Replace(valid, `"bucket":`, `"BUCKET":"other","bucket":`, 1),
		"null":           strings.Replace(valid, `"artifacts"`, `null`, 1),
		"valid":          valid, "empty": "", "unknown": strings.Replace(valid, `"contract":`, `"extra":true,"contract":`, 1),
		"duplicate": strings.Replace(valid, `"bucket":`, `"bucket":"other","bucket":`, 1),
		"trailing":  valid + `{}`, "version": strings.Replace(valid, "/v1", "/v2", 1),
		"remote": strings.Replace(valid, "127.0.0.1", "192.0.2.1", 1), "dns": strings.Replace(valid, "127.0.0.1", "localhost", 1),
		"user": strings.Replace(valid, "http://", "http://user:secret@", 1), "query": strings.Replace(valid, ":58333", ":58333?query=secret", 1),
		"path": strings.Replace(valid, ":58333", ":58333/path", 1), "bucket": strings.Replace(valid, `"artifacts"`, `"../other"`, 1),
		"zero": strings.Replace(valid, "1048576", "0", 1), "large": strings.Replace(valid, "1048576", "16777217", 1),
	}
	for name, encoded := range tests {
		t.Run(name, func(t *testing.T) {
			path := writeStartupFile(t, t.TempDir(), "artifacts.json", encoded)
			config, err := loadArtifactContentConfiguration(lookup(path))
			if name == "valid" {
				if err != nil || len(config) != 1 {
					t.Fatalf("valid config: %v", err)
				}
			} else if err == nil {
				t.Fatal("invalid config accepted")
			}
		})
	}
	for _, name := range []string{"absent", "empty path", "process local", "oidc"} {
		t.Run(name, func(t *testing.T) {
			config, err := loadArtifactContentConfiguration(func(key string) (string, bool) {
				if name == "absent" {
					return "", false
				}
				if key == artifactContentConfigurationEnvironment {
					if name == "empty path" {
						return "", true
					}
					return "/unread-file", true
				}
				if key == "DATAGROUND_API_SECURITY_CONFIG_FILE" && name == "oidc" {
					return "/security", true
				}
				if key == "DATAGROUND_DATABASE_URL" && name != "process local" {
					return "configured", true
				}
				return "", false
			})
			if name == "absent" {
				if err != nil || len(config) != 0 {
					t.Fatal("reader enabled by default")
				}
			} else if err == nil {
				t.Fatal("unsupported mode accepted")
			}
		})
	}
}

func TestArtifactContentConfigurationReadsOnlyPinnedObjectEndpoint(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.Method != "GET" || r.URL.Path != "/artifacts/invocation-artifacts/v1/iso_00000000000000000001/inv_00000000000000000001/art_00000000000000000001/"+strings.Repeat("a", 64) || r.Header.Get("Authorization") != "" || r.Header.Get("Accept-Encoding") != "identity" {
			t.Error("unexpected object request")
		}
		_, _ = w.Write([]byte("result"))
	}))
	defer server.Close()
	path := writeStartupFile(t, t.TempDir(), "artifacts.json", `{"contract":"dataground.api-artifact-content/v1","endpoint":"`+server.URL+`","bucket":"artifacts","maximumBytes":1024}`)
	config, err := loadArtifactContentConfiguration(func(key string) (string, bool) {
		switch key {
		case artifactContentConfigurationEnvironment:
			return path, true
		case "DATAGROUND_DATABASE_URL":
			return "configured", true
		default:
			return "", false
		}
	})
	if err != nil {
		t.Fatal(err)
	}
	object, err := config[0].Objects.OpenInvocationArtifactObject(context.Background(), "invocation-artifacts/v1/iso_00000000000000000001/inv_00000000000000000001/art_00000000000000000001/"+strings.Repeat("a", 64))
	if err != nil {
		t.Fatal(err)
	}
	content, err := io.ReadAll(object)
	closeErr := object.Close()
	if err != nil || closeErr != nil || string(content) != "result" || calls != 1 {
		t.Fatal("object read failed")
	}
}
