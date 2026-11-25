package api

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"dockroom/internal/auth"
	"dockroom/internal/pki"
	"dockroom/internal/protocol"
	"dockroom/internal/store"
)

func TestPairingRenewalKeepsIdentityAndRejectsRevokedDevice(t *testing.T) {
	dir := t.TempDir()
	need := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	need(pki.Init(dir, "localhost"))
	ca, err := pki.Load(dir)
	need(err)
	db, err := store.Open(dir, true)
	need(err)
	defer db.Close()
	const origin = "https://localhost:8442"
	h := New(db, ca, origin, 8443)
	defer h.Close()
	secret := protocol.Token()
	_, err = db.Exec(`INSERT INTO users(id,password,admin) VALUES('admin','test',1)`)
	need(err)
	_, err = db.Exec(`INSERT INTO pairing_key VALUES(1,?,'admin')`, auth.HashSecret(secret))
	need(err)
	const node = "fixture-renew"
	csr, key, err := pki.CSR(node)
	need(err)
	post := func(path string, secure bool, body any) *httptest.ResponseRecorder {
		t.Helper()
		b, err := json.Marshal(body)
		need(err)
		req := httptest.NewRequest(http.MethodPost, origin+path, bytes.NewReader(b))
		if !secure {
			req.TLS = nil
		} else {
			req.TLS = &tls.ConnectionState{}
		}
		req.Header.Set("Content-Type", "application/json")
		w := httptest.NewRecorder()
		h.Browser().ServeHTTP(w, req)
		return w
	}
	issued := func(w *httptest.ResponseRecorder, status int) protocol.Issued {
		t.Helper()
		if w.Code != status {
			t.Fatalf("status %d, expected %d", w.Code, status)
		}
		var out protocol.Issued
		need(json.Unmarshal(w.Body.Bytes(), &out))
		return out
	}
	b := map[string]string{"nodeId": node, "key": secret, "csr": string(csr)}
	if w := post("/device/join", false, b); w.Code != 403 {
		t.Fatalf("non-TLS enrollment returned %d", w.Code)
	}
	first := issued(post("/device/join", true, b), 201)
	if w := post("/device/join", true, b); w.Code != 409 {
		t.Fatalf("existing identity replaced: %d", w.Code)
	}
	_, err = db.Exec(`UPDATE paired_devices SET renew_at=0 WHERE node=?`, node)
	need(err)
	_, err = db.Exec(`DELETE FROM pairing_key`)
	need(err)
	csr, err = pki.RenewalCSR(node, key)
	need(err)
	wrongCSR, _, err := pki.CSR(node)
	need(err)
	if w := post("/device/renew", true, map[string]string{"nodeId": node, "csr": string(wrongCSR)}); w.Code != 403 {
		t.Fatalf("different key renewed identity: %d", w.Code)
	}
	request := map[string]string{"nodeId": node, "csr": string(csr)}
	second := issued(post("/device/renew", true, request), 200)
	if second.Fingerprint == first.Fingerprint {
		t.Fatal("renewal reused the old certificate")
	}
	_, err = tls.X509KeyPair([]byte(second.Certificate), key)
	need(err)
	if again := issued(post("/device/renew", true, request), 200); again != second {
		t.Fatal("pending renewal was not replay-safe")
	}
	_, err = db.Exec(`UPDATE nodes SET state='revoked' WHERE id=?`, node)
	need(err)
	if w := post("/device/renew", true, request); w.Code != 403 {
		t.Fatalf("revoked device renewed identity: %d", w.Code)
	}
}
