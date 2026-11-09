package macos

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const beeperVersion = "0.15.0"
const beeperAuthRepair = "Use a separate cache directory or manually repair the existing private authentication; it was not replaced."

var beeperDigests = map[string]string{
	"linux-amd64": "ff3091816429d41aeb51871445f8ccd77864ebe479a57a4b4a5981fd5051a73e",
	"linux-arm64": "aea4f129ac1dffd964d1ad8a57bd2d080c969f277666d0aa734b73de7c6f0033",
	"macos-amd64": "887b8a344c2782c150bb84aecd6c144f5b8d028bf9960622cf497730fb5dbf2c",
	"macos-arm64": "4ed8dcd4bfda2500fef7258d6109dfc200a9d7a4046c878d9749962d1219671c",
}

// privateCommand never exposes subprocess streams or raw execution errors.
// The shell only establishes a child-local umask; credentials remain in Env.
func privateCommand(ctx context.Context, environment []string, binary string, arguments ...string) ([]byte, error) {
	command := exec.CommandContext(ctx, "/bin/sh", append([]string{"-c", "umask 077; exec \"$@\"", "corral-private", binary}, arguments...)...)
	command.Env = environment
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = io.Discard
	if err := command.Run(); err != nil {
		return nil, errors.New("private subprocess did not complete")
	}
	return stdout.Bytes(), nil
}

func withoutEnvironment(environment []string, names ...string) []string {
	result := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, _ := strings.Cut(entry, "=")
		keep := true
		for _, excluded := range names {
			if name == excluded {
				keep = false
				break
			}
		}
		if keep {
			result = append(result, entry)
		}
	}
	return result
}

func vaultLogin(ctx context.Context, item string) (string, string, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "darwin" {
		return "", "", errors.New("Bitwarden input requires a Linux or macOS client")
	}
	if strings.TrimSpace(item) == "" || strings.HasPrefix(item, "-") || strings.ContainsRune(item, 0) {
		return "", "", errors.New("a selected Bitwarden login item is required")
	}
	environment := os.Environ()
	if os.Getenv("BW_SESSION") == "" {
		file, err := openBitwardenSession()
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return "", "", err
		}
		if file != nil {
			body, readErr := io.ReadAll(io.LimitReader(file, 1024*1024+1))
			_ = file.Close()
			if readErr != nil || len(body) > 1024*1024 {
				return "", "", errors.New("cannot read private Bitwarden session")
			}
			session := strings.TrimSpace(string(body))
			if strings.ContainsRune(session, 0) {
				return "", "", errors.New("invalid private Bitwarden session")
			}
			environment = append(withoutEnvironment(environment, "BW_SESSION"), "BW_SESSION="+session)
		}
	}
	checkCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	_, err := privateCommand(checkCtx, environment, "bw", "unlock", "--check")
	cancel()
	if err != nil {
		return "", "", errors.New("Bitwarden is locked; unlock it noninteractively before continuing")
	}
	getCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	body, err := privateCommand(getCtx, environment, "bw", "get", "item", item)
	if err != nil {
		return "", "", errors.New("could not retrieve the selected Bitwarden item")
	}
	var record struct {
		Login struct {
			Username string `json:"username"`
			Password string `json:"password"`
		} `json:"login"`
	}
	if json.Unmarshal(body, &record) != nil || record.Login.Username == "" || record.Login.Password == "" || strings.ContainsRune(record.Login.Username, 0) || strings.ContainsRune(record.Login.Password, 0) {
		return "", "", errors.New("the selected vault item must contain valid login username and password")
	}
	return record.Login.Username, record.Login.Password, nil
}

type beeperAuthClient struct {
	binary func(context.Context, string) (string, error)
	login  func(context.Context, string) (string, string, error)
	run    func(context.Context, []string, string, ...string) ([]byte, error)
}

// PrepareBeeperAuth preserves an existing native account, even when item selects
// a different vault record. Invalid/stale native state is never replaced.
func PrepareBeeperAuth(ctx context.Context, cacheDir, item string) (string, error) {
	return prepareBeeperAuth(ctx, cacheDir, item, beeperAuthClient{prepareBeeperBinary, vaultLogin, privateCommand})
}

func prepareBeeperAuth(ctx context.Context, cacheDir, item string, client beeperAuthClient) (string, error) {
	if strings.TrimSpace(cacheDir) == "" {
		return "", errors.New("a private Beeper cache directory is required")
	}
	cache, err := filepath.Abs(cacheDir)
	if err != nil {
		return "", errors.New("cannot resolve Beeper cache directory")
	}
	if err := privateDirectory(cache); err != nil {
		return "", err
	}
	unlock, err := lockPrivateState(ctx, cache)
	if err != nil {
		return "", err
	}
	defer unlock()
	directory := filepath.Join(cache, "beeper-auth")
	if err := privateDirectory(directory); err != nil {
		return "", err
	}
	config := filepath.Join(directory, "config.json")
	present := false
	if _, err := os.Lstat(config); err == nil {
		present = true
		if err := privateConfig(config); err != nil {
			return "", err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("cannot inspect existing Beeper authentication")
	}
	binary, err := client.binary(ctx, cache)
	if err != nil {
		return "", err
	}
	environment := withoutEnvironment(os.Environ(), "BEEPER_USERNAME", "BEEPER_PASSWORD", "BW_SESSION")
	invoke := func(environment []string, command ...string) ([]byte, error) {
		commandCtx, cancel := context.WithTimeout(ctx, 120*time.Second)
		defer cancel()
		return client.run(commandCtx, environment, binary, append([]string{"--config", config}, command...)...)
	}
	authenticated := func() bool {
		body, err := invoke(environment, "whoami", "--raw")
		return err == nil && validBeeperIdentity(body)
	}
	if present {
		if !authenticated() {
			return "", errors.New("existing Beeper authentication could not be validated. " + beeperAuthRepair)
		}
		return config, nil
	}
	username, password, err := client.login(ctx, item)
	if err != nil {
		return "", err
	}
	credentials := append(append([]string(nil), environment...), "BEEPER_USERNAME="+username, "BEEPER_PASSWORD="+password)
	_, loginErr := invoke(credentials, "login-password")
	credentials = nil
	username, password = "", ""
	// Even a failed bbctl command may have persisted partial native state. Keep
	// that state private and retain it for explicit repair, never auto-retry.
	if err := privateConfig(config); err != nil {
		return "", errors.New("Beeper password authentication did not create a safe private config. " + beeperAuthRepair)
	}
	if loginErr != nil || !authenticated() {
		return "", errors.New("prepared Beeper authentication could not be validated. " + beeperAuthRepair)
	}
	return config, nil
}

func validBeeperIdentity(body []byte) bool {
	var identity struct {
		User     map[string]json.RawMessage `json:"user"`
		UserInfo struct {
			Username string `json:"username"`
		} `json:"userInfo"`
	}
	return json.Unmarshal(body, &identity) == nil && identity.User != nil && identity.UserInfo.Username != ""
}

func beeperArtifact(system, arch string) (string, error) {
	if system == "darwin" {
		system = "macos"
	}
	artifact := system + "-" + arch
	if _, ok := beeperDigests[artifact]; !ok {
		return "", errors.New("bbctl requires a Linux/macOS amd64 or arm64 client")
	}
	return artifact, nil
}

func prepareBeeperBinary(ctx context.Context, cache string) (string, error) {
	artifact, err := beeperArtifact(runtime.GOOS, runtime.GOARCH)
	if err != nil {
		return "", err
	}
	tools := filepath.Join(cache, "tools")
	if err := privateDirectory(tools); err != nil {
		return "", err
	}
	binary := filepath.Join(tools, "bbctl-"+artifact)
	digest := beeperDigests[artifact]
	file, err := privateOpenFile(binary, os.O_RDONLY, 0)
	if err == nil {
		hash := sha256.New()
		_, readErr := io.Copy(hash, file)
		if readErr == nil && hex.EncodeToString(hash.Sum(nil)) == digest {
			err := file.Chmod(0o700)
			_ = file.Close()
			if err != nil {
				return "", errors.New("cannot restrict cached bbctl executable")
			}
			return binary, nil
		}
		_ = file.Close()
		if readErr != nil {
			return "", errors.New("cannot verify cached bbctl executable")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	requestCtx, cancel := context.WithTimeout(ctx, 60*time.Second)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, "https://github.com/beeper/bridge-manager/releases/download/v"+beeperVersion+"/bbctl-"+artifact, nil)
	if err != nil {
		return "", errors.New("cannot prepare pinned bbctl download")
	}
	client := &http.Client{CheckRedirect: func(request *http.Request, via []*http.Request) error {
		if request.URL.Scheme != "https" || len(via) >= 10 {
			return errors.New("refusing unsafe bbctl download redirect")
		}
		return nil
	}}
	response, err := client.Do(request)
	if err != nil {
		return "", errors.New("cannot download pinned bbctl executable")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK || response.Request.URL.Scheme != "https" {
		return "", errors.New("pinned bbctl download was not successful HTTPS")
	}
	temporary, err := os.CreateTemp(tools, ".bbctl-")
	if err != nil {
		return "", errors.New("cannot stage pinned bbctl executable")
	}
	defer func() { _ = os.Remove(temporary.Name()) }()
	defer func() { _ = temporary.Close() }()
	hash := sha256.New()
	if _, err := io.Copy(io.MultiWriter(temporary, hash), response.Body); err != nil {
		return "", errors.New("cannot receive pinned bbctl executable")
	}
	if hex.EncodeToString(hash.Sum(nil)) != digest {
		return "", errors.New("downloaded bbctl failed its pinned SHA256 check")
	}
	if temporary.Sync() != nil || temporary.Chmod(0o700) != nil || temporary.Close() != nil {
		return "", errors.New("cannot publish pinned bbctl executable")
	}
	if err := os.Rename(temporary.Name(), binary); err != nil {
		return "", errors.New("cannot publish pinned bbctl executable")
	}
	return binary, nil
}
