package server

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/BViganotti/redlite/internal/config"
	"github.com/BViganotti/redlite/internal/resp"
)

// writeTestCertificate creates a self-signed certificate valid for 127.0.0.1
// and returns the certificate, key and CA paths.
func writeTestCertificate(t *testing.T) (certFile, keyFile, caFile string) {
	t.Helper()
	dir := t.TempDir()

	privateKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "redlite-test"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(24 * time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
		IPAddresses:           []net.IP{net.ParseIP("127.0.0.1")},
		DNSNames:              []string{"localhost"},
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &privateKey.PublicKey, privateKey)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})

	keyDER, err := x509.MarshalECPrivateKey(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	certFile = filepath.Join(dir, "cert.pem")
	keyFile = filepath.Join(dir, "key.pem")
	caFile = filepath.Join(dir, "ca.pem")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(caFile, certPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatal(err)
	}
	return certFile, keyFile, caFile
}

func freePort(t *testing.T) int {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	_ = listener.Close()
	return port
}

func tlsConfig() config.Config {
	cfg := config.Default()
	cfg.Bind = "127.0.0.1"
	cfg.Port = 0
	cfg.LogLevel = "error"
	return cfg
}

func TestTLSListener(t *testing.T) {
	certFile, keyFile, _ := writeTestCertificate(t)

	cfg := tlsConfig()
	cfg.Dir = t.TempDir()
	cfg.TLSPort = freePort(t)
	cfg.TLSCertFile = certFile
	cfg.TLSKeyFile = keyFile
	srv := startServer(t, cfg)

	conn, err := tls.Dial("tcp", srv.TLSAddr().String(), &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("TLS dial: %v", err)
	}
	defer conn.Close()

	if _, err := conn.Write(resp.AppendCommand(nil, [][]byte{[]byte("PING")})); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if line != "+PONG\r\n" {
		t.Fatalf("TLS PING reply = %q", line)
	}

	// The plain listener still serves.
	plain := dial(t, srv)
	expectSimple(t, plain.do("PING"), "PONG")
	info := string(plain.do("INFO", "server").([]byte))
	if !strings.Contains(info, "tls_port:"+strconv.Itoa(cfg.TLSPort)) {
		t.Fatalf("INFO missing tls_port: %s", info)
	}
}

func TestTLSReplication(t *testing.T) {
	certFile, keyFile, caFile := writeTestCertificate(t)

	masterCfg := tlsConfig()
	masterCfg.Dir = t.TempDir()
	masterCfg.TLSPort = freePort(t)
	masterCfg.TLSCertFile = certFile
	masterCfg.TLSKeyFile = keyFile
	master := startServer(t, masterCfg)

	mc := dial(t, master)
	expectSimple(t, mc.do("SET", "pre", "1"), "OK")

	replicaCfg := tlsConfig()
	replicaCfg.Dir = t.TempDir()
	replicaCfg.TLSReplication = true
	replicaCfg.TLSCACertFile = caFile
	replica := startServer(t, replicaCfg)

	rc := dial(t, replica)
	expectSimple(t, rc.do("REPLICAOF", "127.0.0.1", strconv.Itoa(masterCfg.TLSPort)), "OK")

	waitFor(t, 3*time.Second, "TLS replication", func() bool {
		value, ok := getString(rc, "pre")
		return ok && value == "1"
	})
	expectSimple(t, mc.do("SET", "live", "2"), "OK")
	waitFor(t, 3*time.Second, "TLS live propagation", func() bool {
		value, ok := getString(rc, "live")
		return ok && value == "2"
	})
}
