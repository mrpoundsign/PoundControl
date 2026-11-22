package webdeck

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/edwards25519"

	"github.com/mrpoundsign/PoundControl/pkg/secrets"
)

func TestEnsureHostIdentityAndFingerprints(t *testing.T) {
	store := secrets.NewMemoryStore()
	cfg1, priv1, changed1, err := ensureHostIdentity(DefaultConfig(), store)
	if err != nil {
		t.Fatalf("ensureHostIdentity(1) error = %v", err)
	}
	if !changed1 || cfg1.HostID == "" || cfg1.HostPublicKey == "" {
		t.Fatalf("expected initial host identity generation, got changed=%v cfg=%+v", changed1, cfg1)
	}

	pub1, err := base64.StdEncoding.DecodeString(cfg1.HostPublicKey)
	if err != nil || len(pub1) != ed25519.PublicKeySize {
		t.Fatalf("invalid stored HostPublicKey (%v): %q", err, cfg1.HostPublicKey)
	}
	fp := PublicKeyFingerprint(pub1)
	if len(fp) != 14 || fp[4] != '-' || fp[9] != '-' {
		t.Fatalf("unexpected fingerprint format: %q", fp)
	}

	cfg2, priv2, changed2, err := ensureHostIdentity(cfg1, store)
	if err != nil {
		t.Fatalf("ensureHostIdentity(2) error = %v", err)
	}
	if changed2 {
		t.Fatal("expected second ensureHostIdentity call to reuse existing identity without changes")
	}
	if !priv1.Equal(priv2) || cfg2.HostID != cfg1.HostID || cfg2.HostPublicKey != cfg1.HostPublicKey {
		t.Fatal("expected persisted host identity to match across loads")
	}
}

func TestSPAKE2MatchWrongPINAndSubgroupRejection(t *testing.T) {
	cPub, cPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(client): %v", err)
	}
	hPub, hPriv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatalf("GenerateKey(host): %v", err)
	}
	winID := "win-test-1234"
	pin := "482910"

	st, err := spake2ClientStart(pin, winID)
	if err != nil {
		t.Fatalf("spake2ClientStart: %v", err)
	}
	srvMat, err := spake2ServerCompute(pin, winID, cPub, hPub, st.pA)
	if err != nil {
		t.Fatalf("spake2ServerCompute: %v", err)
	}
	cliMat, err := spake2ClientFinish(st, cPub, hPub, srvMat.pB)
	if err != nil {
		t.Fatalf("spake2ClientFinish: %v", err)
	}

	if string(srvMat.K) != string(cliMat.K) || string(srvMat.cA) != string(cliMat.cA) || string(srvMat.cB) != string(cliMat.cB) {
		t.Fatal("SPAKE2 key material mismatch for matching PIN")
	}
	if srvMat.sas != cliMat.sas || len(srvMat.sas) != 7 || srvMat.sas[3] != '-' {
		t.Fatalf("unexpected SAS mismatch: srv=%q cli=%q", srvMat.sas, cliMat.sas)
	}

	// Host & client signature payloads verify across both keys.
	hSig := ed25519.Sign(hPriv, pairHostSigPayload(srvMat.hashTT))
	if !ed25519.Verify(hPub, pairHostSigPayload(cliMat.hashTT), hSig) {
		t.Fatal("host pairing signature failed verification on client hashTT")
	}
	cSig := ed25519.Sign(cPriv, pairClientSigPayload(cliMat.hashTT))
	if !ed25519.Verify(cPub, pairClientSigPayload(srvMat.hashTT), cSig) {
		t.Fatal("client pairing signature failed verification on server hashTT")
	}

	// Wrong PIN must yield mismatched cA/cB/SAS.
	badSt, err := spake2ClientStart("999999", winID)
	if err != nil {
		t.Fatalf("spake2ClientStart(wrong PIN): %v", err)
	}
	badSrv, err := spake2ServerCompute(pin, winID, cPub, hPub, badSt.pA)
	if err != nil {
		t.Fatalf("spake2ServerCompute(wrong PIN): %v", err)
	}
	badCli, err := spake2ClientFinish(badSt, cPub, hPub, badSrv.pB)
	if err != nil {
		t.Fatalf("spake2ClientFinish(wrong PIN): %v", err)
	}
	if string(badSrv.cA) == string(badCli.cA) {
		t.Fatal("expected SPAKE2 confirmation tag mismatch on wrong PIN")
	}

	// Small-subgroup / identity share pA = 0*G + w*M must be rejected.
	w, err := deriveSpake2ScalarW(pin, winID)
	if err != nil {
		t.Fatalf("deriveSpake2ScalarW: %v", err)
	}
	degeneratePA := new(edwards25519.Point).ScalarMult(w, spake2PointM).Bytes()
	if _, err := spake2ServerCompute(pin, winID, cPub, hPub, degeneratePA); err == nil {
		t.Fatal("expected small-subgroup SPAKE2 share rejection")
	}
}

func TestCryptoJSInterop(t *testing.T) {
	nodeBin, err := exec.LookPath("node")
	if err != nil {
		t.Skip("node not installed; skipping Go<->JS interop verification")
	}

	cPub, cPriv, _ := ed25519.GenerateKey(rand.Reader)
	hPub, hPriv, _ := ed25519.GenerateKey(rand.Reader)
	goEphPriv, _ := generateX25519Keypair()
	goEphPub := goEphPriv.PublicKey().Bytes()

	windowID := "win_interop_abc"
	pin := "739204"
	attemptID := "attempt_xyz"
	hostID := "host-uuid-1"
	deviceID := "dev-uuid-2"

	jsInput := map[string]string{
		"msgB64":       base64.StdEncoding.EncodeToString([]byte("PoundControl mutual auth test payload — 123456!")),
		"goPrivB64":    base64.StdEncoding.EncodeToString(hPriv[:32]),
		"goPubB64":     base64.StdEncoding.EncodeToString(hPub),
		"goSigB64":     base64.StdEncoding.EncodeToString(ed25519.Sign(hPriv, []byte("PoundControl mutual auth test payload — 123456!"))),
		"goEphPubB64":  base64.StdEncoding.EncodeToString(goEphPub),
		"clientSeed":   base64.StdEncoding.EncodeToString(cPriv[:32]),
		"clientPubB64": base64.StdEncoding.EncodeToString(cPub),
		"pin":          pin,
		"windowId":     windowID,
		"attemptId":    attemptID,
		"hostId":       hostID,
		"deviceId":     deviceID,
	}
	inBytes, _ := json.Marshal(jsInput)

	cryptoJSPath, err := filepath.Abs(filepath.Join("static", "crypto.js"))
	if err != nil {
		t.Fatalf("Abs(crypto.js): %v", err)
	}

	// Step 1: Ask Node.js/crypto.js to verify Go signatures/hashes and produce JS SPAKE2 start share + X25519 pub.
	script1 := `
const C = require(process.argv[1]);
const input = JSON.parse(require('fs').readFileSync(0, 'utf8'));
const msg = C.fromBase64(input.msgB64);
const sha2 = C.toBase64(C.sha256(msg));
const sha5 = C.toBase64(C.sha512(msg));
const hmac = C.toBase64(C.hmacSha256(C.fromBase64(input.goPubB64), msg));
const hkdf = C.toBase64(C.hkdfSha256(msg, C.utf8Bytes('salt-1'), 'info-1', 42));

const goPub = C.fromBase64(input.goPubB64);
const goSig = C.fromBase64(input.goSigB64);
const verifyGo = C.ed25519.verify(goPub, msg, goSig);
const verifyTampered = C.ed25519.verify(goPub, msg, new Uint8Array(goSig.map((v, i) => i === 5 ? v ^ 1 : v)));

const jsSeed = C.fromBase64(input.clientSeed);
const jsPub = C.ed25519.publicKeyFromSeed(jsSeed);
const jsSig = C.ed25519.sign(jsSeed, msg);
const jsFp = C.ed25519.fingerprint(jsPub);
const goFp = C.ed25519.fingerprint(goPub);

const jsEph = C.x25519.generateKeypair();
const xShared = C.x25519.sharedSecret(jsEph.privateKey, C.fromBase64(input.goEphPubB64));

const st = C.spake2.clientStart(input.pin, input.windowId);
process.stdout.write(JSON.stringify({
  sha2, sha5, hmac, hkdf, verifyGo, verifyTampered,
  jsPubB64: C.toBase64(jsPub),
  jsSigB64: C.toBase64(jsSig),
  jsFp, goFp,
  jsEphPrivB64: C.toBase64(jsEph.privateKey),
  jsEphPubB64: C.toBase64(jsEph.publicKey),
  xSharedB64: C.toBase64(xShared),
  pAB64: C.toBase64(st.pA),
  spakeState: {
    pin: st.pin,
    windowId: st.windowId,
    wDec: st.w.toString(),
    xDec: st.x.toString(),
    pAB64: C.toBase64(st.pA),
  },
}));
`
	cmd1 := exec.Command(nodeBin, "-e", script1, cryptoJSPath)
	cmd1.Stdin = strings.NewReader(string(inBytes))
	out1Bytes, err := cmd1.CombinedOutput()
	if err != nil {
		t.Fatalf("node step 1 failed: %v\n%s", err, out1Bytes)
	}
	var res1 struct {
		Sha2           string          `json:"sha2"`
		Sha5           string          `json:"sha5"`
		HMAC           string          `json:"hmac"`
		HKDF           string          `json:"hkdf"`
		VerifyGo       bool            `json:"verifyGo"`
		VerifyTampered bool            `json:"verifyTampered"`
		JsPubB64       string          `json:"jsPubB64"`
		JsSigB64       string          `json:"jsSigB64"`
		JsFp           string          `json:"jsFp"`
		GoFp           string          `json:"goFp"`
		JsEphPrivB64   string          `json:"jsEphPrivB64"`
		JsEphPubB64    string          `json:"jsEphPubB64"`
		XSharedB64     string          `json:"xSharedB64"`
		PAB64          string          `json:"pAB64"`
		SpakeState     json.RawMessage `json:"spakeState"`
	}
	if err := json.Unmarshal(out1Bytes, &res1); err != nil {
		t.Fatalf("decode node step 1 output: %v\n%s", err, out1Bytes)
	}

	rawMsg := []byte("PoundControl mutual auth test payload — 123456!")
	wantSha2 := sha256.Sum256(rawMsg)
	wantSha5 := sha512.Sum512(rawMsg)
	if res1.Sha2 != base64.StdEncoding.EncodeToString(wantSha2[:]) {
		t.Fatal("SHA-256 mismatch between Go and JS")
	}
	if res1.Sha5 != base64.StdEncoding.EncodeToString(wantSha5[:]) {
		t.Fatal("SHA-512 mismatch between Go and JS")
	}
	if res1.HMAC != base64.StdEncoding.EncodeToString(computeHMACSHA256(hPub, rawMsg)) {
		t.Fatal("HMAC-SHA256 mismatch between Go and JS")
	}
	if !res1.VerifyGo || res1.VerifyTampered {
		t.Fatalf("JS ed25519 verify unexpected: verifyGo=%v verifyTampered=%v", res1.VerifyGo, res1.VerifyTampered)
	}
	if res1.JsPubB64 != base64.StdEncoding.EncodeToString(cPub) {
		t.Fatal("Ed25519 publicKeyFromSeed mismatch between Go and JS")
	}
	jsSigBytes, _ := base64.StdEncoding.DecodeString(res1.JsSigB64)
	if !ed25519.Verify(cPub, rawMsg, jsSigBytes) {
		t.Fatal("Go failed to verify JS Ed25519 signature")
	}
	if res1.JsFp != PublicKeyFingerprint(cPub) || res1.GoFp != PublicKeyFingerprint(hPub) {
		t.Fatal("fingerprint mismatch between Go and JS")
	}

	jsEphPub, _ := base64.StdEncoding.DecodeString(res1.JsEphPubB64)
	goXShared, err := computeX25519Shared(goEphPriv, jsEphPub)
	if err != nil {
		t.Fatalf("computeX25519Shared: %v", err)
	}
	if res1.XSharedB64 != base64.StdEncoding.EncodeToString(goXShared) {
		t.Fatal("X25519 shared secret mismatch between Go and JS")
	}

	// Step 2: Go finishes SPAKE2 server computation & WS transcript test inputs -> JS completes & compares.
	pA, _ := base64.StdEncoding.DecodeString(res1.PAB64)
	srvMat, err := spake2ServerCompute(pin, windowID, cPub, hPub, pA)
	if err != nil {
		t.Fatalf("spake2ServerCompute: %v", err)
	}
	hostPairSig := ed25519.Sign(hPriv, pairHostSigPayload(srvMat.hashTT))
	statusRespMAC := pairStatusRespMAC(srvMat.kcB, attemptID, "approved", deviceID)

	serverNonce := make([]byte, 32)
	clientNonce := make([]byte, 32)
	_, _ = rand.Read(serverNonce)
	_, _ = rand.Read(clientNonce)
	t1Go := buildWSTranscript1(hostID, deviceID, hPub, cPub, serverNonce, clientNonce, jsEphPub)
	t2Go := buildWSTranscript2(t1Go, goEphPub)
	hostWSSig := ed25519.Sign(hPriv, t2Go)
	cMacGo, sMacGo, aMacGo, err := deriveWSSessionKeys(goXShared, t2Go)
	if err != nil {
		t.Fatalf("deriveWSSessionKeys: %v", err)
	}
	sampleFrame := `{"type":"ping"}`
	goC2SMAC := computeFrameMAC(cMacGo, "c2s", 1, sampleFrame)
	goS2CMAC := computeFrameMAC(sMacGo, "s2c", 2, sampleFrame)
	goAssetMAC := computeAssetMAC(aMacGo, "sess-42", "asset-99")

	step2In, _ := json.Marshal(map[string]any{
		"spakeState":     res1.SpakeState,
		"clientSeed":     base64.StdEncoding.EncodeToString(cPriv[:32]),
		"clientPubB64":   base64.StdEncoding.EncodeToString(cPub),
		"hostPubB64":     base64.StdEncoding.EncodeToString(hPub),
		"pBB64":          base64.StdEncoding.EncodeToString(srvMat.pB),
		"hostPairSigB64": base64.StdEncoding.EncodeToString(hostPairSig),
		"attemptId":      attemptID,
		"hostId":         hostID,
		"deviceId":       deviceID,
		"serverNonceB64": base64.StdEncoding.EncodeToString(serverNonce),
		"clientNonceB64": base64.StdEncoding.EncodeToString(clientNonce),
		"jsEphPrivB64":   res1.JsEphPrivB64,
		"jsEphPubB64":    res1.JsEphPubB64,
		"goEphPubB64":    base64.StdEncoding.EncodeToString(goEphPub),
		"hostWSSigB64":   base64.StdEncoding.EncodeToString(hostWSSig),
		"sampleFrame":    sampleFrame,
	})

	script2 := `
const C = require(process.argv[1]);
const inp = JSON.parse(require('fs').readFileSync(0, 'utf8'));
const st = {
  pin: inp.spakeState.pin,
  windowId: inp.spakeState.windowId,
  w: BigInt(inp.spakeState.wDec),
  x: BigInt(inp.spakeState.xDec),
  pA: C.fromBase64(inp.spakeState.pAB64),
};
const cPub = C.fromBase64(inp.clientPubB64);
const cSeed = C.fromBase64(inp.clientSeed);
const hPub = C.fromBase64(inp.hostPubB64);
const pB = C.fromBase64(inp.pBB64);
const mat = C.spake2.clientFinish(st, cPub, hPub, pB);
const hostSigOK = C.ed25519.verify(hPub, C.spake2.hostSigPayload(mat.hashTT), C.fromBase64(inp.hostPairSigB64));
const clientSig = C.ed25519.sign(cSeed, C.spake2.clientSigPayload(mat.hashTT));
const confirmMac = C.spake2.pairConfirmMAC(mat.kcA, inp.attemptId, true);
const statusReqMac = C.spake2.pairStatusReqMAC(mat.kcA, inp.attemptId);
const statusRespMac = C.spake2.pairStatusRespMAC(mat.kcB, inp.attemptId, 'approved', inp.deviceId);

const t1 = C.wsAuth.buildTranscript1(
  inp.hostId, inp.deviceId, hPub, cPub,
  C.fromBase64(inp.serverNonceB64), C.fromBase64(inp.clientNonceB64), C.fromBase64(inp.jsEphPubB64)
);
const t2 = C.wsAuth.buildTranscript2(t1, C.fromBase64(inp.goEphPubB64));
const wsHostSigOK = C.ed25519.verify(hPub, t2, C.fromBase64(inp.hostWSSigB64));
const shared = C.x25519.sharedSecret(C.fromBase64(inp.jsEphPrivB64), C.fromBase64(inp.goEphPubB64));
const keys = C.wsAuth.deriveSessionKeys(shared, t2);
const c2sMac = C.wsAuth.frameMAC(keys.clientMacKey, 'c2s', 1, inp.sampleFrame);
const s2cMac = C.wsAuth.frameMAC(keys.serverMacKey, 's2c', 2, inp.sampleFrame);
const assetMac = C.wsAuth.assetMAC(keys.assetMacKey, 'sess-42', 'asset-99');

process.stdout.write(JSON.stringify({
  KB64: C.toBase64(mat.K),
  cAB64: C.toBase64(mat.cA),
  cBB64: C.toBase64(mat.cB),
  sas: mat.sas,
  hostSigOK,
  clientSigB64: C.toBase64(clientSig),
  confirmMacB64: C.toBase64(confirmMac),
  statusReqMacB64: C.toBase64(statusReqMac),
  statusRespMacB64: C.toBase64(statusRespMac),
  wsHostSigOK,
  c2sMacB64: C.toBase64(c2sMac),
  s2cMacB64: C.toBase64(s2cMac),
  assetMac,
}));
`
	cmd2 := exec.Command(nodeBin, "-e", script2, cryptoJSPath)
	cmd2.Stdin = strings.NewReader(string(step2In))
	out2Bytes, err := cmd2.CombinedOutput()
	if err != nil {
		t.Fatalf("node step 2 failed: %v\n%s", err, out2Bytes)
	}
	var res2 struct {
		KB64             string `json:"KB64"`
		CAB64            string `json:"cAB64"`
		CBB64            string `json:"cBB64"`
		SAS              string `json:"sas"`
		HostSigOK        bool   `json:"hostSigOK"`
		ClientSigB64     string `json:"clientSigB64"`
		ConfirmMacB64    string `json:"confirmMacB64"`
		StatusReqMacB64  string `json:"statusReqMacB64"`
		StatusRespMacB64 string `json:"statusRespMacB64"`
		WSHostSigOK      bool   `json:"wsHostSigOK"`
		C2SMacB64        string `json:"c2sMacB64"`
		S2CMacB64        string `json:"s2cMacB64"`
		AssetMac         string `json:"assetMac"`
	}
	if err := json.Unmarshal(out2Bytes, &res2); err != nil {
		t.Fatalf("decode node step 2 output: %v\n%s", err, out2Bytes)
	}

	if res2.KB64 != base64.StdEncoding.EncodeToString(srvMat.K) {
		t.Fatal("SPAKE2 shared point K mismatch between Go and JS")
	}
	if res2.CAB64 != base64.StdEncoding.EncodeToString(srvMat.cA) ||
		res2.CBB64 != base64.StdEncoding.EncodeToString(srvMat.cB) {
		t.Fatal("SPAKE2 confirmation MAC mismatch between Go and JS")
	}
	if res2.SAS != srvMat.sas || !res2.HostSigOK || !res2.WSHostSigOK {
		t.Fatalf("SAS/Sig interop failed: sas=%q/%q hostSig=%v wsHostSig=%v", res2.SAS, srvMat.sas, res2.HostSigOK, res2.WSHostSigOK)
	}
	cSigDec, _ := base64.StdEncoding.DecodeString(res2.ClientSigB64)
	if !ed25519.Verify(cPub, pairClientSigPayload(srvMat.hashTT), cSigDec) {
		t.Fatal("Go failed to verify JS pairing ownership signature")
	}
	if res2.ConfirmMacB64 != base64.StdEncoding.EncodeToString(pairConfirmMAC(srvMat.kcA, attemptID, true)) {
		t.Fatal("confirm MAC mismatch")
	}
	if res2.StatusReqMacB64 != base64.StdEncoding.EncodeToString(pairStatusReqMAC(srvMat.kcA, attemptID)) {
		t.Fatal("status request MAC mismatch")
	}
	if res2.StatusRespMacB64 != base64.StdEncoding.EncodeToString(statusRespMAC) {
		t.Fatal("status response MAC mismatch")
	}
	if res2.C2SMacB64 != base64.StdEncoding.EncodeToString(goC2SMAC) ||
		res2.S2CMacB64 != base64.StdEncoding.EncodeToString(goS2CMAC) ||
		res2.AssetMac != goAssetMAC {
		t.Fatal("WebSocket session frame/asset MAC mismatch between Go and JS")
	}
}
