// Package main initializes the Vaultwarden container runtime environment by injecting
// Docker secrets and enforcing immutable configuration parameters before process execution.
package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"time"
)

const (
	defaultBinaryPath = "/usr/local/bin/vaultwarden"
	defaultBinaryName = "vaultwarden"
	defaultSecretsDir = "/run/secrets"
)

const (
	fileDBName     = "db_name"
	fileDBUser     = "db_user"
	fileDBPassword = "db_password"
	fileRSAKey     = "rsa_key.pem"
)

const (
	envDatabaseURL    = "DATABASE_URL"
	envRSAKeyFilename = "RSA_KEY_FILENAME"
	envRocketProfile  = "ROCKET_PROFILE"
	envRocketAddress  = "ROCKET_ADDRESS"
	envRocketPort     = "ROCKET_PORT"
	envDataFolder     = "DATA_FOLDER"
	envWebVaultFolder = "WEB_VAULT_FOLDER"
	envLogFile        = "LOG_FILE"
	envSSLCertFile    = "SSL_CERT_FILE"
	envSSLCertDir     = "SSL_CERT_DIR"
	envHome           = "HOME"
)

const (
	defaultPostgresHost   = "/var/run/postgresql"
	defaultRocketProfile  = "release"
	defaultRocketAddress  = "0.0.0.0"
	defaultRocketPort     = "8080"
	defaultDataFolder     = "/data"
	defaultWebVaultFolder = "/usr/share/vaultwarden/web-vault"
	defaultLogFile        = "/var/log/vaultwarden/vaultwarden.log"
	defaultSSLCertFile    = "/etc/ssl/certs/ca-certificates.crt"
	defaultSSLCertDir     = "/etc/ssl/certs"
	defaultHomeDir        = "/tmp"
	rsaKeyBaseName        = "rsa_key"
)

const (
	vaultwardenSecretPrefix = "vw_"
)

func defaultReadFile(path string) ([]byte, error) {
	cleanPath := filepath.Clean(path)
	data, err := os.ReadFile(cleanPath)
	if err != nil {
		return nil, fmt.Errorf("reading secret file %q: %w", cleanPath, err)
	}
	return data, nil
}

func defaultReadDir(dirname string) ([]fs.DirEntry, error) {
	cleanDir := filepath.Clean(dirname)
	entries, err := os.ReadDir(cleanDir)
	if err != nil {
		return nil, fmt.Errorf("reading secrets directory %q: %w", cleanDir, err)
	}
	return entries, nil
}

func cancelCommand(cmd *exec.Cmd) func() error {
	return func() error {
		if cmd.Process != nil {
			return cmd.Process.Signal(syscall.SIGTERM)
		}
		return nil
	}
}

func defaultCreateCommand(ctx context.Context, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, defaultBinaryPath)
	if len(args) > 1 {
		cmd.Args = append([]string{defaultBinaryPath}, args[1:]...)
	}
	cmd.Cancel = cancelCommand(cmd)
	cmd.WaitDelay = 5 * time.Second
	return cmd
}

func defaultExec(ctx context.Context, args, env []string) error {
	cmd := createCommand(ctx, args)
	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("running %q: %w", defaultBinaryPath, err)
	}
	return nil
}

var (
	createCommand = defaultCreateCommand
	execProcess   = defaultExec
	exitFunc      = os.Exit
	readDir       = defaultReadDir
	readFile      = defaultReadFile
)

func readOptionalSecret(path string) (secret string, exists bool, err error) {
	data, err := readFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", false, nil
		}
		return "", false, fmt.Errorf("reading secret %q: %w", path, err)
	}

	trimmed := strings.TrimSpace(string(data))
	return trimmed, true, nil
}

func readRequiredSecret(secretsDir, secretName string) (string, error) {
	path := filepath.Join(secretsDir, secretName)
	val, exists, err := readOptionalSecret(path)
	if err != nil {
		return "", err
	}
	if !exists || val == "" {
		return "", fmt.Errorf("required secret %q is missing or empty", secretName)
	}
	return val, nil
}

func setEnvVar(env []string, key, value string) []string {
	prefix := key + "="
	for i, kv := range env {
		if strings.HasPrefix(kv, prefix) {
			env[i] = prefix + value
			return env
		}
	}
	return append(env, prefix+value)
}

func listSecretNames(secretsDir string) ([]string, error) {
	entries, err := readDir(secretsDir)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("scanning secrets directory: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			names = append(names, entry.Name())
		}
	}

	slices.Sort(names)
	return names, nil
}

func hasVaultwardenSecretPrefix(name string) bool {
	return len(name) >= len(vaultwardenSecretPrefix) && strings.EqualFold(name[:len(vaultwardenSecretPrefix)], vaultwardenSecretPrefix)
}

func isInvalidEnvKeyRune(r rune) bool {
	return (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_'
}

func secretEnvKey(name string) (string, error) {
	remainder := name[len(vaultwardenSecretPrefix):]
	key := strings.ToUpper(remainder)
	if key == "" || strings.ContainsFunc(key, isInvalidEnvKeyRune) {
		return "", fmt.Errorf("secret %q cannot be mapped to an environment variable", name)
	}
	return key, nil
}

func configureDynamicSecrets(env []string, secretsDir string, names []string) ([]string, error) {
	for _, name := range names {
		if !hasVaultwardenSecretPrefix(name) {
			continue
		}

		key, err := secretEnvKey(name)
		if err != nil {
			return nil, err
		}

		val, exists, err := readOptionalSecret(filepath.Join(secretsDir, name))
		if err != nil {
			return nil, err
		}
		if exists && val != "" {
			env = setEnvVar(env, key, val)
		}
	}
	return env, nil
}

func configureDatabaseSecrets(env []string, secretsDir string) ([]string, error) {
	dbName, err := readRequiredSecret(secretsDir, fileDBName)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", fileDBName, err)
	}

	dbUser, err := readRequiredSecret(secretsDir, fileDBUser)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", fileDBUser, err)
	}

	dbPassword, err := readRequiredSecret(secretsDir, fileDBPassword)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", fileDBPassword, err)
	}

	userInfo := url.UserPassword(dbUser, dbPassword)
	databaseURL := fmt.Sprintf("postgresql://%s@/%s?host=%s", userInfo.String(), url.PathEscape(dbName), defaultPostgresHost)
	env = setEnvVar(env, envDatabaseURL, databaseURL)
	return env, nil
}

func configureRSAKeySecret(env []string, secretsDir string) ([]string, error) {
	if _, err := readRequiredSecret(secretsDir, fileRSAKey); err != nil {
		return nil, fmt.Errorf("verifying RSA private key: %w", err)
	}

	keyPathWithoutExt := filepath.Join(secretsDir, rsaKeyBaseName)
	env = setEnvVar(env, envRSAKeyFilename, keyPathWithoutExt)
	return env, nil
}

func configureEnforcedSettings(env []string) []string {
	env = setEnvVar(env, envRocketProfile, defaultRocketProfile)
	env = setEnvVar(env, envRocketAddress, defaultRocketAddress)
	env = setEnvVar(env, envRocketPort, defaultRocketPort)
	env = setEnvVar(env, envDataFolder, defaultDataFolder)
	env = setEnvVar(env, envWebVaultFolder, defaultWebVaultFolder)
	env = setEnvVar(env, envLogFile, defaultLogFile)
	env = setEnvVar(env, envSSLCertFile, defaultSSLCertFile)
	env = setEnvVar(env, envSSLCertDir, defaultSSLCertDir)
	env = setEnvVar(env, envHome, defaultHomeDir)
	return env
}

// PrepareEnvironment populates process configuration from Docker secrets and enforces
// immutable container constraints required for secure scratch execution.
func PrepareEnvironment(baseEnv []string, secretsDir string) ([]string, error) {
	env := slices.Clone(baseEnv)

	names, err := listSecretNames(secretsDir)
	if err != nil {
		return nil, err
	}

	if env, err = configureDynamicSecrets(env, secretsDir, names); err != nil {
		return nil, err
	}
	if env, err = configureDatabaseSecrets(env, secretsDir); err != nil {
		return nil, err
	}
	if env, err = configureRSAKeySecret(env, secretsDir); err != nil {
		return nil, err
	}

	env = configureEnforcedSettings(env)
	return env, nil
}

// BuildCommandArguments standardizes invocation parameters to maintain deterministic
// execution when arguments are supplied with or without the binary name.
func BuildCommandArguments(args []string) []string {
	if len(args) == 0 {
		return []string{defaultBinaryName}
	}

	if args[0] == defaultBinaryPath || args[0] == defaultBinaryName {
		res := make([]string, len(args))
		copy(res, args)
		res[0] = defaultBinaryName
		return res
	}

	res := make([]string, 0, len(args)+1)
	res = append(res, defaultBinaryName)
	res = append(res, args...)
	return res
}

// Run coordinates environment preparation and process supervision to ensure graceful
// shutdown and secret injection during container lifecycle management.
func Run(ctx context.Context, args []string, secretsDir string) error {
	env, err := PrepareEnvironment(os.Environ(), secretsDir)
	if err != nil {
		return fmt.Errorf("preparing runtime environment: %w", err)
	}

	cmdArgs := BuildCommandArguments(args)
	if execErr := execProcess(ctx, cmdArgs, env); execErr != nil {
		return fmt.Errorf("executing %q: %w", defaultBinaryPath, execErr)
	}

	return nil
}

func main() {
	var args []string
	if len(os.Args) > 1 {
		args = os.Args[1:]
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	if err := Run(ctx, args, defaultSecretsDir); err != nil {
		fmt.Fprintf(os.Stderr, "init error: %v\n", err)
		exitFunc(1)
	}
}
