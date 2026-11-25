package pki

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"
)

func WriteNew(dir, name string, data []byte) error {
	f, e := os.OpenFile(filepath.Join(dir, name), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if e != nil {
		return e
	}
	defer f.Close()
	if _, e = f.Write(data); e != nil {
		return e
	}
	return f.Sync()
}
func block(typ string, b []byte) []byte { return pem.EncodeToMemory(&pem.Block{Type: typ, Bytes: b}) }
func parse(b []byte, typ string) ([]byte, error) {
	p, rest := pem.Decode(b)
	if p == nil || p.Type != typ || len(rest) != 0 {
		return nil, errors.New("invalid PEM")
	}
	return p.Bytes, nil
}
func Cert(b []byte) (*x509.Certificate, error) {
	d, e := parse(b, "CERTIFICATE")
	if e != nil {
		return nil, e
	}
	return x509.ParseCertificate(d)
}
func Fingerprint(c *x509.Certificate) string {
	s := sha256.Sum256(c.Raw)
	return hex.EncodeToString(s[:])
}
func keys() (ed25519.PublicKey, ed25519.PrivateKey, []byte, error) {
	p, k, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		return nil, nil, nil, e
	}
	b, e := x509.MarshalPKCS8PrivateKey(k)
	return p, k, block("PRIVATE KEY", b), e
}
func template(cn string, days int) *x509.Certificate {
	now := time.Now()
	return &x509.Certificate{Subject: pkix.Name{CommonName: cn}, NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(0, 0, days)}
}
func sign(t, ca *x509.Certificate, p crypto.PublicKey, k crypto.Signer) ([]byte, error) {
	n, e := rand.Int(rand.Reader, new(big.Int).Sub(new(big.Int).Lsh(big.NewInt(1), 128), big.NewInt(1)))
	if e != nil {
		return nil, e
	}
	t.SerialNumber = n.Add(n, big.NewInt(1))
	b, e := x509.CreateCertificate(rand.Reader, t, ca, p, k)
	return block("CERTIFICATE", b), e
}

func Init(dir, host string) error {
	newTLSKey := func() (*ecdsa.PrivateKey, []byte, error) {
		k, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, err
		}
		data, err := x509.MarshalPKCS8PrivateKey(k)
		return k, block("PRIVATE KEY", data), err
	}
	k, key, e := newTLSKey()
	if e != nil {
		return e
	}
	ca := template("Dockroom device CA", 1825)
	ca.IsCA = true
	ca.BasicConstraintsValid = true
	ca.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageCRLSign
	cb, e := sign(ca, ca, k.Public(), k)
	if e != nil {
		return e
	}
	skey, serverKey, e := newTLSKey()
	if e != nil {
		return e
	}
	s := template(host, 365)
	s.KeyUsage = x509.KeyUsageDigitalSignature
	s.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	if ip := net.ParseIP(host); ip != nil {
		s.IPAddresses = []net.IP{ip}
	} else {
		s.DNSNames = []string{host}
	}
	sb, e := sign(s, ca, skey.Public(), k)
	if e != nil {
		return e
	}
	for n, data := range map[string][]byte{"ca.pem": cb, "ca-key.pem": key, "server.pem": sb, "server-key.pem": serverKey} {
		if e = WriteNew(dir, n, data); e != nil {
			return e
		}
	}
	return nil
}

type Authority struct {
	CA  *x509.Certificate
	Key *ecdsa.PrivateKey
}

func Load(dir string) (Authority, error) {
	cb, e := os.ReadFile(filepath.Join(dir, "ca.pem"))
	if e != nil {
		return Authority{}, e
	}
	ca, e := Cert(cb)
	if e != nil {
		return Authority{}, e
	}
	kb, e := os.ReadFile(filepath.Join(dir, "ca-key.pem"))
	if e != nil {
		return Authority{}, e
	}
	d, e := parse(kb, "PRIVATE KEY")
	if e != nil {
		return Authority{}, e
	}
	x, e := x509.ParsePKCS8PrivateKey(d)
	if e != nil {
		return Authority{}, e
	}
	k, ok := x.(*ecdsa.PrivateKey)
	if !ok || !ca.IsCA || !k.PublicKey.Equal(ca.PublicKey) {
		return Authority{}, errors.New("invalid device CA")
	}
	return Authority{ca, k}, nil
}
func CSR(node string) ([]byte, []byte, error) {
	_, k, key, e := keys()
	if e != nil {
		return nil, nil, e
	}
	b, e := makeCSR(node, k)
	return b, key, e
}

func makeCSR(node string, k ed25519.PrivateKey) ([]byte, error) {
	b, e := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{Subject: pkix.Name{CommonName: node}}, k)
	return block("CERTIFICATE REQUEST", b), e
}

func RenewalCSR(node string, key []byte) ([]byte, error) {
	b, e := parse(key, "PRIVATE KEY")
	if e != nil {
		return nil, e
	}
	v, e := x509.ParsePKCS8PrivateKey(b)
	if e != nil {
		return nil, e
	}
	k, ok := v.(ed25519.PrivateKey)
	if !ok {
		return nil, errors.New("invalid device key")
	}
	return makeCSR(node, k)
}

func CSRKey(node string, pemCSR []byte) (ed25519.PublicKey, error) {
	b, e := parse(pemCSR, "CERTIFICATE REQUEST")
	if e != nil {
		return nil, e
	}
	csr, e := x509.ParseCertificateRequest(b)
	if e != nil {
		return nil, e
	}
	p, ok := csr.PublicKey.(ed25519.PublicKey)
	if !ok || csr.CheckSignature() != nil || csr.Subject.CommonName != node {
		return nil, errors.New("invalid CSR identity/signature")
	}
	return p, nil
}

func (a Authority) Issue(node string, pemCSR []byte) ([]byte, *x509.Certificate, error) {
	p, e := CSRKey(node, pemCSR)
	if e != nil {
		return nil, nil, e
	}
	t := template(node, 30)
	t.KeyUsage = x509.KeyUsageDigitalSignature
	t.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	issued, e := sign(t, a.CA, p, a.Key)
	if e != nil {
		return nil, nil, e
	}
	c, e := Cert(issued)
	return issued, c, e
}
