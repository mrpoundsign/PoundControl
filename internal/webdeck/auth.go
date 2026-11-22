package webdeck

import (
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/hkdf"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"filippo.io/edwards25519"

	"github.com/mrpoundsign/PoundDeck/pkg/deck"
	"github.com/mrpoundsign/PoundDeck/pkg/secrets"
)

const (
	// HostSecretKeyName is the OS keyring key storing the persistent base64 Ed25519 host seed.
	HostSecretKeyName = "webdeck.host.ed25519"

	// RFC 9382 Edwards25519 base points M and N (Section 4).
	rfc9382PointMHex = "d048032c6ea0b6d697ddc2e86bda85a33adac920f1bf18e1b0c6d166a5cecdaf"
	rfc9382PointNHex = "d3bfb518f44f3430f29d0c92af503865a1ed3281dc69b35dd868ba85f886c4ab"
)

var (
	spake2PointM = mustDecodeEdwardsPointHex(rfc9382PointMHex)
	spake2PointN = mustDecodeEdwardsPointHex(rfc9382PointNHex)
)

func mustDecodeEdwardsPointHex(hexStr string) *edwards25519.Point {
	raw, err := hex.DecodeString(hexStr)
	if err != nil {
		panic(fmt.Sprintf("invalid SPAKE2 point hex: %v", err))
	}
	pt, err := new(edwards25519.Point).SetBytes(raw)
	if err != nil {
		panic(fmt.Sprintf("invalid SPAKE2 point bytes: %v", err))
	}
	return pt
}

// PublicKeyFingerprint formats a 32-byte public key into a human-verifiable XXXX-XXXX-XXXX fingerprint.
func PublicKeyFingerprint(pub []byte) string {
	sum := sha256.Sum256(pub)
	h := strings.ToUpper(hex.EncodeToString(sum[:6]))
	return h[0:4] + "-" + h[4:8] + "-" + h[8:12]
}

// DefaultHostName returns the local device hostname with a safe fallback.
func DefaultHostName() string {
	host, err := os.Hostname()
	host = strings.TrimSpace(host)
	if err != nil || host == "" {
		return "PoundDeck Studio"
	}
	return host
}

func ensureHostIdentity(cfg Config, store secrets.Store) (Config, ed25519.PrivateKey, bool, error) {
	if store == nil {
		store = secrets.NewMemoryStore()
	}
	out := cfg
	needsSave := false
	if out.HostID == "" {
		out.HostID = deck.NewUUID()
		needsSave = true
	}

	var seed []byte
	if rawSeed, err := store.Get(HostSecretKeyName); err == nil {
		decoded, decErr := base64.StdEncoding.DecodeString(strings.TrimSpace(rawSeed))
		if decErr == nil && len(decoded) == ed25519.SeedSize {
			seed = decoded
		}
	}

	if len(seed) != ed25519.SeedSize {
		seed = make([]byte, ed25519.SeedSize)
		if _, err := rand.Read(seed); err != nil {
			return out, nil, false, fmt.Errorf("generate host identity seed: %w", err)
		}
		if err := store.Set(HostSecretKeyName, base64.StdEncoding.EncodeToString(seed)); err != nil {
			return out, nil, false, fmt.Errorf("store host identity seed: %w", err)
		}
	}

	priv := ed25519.NewKeyFromSeed(seed)
	pubB64 := base64.StdEncoding.EncodeToString(priv[ed25519.SeedSize:])
	if out.HostPublicKey != pubB64 {
		out.HostPublicKey = pubB64
		needsSave = true
	}
	return out, priv, needsSave, nil
}

func randomNonZeroScalar() (*edwards25519.Scalar, error) {
	var uniform [64]byte
	for {
		if _, err := rand.Read(uniform[:]); err != nil {
			return nil, fmt.Errorf("generate SPAKE2 scalar: %w", err)
		}
		sc, err := edwards25519.NewScalar().SetUniformBytes(uniform[:])
		if err != nil {
			return nil, fmt.Errorf("set uniform scalar: %w", err)
		}
		var zero edwards25519.Scalar
		if sc.Equal(&zero) == 0 {
			return sc, nil
		}
	}
}

func deriveSpake2ScalarW(pin, windowID string) (*edwards25519.Scalar, error) {
	salt := []byte("pounddeck-spake2-v1:" + windowID)
	raw, err := hkdf.Key(sha256.New, []byte(pin), salt, "w", 64)
	if err != nil {
		return nil, fmt.Errorf("derive SPAKE2 w: %w", err)
	}
	sc, err := edwards25519.NewScalar().SetUniformBytes(raw)
	if err != nil {
		return nil, fmt.Errorf("reduce SPAKE2 w: %w", err)
	}
	return sc, nil
}

type spake2Material struct {
	w          *edwards25519.Scalar
	pA         []byte
	pB         []byte
	K          []byte
	transcript []byte
	hashTT     []byte
	ke         []byte
	ka         []byte
	kcA        []byte
	kcB        []byte
	cA         []byte
	cB         []byte
	sas        string
}

func appendLengthPrefixed(dst, elem []byte) []byte {
	var lenBuf [8]byte
	binary.LittleEndian.PutUint64(lenBuf[:], uint64(len(elem)))
	dst = append(dst, lenBuf[:]...)
	return append(dst, elem...)
}

func buildSpake2Transcript(windowID string, clientPub, hostPub, pA, pB, kBytes, wBytes []byte) []byte {
	ctx := []byte("pounddeck-pair-v1:" + windowID)
	total := 7*8 + len(ctx) + len(clientPub) + len(hostPub) + len(pA) + len(pB) + len(kBytes) + len(wBytes)
	tt := make([]byte, 0, total)
	tt = appendLengthPrefixed(tt, ctx)
	tt = appendLengthPrefixed(tt, clientPub)
	tt = appendLengthPrefixed(tt, hostPub)
	tt = appendLengthPrefixed(tt, pA)
	tt = appendLengthPrefixed(tt, pB)
	tt = appendLengthPrefixed(tt, kBytes)
	tt = appendLengthPrefixed(tt, wBytes)
	return tt
}

func computeHMACSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write(data)
	return mac.Sum(nil)
}

func finishSpake2Material(windowID string, clientPub, hostPub, pA, pB, kBytes []byte, w *edwards25519.Scalar) (spake2Material, error) {
	tt := buildSpake2Transcript(windowID, clientPub, hostPub, pA, pB, kBytes, w.Bytes())
	sum := sha256.Sum256(tt)
	ke := make([]byte, 16)
	ka := make([]byte, 16)
	copy(ke, sum[0:16])
	copy(ka, sum[16:32])

	kc, err := hkdf.Key(sha256.New, ka, nil, "ConfirmationKeys", 64)
	if err != nil {
		return spake2Material{}, fmt.Errorf("derive SPAKE2 confirmation keys: %w", err)
	}
	kcA := make([]byte, 32)
	kcB := make([]byte, 32)
	copy(kcA, kc[0:32])
	copy(kcB, kc[32:64])

	sasBytes, err := hkdf.Key(sha256.New, ke, nil, "PoundDeck-SAS-v1", 4)
	if err != nil {
		return spake2Material{}, fmt.Errorf("derive SAS code: %w", err)
	}
	sasInt := (binary.BigEndian.Uint32(sasBytes) & 0x7fffffff) % 1000000
	sas := fmt.Sprintf("%03d-%03d", sasInt/1000, sasInt%1000)

	return spake2Material{
		w:          w,
		pA:         append([]byte(nil), pA...),
		pB:         append([]byte(nil), pB...),
		K:          append([]byte(nil), kBytes...),
		transcript: tt,
		hashTT:     append([]byte(nil), sum[:]...),
		ke:         ke,
		ka:         ka,
		kcA:        kcA,
		kcB:        kcB,
		cA:         computeHMACSHA256(kcA, tt),
		cB:         computeHMACSHA256(kcB, tt),
		sas:        sas,
	}, nil
}

func spake2ServerCompute(pin, windowID string, clientPub, hostPub, pA []byte) (spake2Material, error) {
	if len(clientPub) != ed25519.PublicKeySize || len(hostPub) != ed25519.PublicKeySize {
		return spake2Material{}, errors.New("invalid Ed25519 public key length")
	}
	pAPoint, err := new(edwards25519.Point).SetBytes(pA)
	if err != nil {
		return spake2Material{}, fmt.Errorf("invalid client SPAKE2 share: %w", err)
	}
	w, err := deriveSpake2ScalarW(pin, windowID)
	if err != nil {
		return spake2Material{}, err
	}
	y, err := randomNonZeroScalar()
	if err != nil {
		return spake2Material{}, err
	}

	// pB = y*G + w*N
	yG := new(edwards25519.Point).ScalarBaseMult(y)
	wN := new(edwards25519.Point).ScalarMult(w, spake2PointN)
	pBPoint := new(edwards25519.Point).Add(yG, wN)

	// K = h*y*(pA - w*M) with subgroup validation.
	wM := new(edwards25519.Point).ScalarMult(w, spake2PointM)
	t := new(edwards25519.Point).Subtract(pAPoint, wM)
	t8 := new(edwards25519.Point).MultByCofactor(t)
	if t8.Equal(edwards25519.NewIdentityPoint()) == 1 {
		return spake2Material{}, errors.New("client SPAKE2 share in small subgroup")
	}
	kPoint := new(edwards25519.Point).ScalarMult(y, t8)

	return finishSpake2Material(windowID, clientPub, hostPub, pA, pBPoint.Bytes(), kPoint.Bytes(), w)
}

type spake2ClientState struct {
	pin      string
	windowID string
	x        *edwards25519.Scalar
	w        *edwards25519.Scalar
	pA       []byte
}

func spake2ClientStart(pin, windowID string) (spake2ClientState, error) {
	w, err := deriveSpake2ScalarW(pin, windowID)
	if err != nil {
		return spake2ClientState{}, err
	}
	x, err := randomNonZeroScalar()
	if err != nil {
		return spake2ClientState{}, err
	}
	// pA = x*G + w*M
	xG := new(edwards25519.Point).ScalarBaseMult(x)
	wM := new(edwards25519.Point).ScalarMult(w, spake2PointM)
	pAPoint := new(edwards25519.Point).Add(xG, wM)

	return spake2ClientState{
		pin:      pin,
		windowID: windowID,
		x:        x,
		w:        w,
		pA:       pAPoint.Bytes(),
	}, nil
}

func spake2ClientFinish(st spake2ClientState, clientPub, hostPub, pB []byte) (spake2Material, error) {
	if len(clientPub) != ed25519.PublicKeySize || len(hostPub) != ed25519.PublicKeySize {
		return spake2Material{}, errors.New("invalid Ed25519 public key length")
	}
	pBPoint, err := new(edwards25519.Point).SetBytes(pB)
	if err != nil {
		return spake2Material{}, fmt.Errorf("invalid server SPAKE2 share: %w", err)
	}

	// K = h*x*(pB - w*N) with subgroup validation.
	wN := new(edwards25519.Point).ScalarMult(st.w, spake2PointN)
	t := new(edwards25519.Point).Subtract(pBPoint, wN)
	t8 := new(edwards25519.Point).MultByCofactor(t)
	if t8.Equal(edwards25519.NewIdentityPoint()) == 1 {
		return spake2Material{}, errors.New("server SPAKE2 share in small subgroup")
	}
	kPoint := new(edwards25519.Point).ScalarMult(st.x, t8)

	return finishSpake2Material(st.windowID, clientPub, hostPub, st.pA, pB, kPoint.Bytes(), st.w)
}

func pairHostSigPayload(hashTT []byte) []byte {
	msg := make([]byte, 0, len("pounddeck-pair-host-v1")+len(hashTT))
	msg = append(msg, "pounddeck-pair-host-v1"...)
	return append(msg, hashTT...)
}

func pairClientSigPayload(hashTT []byte) []byte {
	msg := make([]byte, 0, len("pounddeck-pair-client-v1")+len(hashTT))
	msg = append(msg, "pounddeck-pair-client-v1"...)
	return append(msg, hashTT...)
}

func pairConfirmMAC(kcA []byte, attemptID string, confirm bool) []byte {
	flag := "0"
	if confirm {
		flag = "1"
	}
	return computeHMACSHA256(kcA, []byte("confirm:"+attemptID+":"+flag))
}

func pairStatusReqMAC(kcA []byte, attemptID string) []byte {
	return computeHMACSHA256(kcA, []byte("status:"+attemptID))
}

func pairStatusRespMAC(kcB []byte, attemptID, status, deviceID string) []byte {
	return computeHMACSHA256(kcB, []byte("result:"+attemptID+":"+status+":"+deviceID))
}

func buildWSTranscript1(hostID, deviceID string, hostPub, clientPub, serverNonce, clientNonce, clientEphPub []byte) []byte {
	domain := []byte("pounddeck-ws-v2")
	total := len(domain) + 16 + len(hostID) + len(deviceID) + len(hostPub) + len(clientPub) + len(serverNonce) + len(clientNonce) + len(clientEphPub)
	buf := make([]byte, 0, total)
	buf = append(buf, domain...)
	buf = appendLengthPrefixed(buf, []byte(hostID))
	buf = appendLengthPrefixed(buf, []byte(deviceID))
	buf = append(buf, hostPub...)
	buf = append(buf, clientPub...)
	buf = append(buf, serverNonce...)
	buf = append(buf, clientNonce...)
	buf = append(buf, clientEphPub...)
	return buf
}

func buildWSTranscript2(t1, serverEphPub []byte) []byte {
	buf := make([]byte, 0, len(t1)+len(serverEphPub))
	buf = append(buf, t1...)
	return append(buf, serverEphPub...)
}

func deriveWSSessionKeys(sharedSecret, t2 []byte) (clientMacKey, serverMacKey, assetMacKey []byte, err error) {
	salt := sha256.Sum256(t2)
	okm, err := hkdf.Key(sha256.New, sharedSecret, salt[:], "pounddeck-ws-keys-v2", 96)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("derive WebSocket session keys: %w", err)
	}
	clientMacKey = make([]byte, 32)
	serverMacKey = make([]byte, 32)
	assetMacKey = make([]byte, 32)
	copy(clientMacKey, okm[0:32])
	copy(serverMacKey, okm[32:64])
	copy(assetMacKey, okm[64:96])
	return clientMacKey, serverMacKey, assetMacKey, nil
}

func computeFrameMAC(macKey []byte, dir string, seq uint64, payload string) []byte {
	msg := dir + ":" + strconv.FormatUint(seq, 10) + ":" + payload
	return computeHMACSHA256(macKey, []byte(msg))
}

func computeAssetMAC(assetMacKey []byte, sessionID, assetID string) string {
	sum := computeHMACSHA256(assetMacKey, []byte("asset:"+sessionID+":"+assetID))
	return base64.RawURLEncoding.EncodeToString(sum)
}

func generateX25519Keypair() (*ecdh.PrivateKey, error) {
	return ecdh.X25519().GenerateKey(rand.Reader)
}

func computeX25519Shared(priv *ecdh.PrivateKey, peerPubBytes []byte) ([]byte, error) {
	peerPub, err := ecdh.X25519().NewPublicKey(peerPubBytes)
	if err != nil {
		return nil, fmt.Errorf("invalid X25519 peer public key: %w", err)
	}
	shared, err := priv.ECDH(peerPub)
	if err != nil {
		return nil, fmt.Errorf("X25519 shared secret failed: %w", err)
	}
	return shared, nil
}
