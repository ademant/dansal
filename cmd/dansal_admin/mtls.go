package main

import (
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"gopkg.in/yaml.v2"
	"software.sslmate.com/src/go-pkcs12"
)

const defaultPKIDir = "/var/lib/dansal/pki"

func pkiDir() string {
	data, err := os.ReadFile(defaultConfigPath)
	if err != nil {
		return defaultPKIDir
	}
	var cfg struct {
		Server struct {
			PKIDir string `yaml:"pki_dir"`
		} `yaml:"server"`
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil || cfg.Server.PKIDir == "" {
		return defaultPKIDir
	}
	return cfg.Server.PKIDir
}

func writePEM(path, pemType string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: pemType, Bytes: data})
}

func loadCACert(dir string) (*x509.Certificate, *rsa.PrivateKey, error) {
	keyPEM, err := os.ReadFile(filepath.Join(dir, "ca.key"))
	if err != nil {
		return nil, nil, fmt.Errorf("read ca.key: %w", err)
	}
	block, _ := pem.Decode(keyPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("invalid ca.key PEM")
	}
	caKey, err := x509.ParsePKCS1PrivateKey(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse ca.key: %w", err)
	}

	certPEM, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		return nil, nil, fmt.Errorf("read ca.crt: %w", err)
	}
	block, _ = pem.Decode(certPEM)
	if block == nil {
		return nil, nil, fmt.Errorf("invalid ca.crt PEM")
	}
	caCert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("parse ca.crt: %w", err)
	}
	return caCert, caKey, nil
}

func loadCRL(dir string) (*x509.RevocationList, error) {
	data, err := os.ReadFile(filepath.Join(dir, "crl.pem"))
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, nil
	}
	return x509.ParseRevocationList(block.Bytes)
}

func revokedSerials(crl *x509.RevocationList) map[string]bool {
	out := make(map[string]bool)
	if crl == nil {
		return out
	}
	for _, rc := range crl.RevokedCertificateEntries {
		out[rc.SerialNumber.String()] = true
	}
	return out
}

func regenerateCRL(dir string, caCert *x509.Certificate, caKey *rsa.PrivateKey, revoked []pkix.RevokedCertificate) error {
	entries := make([]x509.RevocationListEntry, len(revoked))
	for i, rc := range revoked {
		entries[i] = x509.RevocationListEntry{
			SerialNumber:   rc.SerialNumber,
			RevocationTime: rc.RevocationTime,
		}
	}
	now := time.Now()
	tpl := &x509.RevocationList{
		Number:              big.NewInt(now.Unix()),
		ThisUpdate:          now,
		NextUpdate:          now.Add(365 * 24 * time.Hour),
		RevokedCertificateEntries: entries,
	}
	der, err := x509.CreateRevocationList(rand.Reader, tpl, caCert, caKey)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(filepath.Join(dir, "crl.pem"), os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return err
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: "X509 CRL", Bytes: der})
}

func cmdMTLSInit(args []string) {
	fs := flag.NewFlagSet("mtls-init", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["mtls-init"]) }
	fs.Parse(args)

	dir := pkiDir()
	crtPath := filepath.Join(dir, "ca.crt")

	if _, err := os.Stat(crtPath); err == nil {
		die("CA already exists at %s — use mtls-ca-cert to view it", crtPath)
	}

	if err := os.MkdirAll(filepath.Join(dir, "issued"), 0750); err != nil {
		die("create pki dir: %v", err)
	}

	fmt.Fprintln(os.Stderr, "Generating RSA-4096 CA key (this takes a moment)...")
	caKey, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		die("generate CA key: %v", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "dansal CA",
			Organization: []string{"dansal"},
		},
		NotBefore:             now,
		NotAfter:              now.Add(10 * 365 * 24 * time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageCRLSign,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, tpl, tpl, &caKey.PublicKey, caKey)
	if err != nil {
		die("create CA cert: %v", err)
	}

	if err := writePEM(filepath.Join(dir, "ca.key"), "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(caKey)); err != nil {
		die("write ca.key: %v", err)
	}
	if err := writePEM(filepath.Join(dir, "ca.crt"), "CERTIFICATE", caDER); err != nil {
		die("write ca.crt: %v", err)
	}

	// Create empty CRL
	caCert, err := x509.ParseCertificate(caDER)
	if err != nil {
		die("parse new CA cert: %v", err)
	}
	if err := regenerateCRL(dir, caCert, caKey, nil); err != nil {
		die("create initial CRL: %v", err)
	}

	fmt.Printf("CA initialised.\n  CA cert: %s\n  CA key:  %s\n  CRL:     %s\n",
		filepath.Join(dir, "ca.crt"),
		filepath.Join(dir, "ca.key"),
		filepath.Join(dir, "crl.pem"),
	)
}

func cmdMTLSIssue(args []string) {
	fs := flag.NewFlagSet("mtls-issue", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["mtls-issue"]) }
	username := fs.String("username", "", "username (CN of the cert)")
	days := fs.Int("days", 3*365, "validity in days")
	password := fs.String("password", "", "PKCS12 import password (empty = no password)")
	fs.Parse(args)

	if *username == "" {
		die("--username is required")
	}

	dir := pkiDir()
	caCert, caKey, err := loadCACert(dir)
	if err != nil {
		die("load CA: %v", err)
	}

	fmt.Fprintln(os.Stderr, "Generating RSA-2048 client key...")
	clientKey, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		die("generate key: %v", err)
	}

	serial, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	now := time.Now()
	tpl := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   *username,
			Organization: []string{"dansal"},
		},
		NotBefore: now,
		NotAfter:  now.Add(time.Duration(*days) * 24 * time.Hour),
		KeyUsage:  x509.KeyUsageDigitalSignature,
		ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	certDER, err := x509.CreateCertificate(rand.Reader, tpl, caCert, &clientKey.PublicKey, caKey)
	if err != nil {
		die("create cert: %v", err)
	}
	clientCert, _ := x509.ParseCertificate(certDER)

	issuedDir := filepath.Join(dir, "issued")
	if err := os.MkdirAll(issuedDir, 0750); err != nil {
		die("create issued dir: %v", err)
	}

	keyPath := filepath.Join(issuedDir, *username+".key")
	crtPath := filepath.Join(issuedDir, *username+".crt")
	p12Path := filepath.Join(issuedDir, *username+".p12")

	if err := writePEM(keyPath, "RSA PRIVATE KEY", x509.MarshalPKCS1PrivateKey(clientKey)); err != nil {
		die("write key: %v", err)
	}
	if err := writePEM(crtPath, "CERTIFICATE", certDER); err != nil {
		die("write cert: %v", err)
	}

	p12Data, err := pkcs12.Legacy.Encode(clientKey, clientCert, []*x509.Certificate{caCert}, *password)
	if err != nil {
		die("encode p12: %v", err)
	}
	if err := os.WriteFile(p12Path, p12Data, 0600); err != nil {
		die("write p12: %v", err)
	}

	fmt.Printf("Issued cert for %q.\n  Key:  %s\n  Cert: %s\n  P12:  %s\n  Expiry: %s\n",
		*username, keyPath, crtPath, p12Path, clientCert.NotAfter.Format("2006-01-02"))
}

func cmdMTLSRevoke(args []string) {
	fs := flag.NewFlagSet("mtls-revoke", flag.ExitOnError)
	fs.Usage = func() { fmt.Println(commandHelp["mtls-revoke"]) }
	username := fs.String("username", "", "username whose cert to revoke")
	fs.Parse(args)

	if *username == "" {
		die("--username is required")
	}

	dir := pkiDir()
	caCert, caKey, err := loadCACert(dir)
	if err != nil {
		die("load CA: %v", err)
	}

	crtPath := filepath.Join(dir, "issued", *username+".crt")
	certPEM, err := os.ReadFile(crtPath)
	if err != nil {
		die("read cert %s: %v", crtPath, err)
	}
	block, _ := pem.Decode(certPEM)
	if block == nil {
		die("invalid cert PEM at %s", crtPath)
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		die("parse cert: %v", err)
	}

	// Load existing CRL entries
	existingCRL, _ := loadCRL(dir)
	existing := make([]pkix.RevokedCertificate, 0)
	if existingCRL != nil {
		for _, rc := range existingCRL.RevokedCertificateEntries {
			existing = append(existing, pkix.RevokedCertificate{
				SerialNumber:   rc.SerialNumber,
				RevocationTime: rc.RevocationTime,
			})
		}
	}

	// Check if already revoked
	alreadyRevoked := false
	for _, rc := range existing {
		if rc.SerialNumber.Cmp(cert.SerialNumber) == 0 {
			alreadyRevoked = true
			break
		}
	}
	if alreadyRevoked {
		die("cert for %q is already revoked", *username)
	}

	existing = append(existing, pkix.RevokedCertificate{
		SerialNumber:   cert.SerialNumber,
		RevocationTime: time.Now(),
	})

	if err := regenerateCRL(dir, caCert, caKey, existing); err != nil {
		die("regenerate CRL: %v", err)
	}

	fmt.Printf("Revoked cert for %q (serial %s). CRL updated.\n", *username, cert.SerialNumber)
}

func cmdMTLSList(args []string) {
	dir := pkiDir()
	issuedDir := filepath.Join(dir, "issued")

	crl, _ := loadCRL(dir)
	revoked := revokedSerials(crl)

	entries, err := os.ReadDir(issuedDir)
	if err != nil {
		if os.IsNotExist(err) {
			fmt.Println("No certs issued yet.")
			return
		}
		die("read issued dir: %v", err)
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "USERNAME\tSERIAL\tISSUED\tEXPIRY\tSTATUS")
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".crt") {
			continue
		}
		username := strings.TrimSuffix(e.Name(), ".crt")
		certPEM, err := os.ReadFile(filepath.Join(issuedDir, e.Name()))
		if err != nil {
			continue
		}
		block, _ := pem.Decode(certPEM)
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			continue
		}
		status := "valid"
		if revoked[cert.SerialNumber.String()] {
			status = "REVOKED"
		} else if time.Now().After(cert.NotAfter) {
			status = "expired"
		}
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			username,
			cert.SerialNumber.String(),
			cert.NotBefore.Format("2006-01-02"),
			cert.NotAfter.Format("2006-01-02"),
			status,
		)
	}
	w.Flush()
}

func cmdMTLSCACert(args []string) {
	dir := pkiDir()
	data, err := os.ReadFile(filepath.Join(dir, "ca.crt"))
	if err != nil {
		die("read ca.crt: %v", err)
	}
	os.Stdout.Write(data)
}
