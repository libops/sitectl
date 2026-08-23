package hostruntime

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"gopkg.in/yaml.v3"
)

type composeDocument struct {
	Secrets map[string]struct {
		File string `yaml:"file"`
	} `yaml:"secrets"`
}

func scaffoldApplicationDefaults(app Application) error {
	files, err := composeSecretFiles(app.ProjectDir)
	if err != nil {
		return err
	}
	for _, path := range files {
		if err := scaffoldFile(path); err != nil {
			return err
		}
	}
	return nil
}

func composeSecretFiles(projectDir string) ([]string, error) {
	seen := map[string]struct{}{}
	for _, name := range []string{"compose.yaml", "compose.yml", "docker-compose.yaml", "docker-compose.yml"} {
		path := filepath.Join(projectDir, name)
		info, err := os.Lstat(path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("compose control file is unsafe: %s", path)
		}
		contents, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var document composeDocument
		if err := yaml.Unmarshal(contents, &document); err != nil {
			return nil, fmt.Errorf("decode %s: %w", path, err)
		}
		for _, secret := range document.Secrets {
			if strings.TrimSpace(secret.File) == "" {
				continue
			}
			target := secret.File
			if !filepath.IsAbs(target) {
				target = filepath.Join(projectDir, target)
			}
			target = filepath.Clean(target)
			relative, err := filepath.Rel(projectDir, target)
			if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
				return nil, fmt.Errorf("compose secret file escapes project directory: %s", secret.File)
			}
			seen[target] = struct{}{}
		}
	}
	files := make([]string, 0, len(seen))
	for path := range seen {
		files = append(files, path)
	}
	sort.Strings(files)
	return files, nil
}

func scaffoldFile(path string) error {
	if info, err := os.Lstat(path); err == nil {
		metadata, ok := info.Sys().(*syscall.Stat_t)
		if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 || !ok || metadata.Nlink != 1 {
			return fmt.Errorf("secret destination is unsafe: %s", path)
		}
		if info.Size() > 0 {
			return nil
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := makeSafeDirectories(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	switch filepath.Base(path) {
	case "UID":
		return writeAtomic(path, []byte(fmt.Sprintf("%d\n", os.Geteuid())), 0o644)
	case "DRUPAL_DEFAULT_SALT":
		value, err := randomString("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789_-", 74)
		if err != nil {
			return err
		}
		return writeAtomic(path, []byte(value+"\n"), 0o640)
	case "JWT_PRIVATE_KEY", "JWT_PUBLIC_KEY":
		return scaffoldJWTKeys(filepath.Join(filepath.Dir(path), "JWT_PRIVATE_KEY"), filepath.Join(filepath.Dir(path), "JWT_PUBLIC_KEY"))
	case "cert.pem", "rootCA.pem", "privkey.pem", "rootCA-key.pem":
		return scaffoldLocalCertificates(filepath.Dir(path))
	default:
		value, err := randomString("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789", 32)
		if err != nil {
			return err
		}
		return writeAtomic(path, []byte(value+"\n"), 0o640)
	}
}

func randomString(alphabet string, length int) (string, error) {
	output := make([]byte, length)
	limit := big.NewInt(int64(len(alphabet)))
	for index := range output {
		value, err := rand.Int(rand.Reader, limit)
		if err != nil {
			return "", err
		}
		output[index] = alphabet[value.Int64()]
	}
	return string(output), nil
}

func scaffoldJWTKeys(privatePath, publicPath string) error {
	if fileNonempty(privatePath) && fileNonempty(publicPath) {
		return nil
	}
	var key *rsa.PrivateKey
	var err error
	if fileNonempty(privatePath) {
		key, err = readRSAPrivateKey(privatePath)
	} else {
		key, err = rsa.GenerateKey(rand.Reader, 2048)
	}
	if err != nil {
		return err
	}
	if !fileNonempty(privatePath) {
		privateBytes, marshalErr := x509.MarshalPKCS8PrivateKey(key)
		if marshalErr != nil {
			return marshalErr
		}
		if err := writeAtomic(privatePath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateBytes}), 0o640); err != nil {
			return err
		}
	}
	publicBytes, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return err
	}
	return writeAtomic(publicPath, pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: publicBytes}), 0o644)
}

func scaffoldLocalCertificates(directory string) error {
	certPath, rootPath := filepath.Join(directory, "cert.pem"), filepath.Join(directory, "rootCA.pem")
	if fileNonempty(certPath) && fileNonempty(rootPath) {
		return nil
	}
	now := time.Now().UTC()
	caKeyPath := filepath.Join(directory, "rootCA-key.pem")
	var caKey *rsa.PrivateKey
	var caTemplate x509.Certificate
	var caDER []byte
	var err error
	if fileNonempty(rootPath) && fileNonempty(caKeyPath) {
		caKey, err = readRSAPrivateKey(caKeyPath)
		if err != nil {
			return err
		}
		caTemplate, err = readCertificate(rootPath)
		if err != nil {
			return err
		}
	} else {
		caKey, err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return err
		}
		serial, serialErr := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
		if serialErr != nil {
			return serialErr
		}
		caTemplate = x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "cloud-compose local root"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(10, 0, 0), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature}
		caDER, err = x509.CreateCertificate(rand.Reader, &caTemplate, &caTemplate, &caKey.PublicKey, caKey)
		if err != nil {
			return err
		}
	}
	leafKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return err
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return err
	}
	leafTemplate := x509.Certificate{SerialNumber: serial, Subject: pkix.Name{CommonName: "localhost"}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(2, 3, 0), KeyUsage: x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, DNSNames: []string{"localhost", "*.localhost", "islandora.io", "*.islandora.io", "islandora.info", "*.islandora.info"}, IPAddresses: []net.IP{net.ParseIP("127.0.0.1"), net.ParseIP("::1")}}
	leafDER, err := x509.CreateCertificate(rand.Reader, &leafTemplate, &caTemplate, &leafKey.PublicKey, caKey)
	if err != nil {
		return err
	}
	privateBytes, _ := x509.MarshalPKCS8PrivateKey(leafKey)
	files := []struct {
		path  string
		block *pem.Block
		mode  os.FileMode
	}{
		{certPath, &pem.Block{Type: "CERTIFICATE", Bytes: leafDER}, 0o644},
		{filepath.Join(directory, "privkey.pem"), &pem.Block{Type: "PRIVATE KEY", Bytes: privateBytes}, 0o640},
	}
	if len(caDER) > 0 {
		caPrivateBytes, _ := x509.MarshalPKCS8PrivateKey(caKey)
		files = append([]struct {
			path  string
			block *pem.Block
			mode  os.FileMode
		}{{rootPath, &pem.Block{Type: "CERTIFICATE", Bytes: caDER}, 0o644}, {caKeyPath, &pem.Block{Type: "PRIVATE KEY", Bytes: caPrivateBytes}, 0o640}}, files...)
	}
	for _, file := range files {
		if err := writeAtomic(file.path, pem.EncodeToMemory(file.block), file.mode); err != nil {
			return err
		}
	}
	return nil
}

func readRSAPrivateKey(path string) (*rsa.PrivateKey, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(contents)
	if block == nil {
		return nil, fmt.Errorf("private key is not PEM: %s", path)
	}
	if key, err := x509.ParsePKCS8PrivateKey(block.Bytes); err == nil {
		if rsaKey, ok := key.(*rsa.PrivateKey); ok {
			return rsaKey, nil
		}
	}
	if key, err := x509.ParsePKCS1PrivateKey(block.Bytes); err == nil {
		return key, nil
	}
	return nil, fmt.Errorf("private key is not RSA: %s", path)
}

func readCertificate(path string) (x509.Certificate, error) {
	contents, err := os.ReadFile(path)
	if err != nil {
		return x509.Certificate{}, err
	}
	block, _ := pem.Decode(contents)
	if block == nil {
		return x509.Certificate{}, fmt.Errorf("certificate is not PEM: %s", path)
	}
	certificate, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return x509.Certificate{}, err
	}
	return *certificate, nil
}

func fileNonempty(path string) bool {
	info, err := os.Lstat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode()&os.ModeSymlink == 0 && info.Size() > 0
}

func makeSafeDirectories(path string, mode os.FileMode) error {
	if err := os.MkdirAll(path, mode); err != nil {
		return err
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return fmt.Errorf("secret directory contains a symbolic link: %s", path)
	}
	return os.Chmod(path, mode)
}
