package execution

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBrokerCAIsBrowserCompatibleAndStable(t *testing.T) {
	state := t.TempDir()
	first := &Broker{}
	if err := first.loadCA(state); err != nil {
		t.Fatal(err)
	}
	if first.ca.PublicKeyAlgorithm != x509.ECDSA || first.ca.SignatureAlgorithm != x509.ECDSAWithSHA256 {
		t.Fatalf("unsupported browser CA algorithms: %s / %s", first.ca.PublicKeyAlgorithm, first.ca.SignatureAlgorithm)
	}
	second := &Broker{}
	if err := second.loadCA(state); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(first.caPEM, second.caPEM) {
		t.Fatal("restart silently replaced the CA")
	}
	leaf, err := second.certificate("fixture.test")
	if err != nil {
		t.Fatal(err)
	}
	cert, err := x509.ParseCertificate(leaf.Certificate[0])
	if err != nil {
		t.Fatal(err)
	}
	roots := x509.NewCertPool()
	roots.AddCert(first.ca)
	if _, err := cert.Verify(x509.VerifyOptions{Roots: roots, DNSName: "fixture.test"}); err != nil {
		t.Fatal(err)
	}
	// An operator copying the wrong private key must fail closed at startup.
	otherKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(otherKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(state, "ca-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}), 0600); err != nil {
		t.Fatal(err)
	}
	if err := (&Broker{}).loadCA(state); err == nil || !strings.Contains(err.Error(), "mismatch") {
		t.Fatalf("mismatched key accepted: %v", err)
	}
}
