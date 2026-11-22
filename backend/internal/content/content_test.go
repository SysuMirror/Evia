package content

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func jwkInteger(value *big.Int) string { return base64.RawURLEncoding.EncodeToString(value.Bytes()) }

func testDecryptor(t *testing.T) (*RSAOAEPDecryptor, *rsa.PublicKey) {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	jwk := map[string]string{
		"kty": "RSA", "n": jwkInteger(key.N), "e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		"d": jwkInteger(key.D), "p": jwkInteger(key.Primes[0]), "q": jwkInteger(key.Primes[1]),
	}
	raw, _ := json.Marshal(jwk)
	path := filepath.Join(t.TempDir(), "private.jwk")
	if err = os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	decryptor, err := NewRSAOAEPDecryptorFromFile(path, "key-1")
	if err != nil {
		t.Fatal(err)
	}
	return decryptor, &key.PublicKey
}

func encryptTestChunk(t *testing.T, public *rsa.PublicKey, plaintext []byte) EncryptedChunk {
	t.Helper()
	return encryptTestChunkBlock(t, public, plaintext, "fixed-600-v1", 600, len(plaintext))
}

func encryptTestChunkBlock(t *testing.T, public *rsa.PublicKey, plaintext []byte, padding string, blockSize, embeddedLength int) EncryptedChunk {
	t.Helper()
	if blockSize < 2 || len(plaintext) > blockSize-2 {
		t.Fatal("invalid encrypted test block")
	}
	rawKey := make([]byte, 32)
	nonce := make([]byte, 12)
	if _, err := rand.Read(rawKey); err != nil {
		t.Fatal(err)
	}
	if _, err := rand.Read(nonce); err != nil {
		t.Fatal(err)
	}
	wrapped, err := rsa.EncryptOAEP(sha256.New(), rand.Reader, public, rawKey, nil)
	if err != nil {
		t.Fatal(err)
	}
	block, _ := aes.NewCipher(rawKey)
	gcm, _ := cipher.NewGCM(block)
	chunk := EncryptedChunk{
		ProjectID: "p", Environment: "development", EventID: "e", ReceivedAt: time.Now().UTC(),
		ContentType: "ai_chat_message", StreamID: "conversation", ItemID: "message", ActorType: "user",
		ChunkIndex: 0, ChunkCount: 1, KeyID: "key-1", Algorithm: "A256GCM+RSA-OAEP-256",
		AADVersion: "evia-content-v1", Padding: padding,
		Nonce: base64.RawURLEncoding.EncodeToString(nonce), WrappedKey: base64.RawURLEncoding.EncodeToString(wrapped),
		ContentLength: len(plaintext),
	}
	padded := make([]byte, blockSize)
	padded[0], padded[1] = byte(embeddedLength>>8), byte(embeddedLength)
	copy(padded[2:], plaintext)
	if _, err = rand.Read(padded[2+len(plaintext):]); err != nil {
		t.Fatal(err)
	}
	aad := []byte("evia-content-v1|p|development|key-1|ai_chat_message|conversation|message|user|0|1|" + fmt.Sprint(len(plaintext)) + "|0||0")
	chunk.Ciphertext = base64.RawURLEncoding.EncodeToString(gcm.Seal(nil, nonce, padded, aad))
	return chunk
}

func TestRSAOAEPDecryptorAuthenticatesContent(t *testing.T) {
	decryptor, public := testDecryptor(t)
	plaintext := []byte("测试字段级加密")
	encrypted := encryptTestChunk(t, public, plaintext)
	decrypted, err := decryptor.Decrypt(context.Background(), encrypted)
	if err != nil || string(decrypted.Plaintext) != string(plaintext) {
		t.Fatalf("decrypt mismatch: %q %v", decrypted.Plaintext, err)
	}
	encrypted.ItemID = "tampered"
	if _, err = decryptor.Decrypt(context.Background(), encrypted); err == nil {
		t.Fatal("AAD tampering must fail closed")
	}
}

func TestRSAOAEPDecryptorSupportsMixedFixedPaddingItems(t *testing.T) {
	decryptor, public := testDecryptor(t)
	cases := []struct {
		padding   string
		blockSize int
		plaintext []byte
	}{
		{padding: "fixed-600-v1", blockSize: 600, plaintext: []byte("chat-compatible")},
		{padding: "fixed-4096-v2", blockSize: 4096, plaintext: bytes.Repeat([]byte("r"), 4094)},
	}
	for _, tc := range cases {
		encrypted := encryptTestChunkBlock(t, public, tc.plaintext, tc.padding, tc.blockSize, len(tc.plaintext))
		decrypted, err := decryptor.Decrypt(context.Background(), encrypted)
		if err != nil || !bytes.Equal(decrypted.Plaintext, tc.plaintext) {
			t.Fatalf("%s decrypt mismatch: len=%d err=%v", tc.padding, len(decrypted.Plaintext), err)
		}
	}
}

func TestRSAOAEPDecryptorRejectsMalformedOrMismatchedPadding(t *testing.T) {
	decryptor, public := testDecryptor(t)
	plain := []byte("bounded")

	malformedLength := encryptTestChunkBlock(t, public, plain, "fixed-4096-v2", 4096, 4095)
	if _, err := decryptor.Decrypt(context.Background(), malformedLength); err == nil {
		t.Fatal("embedded length beyond block payload must fail closed")
	}
	zeroLength := encryptTestChunkBlock(t, public, plain, "fixed-4096-v2", 4096, 0)
	if _, err := decryptor.Decrypt(context.Background(), zeroLength); err == nil {
		t.Fatal("zero embedded length must fail closed")
	}
	wrongOpenedLength := encryptTestChunkBlock(t, public, plain, "fixed-4096-v2", 4095, len(plain))
	if _, err := decryptor.Decrypt(context.Background(), wrongOpenedLength); err == nil {
		t.Fatal("non-exact opened block length must fail closed")
	}

	large := encryptTestChunkBlock(t, public, plain, "fixed-4096-v2", 4096, len(plain))
	large.Padding = "fixed-600-v1"
	if _, err := decryptor.Decrypt(context.Background(), large); err == nil {
		t.Fatal("4096 to 600 padding downgrade must fail closed")
	}
	small := encryptTestChunk(t, public, plain)
	small.Padding = "fixed-4096-v2"
	if _, err := decryptor.Decrypt(context.Background(), small); err == nil {
		t.Fatal("600 to 4096 padding tamper must fail closed")
	}
	small.Padding = "fixed-unknown-v9"
	if _, err := decryptor.Decrypt(context.Background(), small); err == nil {
		t.Fatal("unknown padding must fail closed")
	}

	unversionedLarge := encryptTestChunkBlock(t, public, plain, "fixed-4096-v2", 4096, len(plain))
	unversionedLarge.AADVersion, unversionedLarge.Padding = "", ""
	if _, err := decryptor.Decrypt(context.Background(), unversionedLarge); err == nil {
		t.Fatal("legacy compatibility must not guess the 4096-byte mode")
	}
}

func TestKMSDecryptorUsesAuthenticatedRemoteUnwrap(t *testing.T) {
	private, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	tokenPath := filepath.Join(t.TempDir(), "kms.token")
	if err = os.WriteFile(tokenPath, []byte("secret-token\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		var request kmsRequest
		if json.NewDecoder(r.Body).Decode(&request) != nil || request.KeyID != "key-1" || request.Algorithm != "RSA-OAEP-256" {
			http.Error(w, "bad request", http.StatusBadRequest)
			return
		}
		wrapped, decodeErr := base64.RawURLEncoding.DecodeString(request.WrappedKey)
		if decodeErr != nil {
			http.Error(w, "bad key", http.StatusBadRequest)
			return
		}
		raw, decryptErr := rsa.DecryptOAEP(sha256.New(), rand.Reader, private, wrapped, nil)
		if decryptErr != nil {
			http.Error(w, "decrypt", http.StatusBadRequest)
			return
		}
		_ = json.NewEncoder(w).Encode(kmsResponse{PlaintextKey: base64.RawURLEncoding.EncodeToString(raw)})
	}))
	defer server.Close()
	decryptor, err := NewRSAOAEPDecryptorFromKMS(server.URL, tokenPath, "key-1", true)
	if err != nil {
		t.Fatal(err)
	}
	encrypted := encryptTestChunk(t, &private.PublicKey, []byte("kms protected"))
	decrypted, err := decryptor.Decrypt(context.Background(), encrypted)
	if err != nil || string(decrypted.Plaintext) != "kms protected" {
		t.Fatalf("KMS decrypt mismatch: %q %v", decrypted.Plaintext, err)
	}
}

func TestFileStoreRoundTripAndHash(t *testing.T) {
	store, err := NewFileStore(filepath.Join(t.TempDir(), "content"))
	if err != nil {
		t.Fatal(err)
	}
	chunk := DecryptedChunk{EncryptedChunk: EncryptedChunk{ProjectID: "p", Environment: "development", EventID: "e", ItemID: "m", ChunkIndex: 0}, Plaintext: []byte("hello")}
	key, hash, err := store.Put(context.Background(), chunk)
	if err != nil || len(key) != 70 || len(hash) != 64 {
		t.Fatal(key, hash, err)
	}
	got, err := store.Read(context.Background(), key)
	if err != nil || string(got) != "hello" {
		t.Fatal(string(got), err)
	}
	if _, err = store.Read(context.Background(), "../private"); err == nil {
		t.Fatal("unsafe object key must fail")
	}

	large := chunk
	large.EventID = "large"
	large.Plaintext = bytes.Repeat([]byte("x"), MaxPlaintextChunkLength)
	largeKey, _, err := store.Put(context.Background(), large)
	if err != nil {
		t.Fatalf("maximum fixed-4096 plaintext chunk must be stored: %v", err)
	}
	largeRoundTrip, err := store.Read(context.Background(), largeKey)
	if err != nil || !bytes.Equal(largeRoundTrip, large.Plaintext) {
		t.Fatalf("maximum fixed-4096 plaintext chunk did not round trip: bytes=%d err=%v", len(largeRoundTrip), err)
	}

	tooLarge := chunk
	tooLarge.EventID = "too-large"
	tooLarge.Plaintext = bytes.Repeat([]byte("x"), MaxPlaintextChunkLength+1)
	if _, _, err = store.Put(context.Background(), tooLarge); err == nil {
		t.Fatal("oversized plaintext chunk reached the artifact store")
	}
}
