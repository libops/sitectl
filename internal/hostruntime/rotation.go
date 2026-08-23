package hostruntime

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	defaultIAMBaseURL       = "https://iam.googleapis.com/v1"
	defaultMetadataTokenURL = "http://169.254.169.254/computeMetadata/v1/instance/service-accounts/default/token"
)

// CredentialCopy declares one application-owned copy of a central credential.
type CredentialCopy struct {
	Path  string
	Owner string
	Group string
}

// KeyRotationOptions defines one managed GCP service-account credential.
type KeyRotationOptions struct {
	ServiceAccount       string
	ProjectID            string
	CredentialsFile      string
	Owner                string
	Group                string
	Copies               []CredentialCopy
	RestartUnit          string
	MinimumAge           time.Duration
	DisableGrace         time.Duration
	AllowOrphanReconcile bool
	FreshMarker          string
	FreshIdentity        string
	HTTPClient           *http.Client
	IAMBaseURL           string
	MetadataTokenURL     string
	Now                  func() time.Time
	Stdout               io.Writer
}

type rotationState struct {
	Version        int      `json:"version"`
	Phase          string   `json:"phase"`
	ServiceAccount string   `json:"service_account"`
	ProjectID      string   `json:"project_id"`
	Credentials    string   `json:"credentials_file"`
	CurrentKeyID   string   `json:"current_key_id,omitempty"`
	NewKeyID       string   `json:"new_key_id,omitempty"`
	NewKeyName     string   `json:"new_key_name,omitempty"`
	Baseline       []string `json:"baseline_key_names,omitempty"`
	CreatedAt      int64    `json:"created_at"`
	DisabledAt     int64    `json:"disabled_at,omitempty"`
}

type serviceAccountCredentials struct {
	Type         string `json:"type"`
	ProjectID    string `json:"project_id"`
	PrivateKeyID string `json:"private_key_id"`
	PrivateKey   string `json:"private_key"`
	ClientEmail  string `json:"client_email"`
	TokenURI     string `json:"token_uri"`
}

type iamKey struct {
	Name     string `json:"name"`
	KeyType  string `json:"keyType"`
	Disabled bool   `json:"disabled"`
}

type keyRotator struct {
	options KeyRotationOptions
	client  *http.Client
	now     func() time.Time
}

// ConvergeKeyRotation advances a durable key rotation by one idempotent pass.
func ConvergeKeyRotation(ctx context.Context, options KeyRotationOptions) error {
	rotator, err := newKeyRotator(options)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(options.CredentialsFile), 0o750); err != nil {
		return fmt.Errorf("prepare credential directory: %w", err)
	}
	lock, err := AcquireLock(options.CredentialsFile + ".rotation.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	return rotator.converge(ctx)
}

// RollbackKeyRotation restores the prior key while it is in the disable grace period.
func RollbackKeyRotation(ctx context.Context, options KeyRotationOptions) error {
	rotator, err := newKeyRotator(options)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(options.CredentialsFile), 0o750); err != nil {
		return fmt.Errorf("prepare credential directory: %w", err)
	}
	lock, err := AcquireLock(options.CredentialsFile + ".rotation.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	return rotator.rollback(ctx)
}

// RetireKeyCredentials removes the managed remote key and every local credential copy.
func RetireKeyCredentials(ctx context.Context, options KeyRotationOptions) error {
	rotator, err := newKeyRotator(options)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(options.CredentialsFile), 0o750); err != nil {
		return fmt.Errorf("prepare credential directory: %w", err)
	}
	lock, err := AcquireLock(options.CredentialsFile + ".rotation.lock")
	if err != nil {
		return err
	}
	defer lock.Close()
	credentials, err := readCredentials(options.CredentialsFile, options)
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err == nil {
		token, tokenErr := rotator.accessToken(ctx)
		if tokenErr != nil {
			return tokenErr
		}
		if err := rotator.mutate(ctx, token, http.MethodDelete, rotator.keyName(credentials.PrivateKeyID)); err != nil {
			return err
		}
	}
	for _, path := range rotator.artifacts() {
		if err := removeRegular(path); err != nil {
			return err
		}
	}
	return nil
}

func newKeyRotator(options KeyRotationOptions) (*keyRotator, error) {
	if options.ServiceAccount == "" || !strings.Contains(options.ServiceAccount, "@") || strings.ContainsAny(options.ServiceAccount, "\r\n/") {
		return nil, fmt.Errorf("invalid service account")
	}
	if options.ProjectID == "" || strings.ContainsAny(options.ProjectID, "\r\n/") {
		return nil, fmt.Errorf("invalid project id")
	}
	if err := validateAbsoluteFile(options.CredentialsFile); err != nil {
		return nil, err
	}
	if options.MinimumAge < 0 || options.DisableGrace < 0 {
		return nil, fmt.Errorf("rotation durations cannot be negative")
	}
	if options.HTTPClient == nil {
		options.HTTPClient = &http.Client{Timeout: 30 * time.Second}
	}
	if options.IAMBaseURL == "" {
		options.IAMBaseURL = defaultIAMBaseURL
	}
	if options.MetadataTokenURL == "" {
		options.MetadataTokenURL = defaultMetadataTokenURL
	}
	if options.Now == nil {
		options.Now = time.Now
	}
	return &keyRotator{options: options, client: options.HTTPClient, now: options.Now}, nil
}

func (r *keyRotator) converge(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(r.options.CredentialsFile), 0o750); err != nil {
		return fmt.Errorf("prepare credential directory: %w", err)
	}
	state, err := r.loadState()
	if errors.Is(err, os.ErrNotExist) {
		return r.begin(ctx)
	}
	if err != nil {
		return err
	}
	switch state.Phase {
	case "creating":
		return r.reconcileAmbiguousCreate(ctx, state)
	case "staged":
		return r.activate(ctx, state)
	case "ready":
		return r.disablePrevious(ctx, state)
	case "grace":
		return r.finishGrace(ctx, state)
	default:
		return fmt.Errorf("unsupported key rotation phase %q", state.Phase)
	}
}

func (r *keyRotator) begin(ctx context.Context) error {
	current, credentialErr := readCredentials(r.options.CredentialsFile, r.options)
	if credentialErr == nil && r.options.MinimumAge > 0 {
		info, err := os.Stat(r.options.CredentialsFile)
		if err != nil {
			return err
		}
		if r.now().Sub(info.ModTime()) < r.options.MinimumAge {
			return r.installCopies(ctx, false)
		}
	} else if credentialErr != nil && !errors.Is(credentialErr, os.ErrNotExist) {
		return credentialErr
	}
	token, err := r.accessToken(ctx)
	if err != nil {
		return err
	}
	keys, err := r.listKeys(ctx, token)
	if err != nil {
		return err
	}
	if credentialErr != nil && len(keys) > 0 {
		if !r.options.AllowOrphanReconcile || !validFreshMarker(r.options.FreshMarker, r.options.FreshIdentity) {
			return fmt.Errorf("remote user-managed keys exist without local credentials")
		}
		for _, key := range keys {
			if err := r.mutate(ctx, token, http.MethodDelete, key.Name); err != nil {
				return fmt.Errorf("reconcile orphaned key: %w", err)
			}
		}
		keys = nil
	}
	baseline := keyNames(keys)
	state := rotationState{Version: 1, Phase: "creating", ServiceAccount: r.options.ServiceAccount, ProjectID: r.options.ProjectID, Credentials: r.options.CredentialsFile, Baseline: baseline, CreatedAt: r.now().Unix()}
	if credentialErr == nil {
		state.CurrentKeyID = current.PrivateKeyID
	}
	if err := r.writeState(state); err != nil {
		return err
	}
	created, err := r.createKey(ctx, token)
	if err != nil {
		return fmt.Errorf("create replacement service-account key; durable creating state retained: %w", err)
	}
	credentials, err := decodeCredentials(created, r.options)
	if err != nil {
		_ = r.mutate(ctx, token, http.MethodDelete, created.Name)
		return err
	}
	state.Phase, state.NewKeyID, state.NewKeyName = "staged", credentials.PrivateKeyID, created.Name
	if err := installBytes(created.decoded, r.stagedPath(), "", "", 0o400); err != nil {
		return err
	}
	if err := r.writeState(state); err != nil {
		return err
	}
	return r.activate(ctx, state)
}

func (r *keyRotator) reconcileAmbiguousCreate(ctx context.Context, state rotationState) error {
	token, err := r.accessToken(ctx)
	if err != nil {
		return err
	}
	keys, err := r.listKeys(ctx, token)
	if err != nil {
		return err
	}
	baseline := make(map[string]bool, len(state.Baseline))
	for _, name := range state.Baseline {
		baseline[name] = true
	}
	var candidates []string
	for _, key := range keys {
		if !baseline[key.Name] {
			candidates = append(candidates, key.Name)
		}
	}
	if len(candidates) > 1 {
		return fmt.Errorf("ambiguous key creation produced %d candidates; operator review required", len(candidates))
	}
	if len(candidates) == 1 {
		if err := r.mutate(ctx, token, http.MethodDelete, candidates[0]); err != nil {
			return fmt.Errorf("remove unrecoverable replacement key: %w", err)
		}
	}
	if err := r.cleanupState(); err != nil {
		return err
	}
	return fmt.Errorf("reconciled an interrupted key creation; rerun to create a recoverable key")
}

func (r *keyRotator) activate(ctx context.Context, state rotationState) error {
	credentials, err := readCredentials(r.stagedPath(), r.options)
	if err != nil || credentials.PrivateKeyID != state.NewKeyID {
		return fmt.Errorf("staged credentials are missing or do not match rotation state")
	}
	if err := r.authenticate(ctx, credentials); err != nil {
		return fmt.Errorf("authenticate replacement key: %w", err)
	}
	if state.CurrentKeyID != "" {
		if _, err := os.Stat(r.previousPath()); errors.Is(err, os.ErrNotExist) {
			contents, readErr := os.ReadFile(r.options.CredentialsFile)
			if readErr != nil {
				return fmt.Errorf("preserve previous credentials: %w", readErr)
			}
			if err := installBytes(contents, r.previousPath(), "", "", 0o400); err != nil {
				return err
			}
		}
	}
	contents, err := os.ReadFile(r.stagedPath())
	if err != nil {
		return err
	}
	if err := installBytes(contents, r.options.CredentialsFile, r.options.Owner, r.options.Group, 0o440); err != nil {
		return err
	}
	if err := r.installCopies(ctx, true); err != nil {
		return err
	}
	state.Phase = "ready"
	if err := r.writeState(state); err != nil {
		return err
	}
	_ = removeRegular(r.stagedPath())
	return r.disablePrevious(ctx, state)
}

func (r *keyRotator) disablePrevious(ctx context.Context, state rotationState) error {
	if state.CurrentKeyID == "" {
		return r.cleanupState()
	}
	token, err := r.accessToken(ctx)
	if err != nil {
		return err
	}
	if err := r.mutate(ctx, token, http.MethodPost, r.keyName(state.CurrentKeyID)+":disable"); err != nil {
		return fmt.Errorf("disable previous key: %w", err)
	}
	state.Phase, state.DisabledAt = "grace", r.now().Unix()
	if err := r.writeState(state); err != nil {
		return err
	}
	return r.finishGrace(ctx, state)
}

func (r *keyRotator) finishGrace(ctx context.Context, state rotationState) error {
	if r.now().Sub(time.Unix(state.DisabledAt, 0)) < r.options.DisableGrace {
		return nil
	}
	token, err := r.accessToken(ctx)
	if err != nil {
		return err
	}
	if err := r.mutate(ctx, token, http.MethodDelete, r.keyName(state.CurrentKeyID)); err != nil {
		return fmt.Errorf("delete previous key after grace: %w", err)
	}
	return r.cleanupState()
}

func (r *keyRotator) rollback(ctx context.Context) error {
	state, err := r.loadState()
	if err != nil {
		return err
	}
	if state.Phase != "grace" || state.CurrentKeyID == "" {
		return fmt.Errorf("rollback is available only during the previous key grace period")
	}
	previous, err := os.ReadFile(r.previousPath())
	if err != nil {
		return fmt.Errorf("read previous credentials: %w", err)
	}
	token, err := r.accessToken(ctx)
	if err != nil {
		return err
	}
	if err := r.mutate(ctx, token, http.MethodPost, r.keyName(state.CurrentKeyID)+":enable"); err != nil {
		return fmt.Errorf("enable rollback key: %w", err)
	}
	if err := installBytes(previous, r.options.CredentialsFile, r.options.Owner, r.options.Group, 0o440); err != nil {
		return err
	}
	if err := r.installCopies(ctx, true); err != nil {
		return err
	}
	if err := r.mutate(ctx, token, http.MethodDelete, state.NewKeyName); err != nil {
		return fmt.Errorf("delete abandoned replacement key: %w", err)
	}
	return r.cleanupState()
}

func (r *keyRotator) installCopies(ctx context.Context, restart bool) error {
	contents, err := os.ReadFile(r.options.CredentialsFile)
	if err != nil {
		return err
	}
	changed := false
	for _, copy := range r.options.Copies {
		current, readErr := os.ReadFile(copy.Path)
		if readErr == nil && string(current) == string(contents) {
			continue
		}
		if readErr != nil && !errors.Is(readErr, os.ErrNotExist) {
			return readErr
		}
		if err := installBytes(contents, copy.Path, copy.Owner, copy.Group, 0o440); err != nil {
			return err
		}
		changed = true
	}
	if restart && changed && r.options.RestartUnit != "" {
		active := exec.CommandContext(ctx, "systemctl", "is-active", "--quiet", r.options.RestartUnit)
		if active.Run() == nil {
			command := exec.CommandContext(ctx, "systemctl", "restart", r.options.RestartUnit)
			command.Stdout, command.Stderr = r.options.Stdout, r.options.Stdout
			if err := command.Run(); err != nil {
				return fmt.Errorf("restart %s: %w", r.options.RestartUnit, err)
			}
		}
	}
	return nil
}

type createdKey struct {
	Name           string `json:"name"`
	PrivateKeyData string `json:"privateKeyData"`
	decoded        []byte
}

func (r *keyRotator) createKey(ctx context.Context, token string) (createdKey, error) {
	body := strings.NewReader(`{"privateKeyType":"TYPE_GOOGLE_CREDENTIALS_FILE","keyAlgorithm":"KEY_ALG_RSA_2048"}`)
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, r.resourceURL("projects/-/serviceAccounts/"+url.PathEscape(r.options.ServiceAccount)+"/keys"), body)
	if err != nil {
		return createdKey{}, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return createdKey{}, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return createdKey{}, fmt.Errorf("IAM key create returned HTTP %d", response.StatusCode)
	}
	var created createdKey
	if err := decodeLimited(response.Body, &created); err != nil {
		return createdKey{}, err
	}
	created.decoded, err = base64.StdEncoding.DecodeString(created.PrivateKeyData)
	if err != nil || created.Name == "" {
		return createdKey{}, fmt.Errorf("IAM returned invalid replacement credentials")
	}
	return created, nil
}

func (r *keyRotator) listKeys(ctx context.Context, token string) ([]iamKey, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.resourceURL("projects/-/serviceAccounts/"+url.PathEscape(r.options.ServiceAccount)+"/keys?keyTypes=USER_MANAGED"), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	response, err := r.client.Do(request)
	if err != nil {
		return nil, err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return nil, fmt.Errorf("IAM key list returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		Keys []iamKey `json:"keys"`
	}
	if err := decodeLimited(response.Body, &payload); err != nil {
		return nil, err
	}
	prefix := "projects/" + r.options.ProjectID + "/serviceAccounts/" + r.options.ServiceAccount + "/keys/"
	filtered := payload.Keys[:0]
	for _, key := range payload.Keys {
		if key.KeyType == "USER_MANAGED" && strings.HasPrefix(key.Name, prefix) && validKeyID(strings.TrimPrefix(key.Name, prefix)) {
			filtered = append(filtered, key)
		}
	}
	return filtered, nil
}

func (r *keyRotator) mutate(ctx context.Context, token, method, name string) error {
	request, err := http.NewRequestWithContext(ctx, method, r.resourceURL(name), strings.NewReader("{}"))
	if err != nil {
		return err
	}
	request.Header.Set("Authorization", "Bearer "+token)
	request.Header.Set("Content-Type", "application/json")
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode == http.StatusNotFound || response.StatusCode/100 == 2 {
		return nil
	}
	return fmt.Errorf("IAM mutation returned HTTP %d", response.StatusCode)
}

func (r *keyRotator) accessToken(ctx context.Context) (string, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, r.options.MetadataTokenURL, nil)
	if err != nil {
		return "", err
	}
	request.Header.Set("Metadata-Flavor", "Google")
	response, err := r.client.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return "", fmt.Errorf("metadata token request returned HTTP %d", response.StatusCode)
	}
	var payload struct {
		AccessToken string `json:"access_token"`
	}
	if err := decodeLimited(response.Body, &payload); err != nil || payload.AccessToken == "" {
		return "", fmt.Errorf("metadata server returned an invalid access token")
	}
	return payload.AccessToken, nil
}

func (r *keyRotator) authenticate(ctx context.Context, credentials serviceAccountCredentials) error {
	block, _ := pem.Decode([]byte(credentials.PrivateKey))
	if block == nil {
		return fmt.Errorf("private key is not PEM")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return err
	}
	privateKey, ok := parsed.(*rsa.PrivateKey)
	if !ok {
		return fmt.Errorf("service-account private key is not RSA")
	}
	now := r.now().Unix()
	header, _ := json.Marshal(map[string]string{"alg": "RS256", "typ": "JWT", "kid": credentials.PrivateKeyID})
	claims, _ := json.Marshal(map[string]any{"iss": credentials.ClientEmail, "scope": "https://www.googleapis.com/auth/cloud-platform", "aud": credentials.TokenURI, "iat": now, "exp": now + 3600})
	encode := base64.RawURLEncoding.EncodeToString
	signingInput := encode(header) + "." + encode(claims)
	digest := sha256.Sum256([]byte(signingInput))
	signature, err := rsa.SignPKCS1v15(rand.Reader, privateKey, crypto.SHA256, digest[:])
	if err != nil {
		return err
	}
	form := url.Values{"grant_type": {"urn:ietf:params:oauth:grant-type:jwt-bearer"}, "assertion": {signingInput + "." + encode(signature)}}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, credentials.TokenURI, strings.NewReader(form.Encode()))
	if err != nil {
		return err
	}
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := r.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode/100 != 2 {
		return fmt.Errorf("service-account token exchange returned HTTP %d", response.StatusCode)
	}
	return nil
}

func (r *keyRotator) loadState() (rotationState, error) {
	contents, err := readSingleLinkFile(r.statePath(), 1<<20)
	if err != nil {
		return rotationState{}, err
	}
	var state rotationState
	decoder := json.NewDecoder(strings.NewReader(string(contents)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&state); err != nil {
		return rotationState{}, fmt.Errorf("decode key rotation state: %w", err)
	}
	if state.Version != 1 || state.ServiceAccount != r.options.ServiceAccount || state.ProjectID != r.options.ProjectID || state.Credentials != r.options.CredentialsFile {
		return rotationState{}, fmt.Errorf("key rotation state belongs to another target")
	}
	return state, nil
}

func (r *keyRotator) writeState(state rotationState) error {
	contents, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeAtomic(r.statePath(), append(contents, '\n'), 0o600)
}

func (r *keyRotator) cleanupState() error {
	for _, path := range []string{r.statePath(), r.stagedPath(), r.previousPath()} {
		if err := removeRegular(path); err != nil {
			return err
		}
	}
	return nil
}

func (r *keyRotator) artifacts() []string {
	paths := []string{r.options.CredentialsFile, r.statePath(), r.stagedPath(), r.previousPath(), r.options.CredentialsFile + ".rotation.lock"}
	for _, copy := range r.options.Copies {
		paths = append(paths, copy.Path)
	}
	return paths
}

func (r *keyRotator) statePath() string  { return r.options.CredentialsFile + ".rotation-pending.json" }
func (r *keyRotator) stagedPath() string { return r.options.CredentialsFile + ".rotation-staged.json" }
func (r *keyRotator) previousPath() string {
	return r.options.CredentialsFile + ".rotation-previous.json"
}
func (r *keyRotator) keyName(id string) string {
	return "projects/" + r.options.ProjectID + "/serviceAccounts/" + r.options.ServiceAccount + "/keys/" + id
}
func (r *keyRotator) resourceURL(resource string) string {
	return strings.TrimRight(r.options.IAMBaseURL, "/") + "/" + strings.TrimLeft(resource, "/")
}

func decodeCredentials(created createdKey, options KeyRotationOptions) (serviceAccountCredentials, error) {
	var credentials serviceAccountCredentials
	if err := json.Unmarshal(created.decoded, &credentials); err != nil {
		return credentials, err
	}
	if err := validateCredentials(credentials, options); err != nil {
		return credentials, err
	}
	return credentials, nil
}

func readCredentials(path string, options KeyRotationOptions) (serviceAccountCredentials, error) {
	contents, err := readSingleLinkFile(path, 1<<20)
	if err != nil {
		return serviceAccountCredentials{}, err
	}
	var credentials serviceAccountCredentials
	if err := json.Unmarshal(contents, &credentials); err != nil {
		return credentials, fmt.Errorf("decode credentials: %w", err)
	}
	if err := validateCredentials(credentials, options); err != nil {
		return credentials, err
	}
	return credentials, nil
}

func validateCredentials(credentials serviceAccountCredentials, options KeyRotationOptions) error {
	if credentials.Type != "service_account" || credentials.ProjectID != options.ProjectID || credentials.ClientEmail != options.ServiceAccount || !validKeyID(credentials.PrivateKeyID) || credentials.PrivateKey == "" {
		return fmt.Errorf("credentials do not match the managed service account")
	}
	parsed, err := url.Parse(credentials.TokenURI)
	if err != nil || parsed.Scheme != "https" && parsed.Scheme != "http" || parsed.Host == "" {
		return fmt.Errorf("credentials contain an invalid token URI")
	}
	return nil
}

func installBytes(contents []byte, path, ownerName, groupName string, mode os.FileMode) error {
	if err := validateAbsoluteFile(path); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return err
	}
	directory := filepath.Dir(path)
	resolved, err := filepath.EvalSymlinks(directory)
	if err != nil || resolved != directory {
		return fmt.Errorf("credential directory contains a symbolic link: %s", directory)
	}
	if info, err := os.Lstat(path); err == nil && (!info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("credential target is unsafe: %s", path)
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".sitectl-credential-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if _, err := temporary.Write(contents); err != nil {
		_ = temporary.Close()
		return err
	}
	uid, gid, err := resolveOwner(ownerName, groupName)
	if err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Chmod(mode); err != nil {
		_ = temporary.Close()
		return err
	}
	if (uid >= 0 || gid >= 0) && syscall.Fchown(int(temporary.Fd()), uid, gid) != nil {
		_ = temporary.Close()
		return fmt.Errorf("set credential ownership")
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, path)
}

func resolveOwner(ownerName, groupName string) (int, int, error) {
	uid, gid := -1, -1
	if ownerName != "" {
		account, err := user.Lookup(ownerName)
		if err != nil {
			if _, parseErr := strconv.Atoi(ownerName); parseErr != nil {
				return -1, -1, err
			}
			uid, _ = strconv.Atoi(ownerName)
		} else {
			uid, _ = strconv.Atoi(account.Uid)
		}
	}
	if groupName != "" {
		group, err := user.LookupGroup(groupName)
		if err != nil {
			if _, parseErr := strconv.Atoi(groupName); parseErr != nil {
				return -1, -1, err
			}
			gid, _ = strconv.Atoi(groupName)
		} else {
			gid, _ = strconv.Atoi(group.Gid)
		}
	}
	return uid, gid, nil
}

func readSingleLinkFile(path string, limit int64) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || stat.Nlink != 1 || info.Size() > limit {
		return nil, fmt.Errorf("unsafe file: %s", path)
	}
	return os.ReadFile(path)
}

func removeRegular(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("refusing to remove unsafe artifact: %s", path)
	}
	return os.Remove(path)
}

func validateAbsoluteFile(path string) error {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == string(filepath.Separator) || strings.ContainsAny(path, "\r\n") {
		return fmt.Errorf("unsafe absolute file path: %s", path)
	}
	return nil
}

func validFreshMarker(path, identity string) bool {
	if path == "" || identity == "" || !strings.HasPrefix(identity, "v1:gcp-disk-id:") {
		return false
	}
	contents, err := readSingleLinkFile(path, 128)
	return err == nil && string(contents) == identity+"\n"
}

func validKeyID(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if character != '_' && character != '-' && (character < 'a' || character > 'z') && (character < 'A' || character > 'Z') && (character < '0' || character > '9') {
			return false
		}
	}
	return true
}

func keyNames(keys []iamKey) []string {
	names := make([]string, 0, len(keys))
	for _, key := range keys {
		names = append(names, key.Name)
	}
	sort.Strings(names)
	return names
}

func decodeLimited(reader io.Reader, target any) error {
	decoder := json.NewDecoder(io.LimitReader(reader, 1<<20))
	return decoder.Decode(target)
}
