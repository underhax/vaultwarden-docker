package main

import (
	"bytes"
	"context"
	"errors"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

var (
	errMockExec    = errors.New("simulated exec error")
	errMockReadDir = errors.New("simulated readdir error")
	errMockRead    = errors.New("simulated read error")
)

var (
	recordedExitCode     = -1
	recordedExecArgs     []string
	recordedExecEnv      []string
	mockTargetSecretPath string
	redirectedSecretsDir string
)

func mockExit(code int) {
	recordedExitCode = code
}

func mockFailingReadDir(_ string) ([]fs.DirEntry, error) {
	return nil, errMockReadDir
}

func mockFailingReadFile(_ string) ([]byte, error) {
	return nil, errMockRead
}

func mockConditionalReadFile(path string) ([]byte, error) {
	if mockTargetSecretPath != "" && path == mockTargetSecretPath {
		return nil, errMockRead
	}
	return defaultReadFile(path)
}

func mockRedirectedReadFile(path string) ([]byte, error) {
	base := filepath.Base(path)
	return defaultReadFile(filepath.Join(redirectedSecretsDir, base))
}

func mockRedirectedReadDir(_ string) ([]fs.DirEntry, error) {
	return defaultReadDir(redirectedSecretsDir)
}

func mockSuccessfulExec(_ context.Context, args, env []string) error {
	recordedExecArgs = args
	recordedExecEnv = env
	return nil
}

func mockFailingExec(_ context.Context, _, _ []string) error {
	return errMockExec
}

func mockNonExistentCommand(ctx context.Context, _ []string) *exec.Cmd {
	return exec.CommandContext(ctx, "/dev/null/non_existent_binary")
}

func mockTrueCommand(ctx context.Context, _ []string) *exec.Cmd {
	return exec.CommandContext(ctx, "true")
}

func TestDefaultReadFile(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "sample.txt")
	expectedContent := []byte("secret_value")

	if err := os.WriteFile(filePath, expectedContent, 0o600); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	data, err := defaultReadFile(filePath)
	if err != nil {
		t.Fatalf("unexpected error reading file: %v", err)
	}

	if !bytes.Equal(data, expectedContent) {
		t.Fatalf("expected content %q, got %q", expectedContent, data)
	}

	if _, err := defaultReadFile(filepath.Join(tempDir, "non_existent.txt")); err == nil {
		t.Fatal("expected error reading non-existent file, got nil")
	}
}

func TestDefaultReadDir(t *testing.T) {
	tempDir := t.TempDir()

	entries, err := defaultReadDir(tempDir)
	if err != nil {
		t.Fatalf("unexpected error reading directory: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected 0 entries, got %d", len(entries))
	}

	if _, err := defaultReadDir(filepath.Join(tempDir, "non_existent_dir")); err == nil {
		t.Fatal("expected error reading non-existent directory, got nil")
	}
}

func TestDefaultCreateCommand(t *testing.T) {
	ctx := context.Background()

	tests := []struct {
		name     string
		args     []string
		wantPath string
		wantArgs []string
	}{
		{
			name:     "empty_args",
			args:     nil,
			wantPath: defaultBinaryPath,
			wantArgs: []string{defaultBinaryPath},
		},
		{
			name:     "binary_only",
			args:     []string{defaultBinaryName},
			wantPath: defaultBinaryPath,
			wantArgs: []string{defaultBinaryPath},
		},
		{
			name:     "with_arguments",
			args:     []string{defaultBinaryName, "--log-level", "debug"},
			wantPath: defaultBinaryPath,
			wantArgs: []string{defaultBinaryPath, "--log-level", "debug"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cmd := defaultCreateCommand(ctx, tt.args)
			if cmd == nil {
				t.Fatal("expected command object, got nil")
			}
			if cmd.Path != tt.wantPath {
				t.Fatalf("expected command path %q, got %q", tt.wantPath, cmd.Path)
			}
			if !slices.Equal(cmd.Args, tt.wantArgs) {
				t.Fatalf("expected args %v, got %v", tt.wantArgs, cmd.Args)
			}
			if cmd.Cancel == nil {
				t.Fatal("expected cancel function, got nil")
			}
			if cmd.WaitDelay != 5*time.Second {
				t.Fatalf("expected wait delay 5s, got %v", cmd.WaitDelay)
			}
			if err := cmd.Cancel(); err != nil {
				t.Fatalf("unexpected cancel error: %v", err)
			}
		})
	}

	runningCmd := exec.CommandContext(ctx, "sleep", "10")
	cancelFn := cancelCommand(runningCmd)
	if err := runningCmd.Start(); err != nil {
		t.Fatalf("cmd.Start() unexpected error: %v", err)
	}
	defer func() {
		if waitErr := runningCmd.Wait(); waitErr != nil {
			if _, ok := errors.AsType[*exec.ExitError](waitErr); !ok {
				t.Errorf("runningCmd.Wait() error: %v", waitErr)
			}
		}
	}()
	if err := cancelFn(); err != nil {
		t.Fatalf("cancelFn() unexpected error: %v", err)
	}
}

func TestDefaultExec(t *testing.T) {
	ctx := context.Background()
	origCreate := createCommand
	t.Cleanup(func() { createCommand = origCreate })

	createCommand = mockNonExistentCommand
	if err := defaultExec(ctx, nil, nil); err == nil {
		t.Fatal("expected error executing non-existent binary, got nil")
	}

	createCommand = mockTrueCommand
	if err := defaultExec(ctx, nil, nil); err != nil {
		t.Fatalf("unexpected error executing true binary: %v", err)
	}
}

func TestReadOptionalSecret(t *testing.T) {
	tempDir := t.TempDir()
	filePath := filepath.Join(tempDir, "optional.txt")

	val, exists, err := readOptionalSecret(filePath)
	if err != nil {
		t.Fatalf("unexpected error for missing secret: %v", err)
	}
	if exists || val != "" {
		t.Fatalf("expected non-existent secret to return exists=false and val=\"\", got %v, %q", exists, val)
	}

	if writeErr := os.WriteFile(filePath, []byte("   trimmed_value   \n"), 0o600); writeErr != nil {
		t.Fatalf("writing secret file: %v", writeErr)
	}

	val, exists, err = readOptionalSecret(filePath)
	if err != nil {
		t.Fatalf("unexpected error reading secret: %v", err)
	}
	if !exists || val != "trimmed_value" {
		t.Fatalf("expected exists=true and trimmed value, got %v, %q", exists, val)
	}

	origReadFile := readFile
	readFile = mockFailingReadFile
	t.Cleanup(func() { readFile = origReadFile })

	if _, _, err := readOptionalSecret(filePath); err == nil {
		t.Fatal("expected read failure error, got nil")
	}
}

func TestReadRequiredSecret(t *testing.T) {
	tempDir := t.TempDir()
	secretName := "required.txt"
	filePath := filepath.Join(tempDir, secretName)

	if _, err := readRequiredSecret(tempDir, secretName); err == nil {
		t.Fatal("expected error for missing required secret, got nil")
	}

	if err := os.WriteFile(filePath, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("writing empty secret file: %v", err)
	}

	if _, err := readRequiredSecret(tempDir, secretName); err == nil {
		t.Fatal("expected error for whitespace-only secret, got nil")
	}

	if err := os.WriteFile(filePath, []byte("valid_secret"), 0o600); err != nil {
		t.Fatalf("writing valid secret: %v", err)
	}

	val, err := readRequiredSecret(tempDir, secretName)
	if err != nil {
		t.Fatalf("unexpected error reading required secret: %v", err)
	}
	if val != "valid_secret" {
		t.Fatalf("expected \"valid_secret\", got %q", val)
	}

	origReadFile := readFile
	readFile = mockFailingReadFile
	t.Cleanup(func() { readFile = origReadFile })

	if _, err := readRequiredSecret(tempDir, secretName); err == nil {
		t.Fatal("expected read error, got nil")
	}
}

func TestSetEnvVar(t *testing.T) {
	tests := []struct {
		name     string
		initial  []string
		key      string
		value    string
		expected []string
	}{
		{
			name:     "append_new_variable",
			initial:  []string{"EXISTING=1"},
			key:      "NEW_KEY",
			value:    "value",
			expected: []string{"EXISTING=1", "NEW_KEY=value"},
		},
		{
			name:     "replace_existing_variable",
			initial:  []string{"TARGET=old", "OTHER=2"},
			key:      "TARGET",
			value:    "new",
			expected: []string{"TARGET=new", "OTHER=2"},
		},
		{
			name:     "empty_initial_slice",
			initial:  nil,
			key:      "TARGET",
			value:    "val",
			expected: []string{"TARGET=val"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := setEnvVar(tt.initial, tt.key, tt.value)
			if !slices.Equal(res, tt.expected) {
				t.Fatalf("expected env %v, got %v", tt.expected, res)
			}
		})
	}
}

func TestListSecretNames(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tempDir, "file_b"), []byte("b"), 0o600); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "file_a"), []byte("a"), 0o600); err != nil {
		t.Fatalf("writing test file: %v", err)
	}
	if err := os.Mkdir(filepath.Join(tempDir, "sub_dir"), 0o750); err != nil {
		t.Fatalf("creating subdirectory: %v", err)
	}

	names, err := listSecretNames(tempDir)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	expectedNames := []string{"file_a", "file_b"}
	if !slices.Equal(names, expectedNames) {
		t.Fatalf("expected names %v, got %v", expectedNames, names)
	}

	missingNames, err := listSecretNames(filepath.Join(tempDir, "non_existent_folder"))
	if err != nil {
		t.Fatalf("unexpected error for non-existent dir: %v", err)
	}
	if missingNames != nil {
		t.Fatalf("expected nil for non-existent directory, got %v", missingNames)
	}

	origReadDir := readDir
	readDir = mockFailingReadDir
	t.Cleanup(func() { readDir = origReadDir })

	if _, err := listSecretNames(tempDir); err == nil {
		t.Fatal("expected error scanning directory, got nil")
	}
}

func TestHasVaultwardenSecretPrefix(t *testing.T) {
	tests := []struct {
		name     string
		fileName string
		expected bool
	}{
		{name: "lowercase_prefix", fileName: "vw_config_key", expected: true},
		{name: "uppercase_prefix", fileName: "VW_DOMAIN", expected: true},
		{name: "mixed_case_prefix", fileName: "Vw_admin_token", expected: true},
		{name: "prefix_only", fileName: vaultwardenSecretPrefix, expected: true},
		{name: "unrelated_file", fileName: "db_password", expected: false},
		{name: "prefix_at_end", fileName: "domain_vw_", expected: false},
		{name: "short_name", fileName: "vw", expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := hasVaultwardenSecretPrefix(tt.fileName); got != tt.expected {
				t.Fatalf("expected %v for %q, got %v", tt.expected, tt.fileName, got)
			}
		})
	}
}

func TestIsInvalidEnvKeyRune(t *testing.T) {
	tests := []struct {
		name     string
		char     rune
		expected bool
	}{
		{name: "uppercase_a", char: 'A', expected: false},
		{name: "uppercase_z", char: 'Z', expected: false},
		{name: "digit_zero", char: '0', expected: false},
		{name: "digit_nine", char: '9', expected: false},
		{name: "underscore", char: '_', expected: false},
		{name: "lowercase_a", char: 'a', expected: true},
		{name: "hyphen", char: '-', expected: true},
		{name: "dot", char: '.', expected: true},
		{name: "equals", char: '=', expected: true},
		{name: "space", char: ' ', expected: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isInvalidEnvKeyRune(tt.char); got != tt.expected {
				t.Fatalf("expected %v for rune %q, got %v", tt.expected, tt.char, got)
			}
		})
	}
}

func TestSecretEnvKey(t *testing.T) {
	tests := []struct {
		name      string
		fileName  string
		wantKey   string
		expectErr bool
	}{
		{name: "simple_domain", fileName: "vw_domain", wantKey: "DOMAIN", expectErr: false},
		{name: "composite_name", fileName: "vw_admin_token", wantKey: "ADMIN_TOKEN", expectErr: false},
		{name: "uppercase_input", fileName: "VW_SIGNUPS_ALLOWED", wantKey: "SIGNUPS_ALLOWED", expectErr: false},
		{name: "digits_and_underscores", fileName: "vw_port_8080_ready", wantKey: "PORT_8080_READY", expectErr: false},
		{name: "empty_after_prefix", fileName: vaultwardenSecretPrefix, wantKey: "", expectErr: true},
		{name: "contains_hyphen", fileName: "vw_sub-domain", wantKey: "", expectErr: true},
		{name: "contains_dot", fileName: "vw_domain.name", wantKey: "", expectErr: true},
		{name: "contains_equal", fileName: "vw_key=value", wantKey: "", expectErr: true},
		{name: "contains_space", fileName: "vw_key name", wantKey: "", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key, err := secretEnvKey(tt.fileName)
			if tt.expectErr {
				if err == nil {
					t.Fatalf("expected error for file %q, got nil", tt.fileName)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for file %q: %v", tt.fileName, err)
			}
			if key != tt.wantKey {
				t.Fatalf("expected key %q, got %q", tt.wantKey, key)
			}
		})
	}
}

func TestConfigureDynamicSecrets(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tempDir, "vw_domain"), []byte("vault.example.org\n"), 0o600); err != nil {
		t.Fatalf("writing secret: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "vw_admin_token"), []byte("secret_token"), 0o600); err != nil {
		t.Fatalf("writing secret: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "vw_empty"), []byte("   \n"), 0o600); err != nil {
		t.Fatalf("writing secret: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "ignored_secret"), []byte("value"), 0o600); err != nil {
		t.Fatalf("writing secret: %v", err)
	}

	names := []string{"ignored_secret", "vw_admin_token", "vw_domain", "vw_empty"}
	env, err := configureDynamicSecrets([]string{"INITIAL=true"}, tempDir, names)
	if err != nil {
		t.Fatalf("unexpected error configuring dynamic secrets: %v", err)
	}

	if !slices.Contains(env, "DOMAIN=vault.example.org") {
		t.Fatal("expected DOMAIN to be configured")
	}
	if !slices.Contains(env, "ADMIN_TOKEN=secret_token") {
		t.Fatal("expected ADMIN_TOKEN to be configured")
	}
	if slices.ContainsFunc(env, func(s string) bool { return strings.HasPrefix(s, "EMPTY=") }) {
		t.Fatal("expected empty secret not to be set")
	}

	invalidNames := []string{vaultwardenSecretPrefix}
	if _, err := configureDynamicSecrets(nil, tempDir, invalidNames); err == nil {
		t.Fatal("expected error for invalid dynamic secret name, got nil")
	}

	origReadFile := readFile
	mockTargetSecretPath = filepath.Join(tempDir, "vw_domain")
	readFile = mockConditionalReadFile
	t.Cleanup(func() {
		readFile = origReadFile
		mockTargetSecretPath = ""
	})

	if _, err := configureDynamicSecrets(nil, tempDir, names); err == nil {
		t.Fatal("expected error when reading secret fails, got nil")
	}
}

func TestConfigureDatabaseSecrets_Success(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tempDir, fileDBName), []byte("vault_db"), 0o600); err != nil {
		t.Fatalf("writing db_name: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileDBUser), []byte("vault_user"), 0o600); err != nil {
		t.Fatalf("writing db_user: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileDBPassword), []byte("p@ss:w/ord#1"), 0o600); err != nil {
		t.Fatalf("writing db_password: %v", err)
	}

	env, err := configureDatabaseSecrets(nil, tempDir)
	if err != nil {
		t.Fatalf("unexpected error configuring database secrets: %v", err)
	}

	var foundURL string
	for _, kv := range env {
		if val, found := strings.CutPrefix(kv, "DATABASE_URL="); found {
			foundURL = val
			break
		}
	}

	expectedPrefix := "postgresql://vault_user:p%40ss%3Aw%2Ford%231@/vault_db?host=/var/run/postgresql"
	if foundURL != expectedPrefix {
		t.Fatalf("expected DATABASE_URL %q, got %q", expectedPrefix, foundURL)
	}
}

func TestConfigureDatabaseSecrets_MissingFields(t *testing.T) {
	tests := []struct {
		name          string
		missingSecret string
	}{
		{name: "missing_db_name", missingSecret: fileDBName},
		{name: "missing_db_user", missingSecret: fileDBUser},
		{name: "missing_db_password", missingSecret: fileDBPassword},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			subDir := t.TempDir()
			if tt.missingSecret != fileDBName {
				if writeErr := os.WriteFile(filepath.Join(subDir, fileDBName), []byte("db"), 0o600); writeErr != nil {
					t.Fatalf("writing file: %v", writeErr)
				}
			}
			if tt.missingSecret != fileDBUser {
				if writeErr := os.WriteFile(filepath.Join(subDir, fileDBUser), []byte("user"), 0o600); writeErr != nil {
					t.Fatalf("writing file: %v", writeErr)
				}
			}
			if tt.missingSecret != fileDBPassword {
				if writeErr := os.WriteFile(filepath.Join(subDir, fileDBPassword), []byte("pass"), 0o600); writeErr != nil {
					t.Fatalf("writing file: %v", writeErr)
				}
			}

			if _, err := configureDatabaseSecrets(nil, subDir); err == nil {
				t.Fatalf("expected error when %s is missing, got nil", tt.missingSecret)
			}
		})
	}
}

func TestConfigureRSAKeySecret(t *testing.T) {
	tempDir := t.TempDir()
	keyFile := filepath.Join(tempDir, fileRSAKey)

	if _, err := configureRSAKeySecret(nil, tempDir); err == nil {
		t.Fatal("expected error when rsa_key.pem is missing, got nil")
	}

	if err := os.WriteFile(keyFile, []byte("   \n"), 0o600); err != nil {
		t.Fatalf("writing empty key file: %v", err)
	}
	if _, err := configureRSAKeySecret(nil, tempDir); err == nil {
		t.Fatal("expected error when rsa_key.pem is empty, got nil")
	}

	if err := os.WriteFile(keyFile, []byte("-----BEGIN RSA PRIVATE KEY-----"), 0o600); err != nil {
		t.Fatalf("writing valid key: %v", err)
	}

	env, err := configureRSAKeySecret(nil, tempDir)
	if err != nil {
		t.Fatalf("unexpected error configuring RSA key: %v", err)
	}

	expectedPath := filepath.Join(tempDir, "rsa_key")
	expectedEntry := "RSA_KEY_FILENAME=" + expectedPath
	if !slices.Contains(env, expectedEntry) {
		t.Fatalf("expected env to contain %q, got %v", expectedEntry, env)
	}
}

func TestConfigureEnforcedSettings(t *testing.T) {
	initial := []string{
		"ROCKET_PORT=9090",
		"HOME=/root",
		"DATA_FOLDER=/custom/data",
	}

	env := configureEnforcedSettings(initial)

	expectedSettings := []struct {
		key   string
		value string
	}{
		{key: "ROCKET_PROFILE", value: "release"},
		{key: "ROCKET_ADDRESS", value: "0.0.0.0"},
		{key: "ROCKET_PORT", value: "8080"},
		{key: "DATA_FOLDER", value: "/data"},
		{key: "WEB_VAULT_FOLDER", value: "/usr/share/vaultwarden/web-vault"},
		{key: "LOG_FILE", value: "/var/log/vaultwarden/vaultwarden.log"},
		{key: "SSL_CERT_FILE", value: "/etc/ssl/certs/ca-certificates.crt"},
		{key: "SSL_CERT_DIR", value: "/etc/ssl/certs"},
		{key: "HOME", value: "/tmp"},
	}

	for _, s := range expectedSettings {
		expectedKV := s.key + "=" + s.value
		if !slices.Contains(env, expectedKV) {
			t.Fatalf("expected enforced setting %q in %v", expectedKV, env)
		}
	}
}

func TestPrepareEnvironment(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tempDir, fileDBName), []byte("db"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileDBUser), []byte("usr"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileDBPassword), []byte("pwd"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileRSAKey), []byte("fake_key"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, "vw_domain"), []byte("vault.example.org"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	env, err := PrepareEnvironment([]string{"BASE=1"}, tempDir)
	if err != nil {
		t.Fatalf("unexpected error preparing environment: %v", err)
	}

	if !slices.Contains(env, "DOMAIN=vault.example.org") {
		t.Fatal("expected DOMAIN to be configured")
	}
	if !slices.Contains(env, "DATABASE_URL=postgresql://usr:pwd@/db?host=/var/run/postgresql") {
		t.Fatal("expected DATABASE_URL to be configured")
	}
	if !slices.Contains(env, "RSA_KEY_FILENAME="+filepath.Join(tempDir, "rsa_key")) {
		t.Fatal("expected RSA_KEY_FILENAME to be configured")
	}
	if !slices.Contains(env, "ROCKET_PROFILE=release") {
		t.Fatal("expected ROCKET_PROFILE to be release")
	}

	origReadDir := readDir
	readDir = mockFailingReadDir
	t.Cleanup(func() { readDir = origReadDir })

	if _, err := PrepareEnvironment(nil, tempDir); err == nil {
		t.Fatal("expected error when directory read fails, got nil")
	}
}

func TestPrepareEnvironment_Errors(t *testing.T) {
	tests := []struct {
		setupFiles func(t *testing.T, dir string)
		name       string
	}{
		{
			name: "dynamic_secret_failure",
			setupFiles: func(t *testing.T, dir string) {
				if writeErr := os.WriteFile(filepath.Join(dir, vaultwardenSecretPrefix), []byte("val"), 0o600); writeErr != nil {
					t.Fatalf("writing secret: %v", writeErr)
				}
			},
		},
		{
			name: "database_secret_failure",
			setupFiles: func(_ *testing.T, _ string) {
			},
		},
		{
			name: "rsa_key_secret_failure",
			setupFiles: func(t *testing.T, dir string) {
				if writeErr := os.WriteFile(filepath.Join(dir, fileDBName), []byte("db"), 0o600); writeErr != nil {
					t.Fatalf("writing db: %v", writeErr)
				}
				if writeErr := os.WriteFile(filepath.Join(dir, fileDBUser), []byte("usr"), 0o600); writeErr != nil {
					t.Fatalf("writing user: %v", writeErr)
				}
				if writeErr := os.WriteFile(filepath.Join(dir, fileDBPassword), []byte("pwd"), 0o600); writeErr != nil {
					t.Fatalf("writing password: %v", writeErr)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			tt.setupFiles(t, dir)
			if _, err := PrepareEnvironment(nil, dir); err == nil {
				t.Fatalf("expected error for %s, got nil", tt.name)
			}
		})
	}
}

func TestBuildCommandArguments(t *testing.T) {
	tests := []struct {
		name     string
		input    []string
		expected []string
	}{
		{
			name:     "empty_arguments",
			input:    nil,
			expected: []string{defaultBinaryName},
		},
		{
			name:     "binary_name_only",
			input:    []string{defaultBinaryName},
			expected: []string{defaultBinaryName},
		},
		{
			name:     "binary_path_only",
			input:    []string{defaultBinaryPath},
			expected: []string{defaultBinaryName},
		},
		{
			name:     "binary_name_with_flags",
			input:    []string{defaultBinaryName, "--version"},
			expected: []string{defaultBinaryName, "--version"},
		},
		{
			name:     "binary_path_with_flags",
			input:    []string{defaultBinaryPath, "--config"},
			expected: []string{defaultBinaryName, "--config"},
		},
		{
			name:     "custom_flags_only",
			input:    []string{"--verbose"},
			expected: []string{defaultBinaryName, "--verbose"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			res := BuildCommandArguments(tt.input)
			if !slices.Equal(res, tt.expected) {
				t.Fatalf("expected args %v, got %v", tt.expected, res)
			}
		})
	}
}

func TestRun(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tempDir, fileDBName), []byte("db"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileDBUser), []byte("usr"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileDBPassword), []byte("pwd"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileRSAKey), []byte("fake_key"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	origExec := execProcess
	execProcess = mockSuccessfulExec
	t.Cleanup(func() { execProcess = origExec })

	ctx := context.Background()
	if err := Run(ctx, []string{"--test"}, tempDir); err != nil {
		t.Fatalf("unexpected error in Run: %v", err)
	}

	expectedArgs := []string{defaultBinaryName, "--test"}
	if !slices.Equal(recordedExecArgs, expectedArgs) {
		t.Fatalf("expected recorded args %v, got %v", expectedArgs, recordedExecArgs)
	}

	if err := Run(ctx, nil, filepath.Join(tempDir, "missing_secrets_dir")); err == nil {
		t.Fatal("expected error preparing environment in Run, got nil")
	}

	execProcess = mockFailingExec
	if err := Run(ctx, nil, tempDir); err == nil {
		t.Fatal("expected execProcess failure, got nil")
	}
}

func TestMain_Execution(t *testing.T) {
	tempDir := t.TempDir()

	if err := os.WriteFile(filepath.Join(tempDir, fileDBName), []byte("db"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileDBUser), []byte("usr"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileDBPassword), []byte("pwd"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}
	if err := os.WriteFile(filepath.Join(tempDir, fileRSAKey), []byte("fake_key"), 0o600); err != nil {
		t.Fatalf("writing file: %v", err)
	}

	origExit := exitFunc
	origExec := execProcess
	origReadDir := readDir
	origReadFile := readFile
	origArgs := os.Args

	exitFunc = mockExit
	execProcess = mockSuccessfulExec
	redirectedSecretsDir = tempDir
	readDir = mockRedirectedReadDir
	readFile = mockRedirectedReadFile

	t.Cleanup(func() {
		exitFunc = origExit
		execProcess = origExec
		readDir = origReadDir
		readFile = origReadFile
		os.Args = origArgs
	})

	recordedExitCode = -1
	os.Args = []string{"init"}
	main()

	if recordedExitCode != -1 {
		t.Fatalf("expected exit code -1 (success), got %d", recordedExitCode)
	}

	execProcess = mockFailingExec
	recordedExitCode = -1
	os.Args = []string{"init", "--help"}
	main()

	if recordedExitCode != 1 {
		t.Fatalf("expected exit code 1 on failure, got %d", recordedExitCode)
	}
}
