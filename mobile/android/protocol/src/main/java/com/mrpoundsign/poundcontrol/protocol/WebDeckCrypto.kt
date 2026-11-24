package com.mrpoundsign.poundcontrol.protocol

import java.math.BigInteger
import java.nio.ByteBuffer
import java.nio.ByteOrder
import java.security.MessageDigest
import java.security.SecureRandom
import java.util.Base64
import javax.crypto.Mac
import javax.crypto.spec.SecretKeySpec

/**
 * Pure-Kotlin cryptographic helpers matching PoundControl Go `internal/webdeck/auth.go`
 * and `internal/webdeck/static/crypto.js` wire-for-wire (Ed25519, X25519, HKDF-SHA256,
 * HMAC-SHA256, and WebSocket v2 mutual-auth transcript/frame MAC helpers).
 */
object WebDeckCrypto {
    private val rng = SecureRandom()

    private val P: BigInteger = BigInteger.ONE.shiftLeft(255).subtract(BigInteger.valueOf(19))
    private val L: BigInteger = BigInteger.ONE.shiftLeft(252).add(BigInteger("27742317777372353535851937790883648493"))
    private val D: BigInteger = modP(
        BigInteger.valueOf(-121665).multiply(invP(BigInteger.valueOf(121666))),
    )
    private val TWO_D: BigInteger = modP(D.shiftLeft(1))
    private val SQRT_M1: BigInteger = BigInteger.valueOf(2).modPow(P.subtract(BigInteger.ONE).shiftRight(2), P)
    private val A24: BigInteger = BigInteger.valueOf(121665)

    private val GX = BigInteger("15112221349535400772501151409588531511454012693041857206046113283949847762202")
    private val GY = BigInteger("46316835694926478169428394003475163141307993866256225615783033603165251855960")

    private data class EdPoint(
        val x: BigInteger,
        val y: BigInteger,
        val z: BigInteger,
        val t: BigInteger,
    )

    private val POINT_ZERO = EdPoint(BigInteger.ZERO, BigInteger.ONE, BigInteger.ONE, BigInteger.ZERO)
    private val POINT_G = EdPoint(GX, GY, BigInteger.ONE, modP(GX.multiply(GY)))
    private val X25519_BASE = ByteArray(32).apply { this[0] = 9 }

    data class X25519KeyPair(
        val privateKey: ByteArray,
        val publicKey: ByteArray,
    )

    data class WSSessionKeys(
        val clientMacKey: ByteArray,
        val serverMacKey: ByteArray,
        val assetMacKey: ByteArray,
    )

    fun randomBytes(len: Int): ByteArray = ByteArray(len).also { rng.nextBytes(it) }

    fun toBase64(bytes: ByteArray): String = Base64.getEncoder().encodeToString(bytes)

    fun fromBase64(raw: String): ByteArray {
        val clean = raw.trim().replace('-', '+').replace('_', '/')
        val pad = when (clean.length % 4) {
            0 -> ""
            else -> "=".repeat(4 - clean.length % 4)
        }
        return Base64.getDecoder().decode(clean + pad)
    }

    fun constantTimeEquals(a: ByteArray, b: ByteArray): Boolean {
        if (a.size != b.size) return false
        var diff = 0
        for (i in a.indices) {
            diff = diff or (a[i].toInt() xor b[i].toInt())
        }
        return diff == 0
    }

    fun sha256(data: ByteArray): ByteArray = MessageDigest.getInstance("SHA-256").digest(data)

    fun sha512(data: ByteArray): ByteArray = MessageDigest.getInstance("SHA-512").digest(data)

    fun hmacSha256(key: ByteArray, data: ByteArray): ByteArray {
        val normalizedKey = if (key.isEmpty()) ByteArray(1) else key
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(normalizedKey, "HmacSHA256"))
        return mac.doFinal(data)
    }

    fun hkdfSha256(ikm: ByteArray, salt: ByteArray?, info: ByteArray, length: Int): ByteArray {
        val actualSalt = if (salt == null || salt.isEmpty()) ByteArray(32) else salt
        val prk = hmacSha256(actualSalt, ikm)
        val blocks = (length + 31) / 32
        val okm = ByteArray(blocks * 32)
        var prev = ByteArray(0)
        for (i in 1..blocks) {
            val input = ByteArray(prev.size + info.size + 1)
            prev.copyInto(input, 0)
            info.copyInto(input, prev.size)
            input[prev.size + info.size] = i.toByte()
            prev = hmacSha256(prk, input)
            prev.copyInto(okm, (i - 1) * 32)
        }
        return okm.copyOf(length)
    }

    fun ed25519PublicKeyFromSeed(seed: ByteArray): ByteArray {
        require(seed.size == 32) { "Ed25519 seed must be 32 bytes" }
        val h = sha512(seed)
        val a = clampEdSeedHash(h)
        return pointEncode(pointMul(POINT_G, a))
    }

    fun ed25519Sign(seedOrPriv: ByteArray, msg: ByteArray): ByteArray {
        val seed = when (seedOrPriv.size) {
            32 -> seedOrPriv
            64 -> seedOrPriv.copyOfRange(0, 32)
            else -> throw IllegalArgumentException("Invalid Ed25519 seed/private key length: ${seedOrPriv.size}")
        }
        val h = sha512(seed)
        val a = clampEdSeedHash(h)
        val prefix = h.copyOfRange(32, 64)
        val pub = pointEncode(pointMul(POINT_G, a))

        val r = bytesToBigIntLE(sha512(concat(prefix, msg))).mod(L)
        val rBytes = pointEncode(pointMul(POINT_G, r))
        val k = bytesToBigIntLE(sha512(concat(rBytes, pub, msg))).mod(L)
        val s = r.add(k.multiply(a)).mod(L)
        return concat(rBytes, bigIntToBytesLE(s, 32))
    }

    fun ed25519Verify(pub: ByteArray, msg: ByteArray, sig: ByteArray): Boolean {
        return try {
            if (pub.size != 32 || sig.size != 64) return false
            val aPoint = pointDecode(pub)
            val rBytes = sig.copyOfRange(0, 32)
            val rPoint = pointDecode(rBytes)
            val s = bytesToBigIntLE(sig.copyOfRange(32, 64))
            if (s >= L) return false
            val k = bytesToBigIntLE(sha512(concat(rBytes, pub, msg))).mod(L)
            val lhs = pointMul(POINT_G, s)
            val rhs = pointAdd(rPoint, pointMul(aPoint, k))
            pointEqual(lhs, rhs)
        } catch (_: Exception) {
            false
        }
    }

    fun x25519GenerateKeypair(): X25519KeyPair {
        val priv = randomBytes(32)
        val pub = x25519ScalarMult(priv, X25519_BASE)
        return X25519KeyPair(priv, pub)
    }

    fun x25519SharedSecret(privateKey: ByteArray, peerPublicKey: ByteArray): ByteArray {
        require(privateKey.size == 32) { "X25519 private key must be 32 bytes" }
        require(peerPublicKey.size == 32) { "X25519 public key must be 32 bytes" }
        val shared = x25519ScalarMult(privateKey, peerPublicKey)
        var nonZero = 0
        for (b in shared) {
            nonZero = nonZero or (b.toInt() and 0xff)
        }
        require(nonZero != 0) { "Low-order X25519 peer public key rejected" }
        return shared
    }

    fun buildWSTranscript1(
        hostId: String,
        deviceId: String,
        hostPub: ByteArray,
        clientPub: ByteArray,
        serverNonce: ByteArray,
        clientNonce: ByteArray,
        clientEphPub: ByteArray,
    ): ByteArray {
        val domain = "poundcontrol-ws-v2".toByteArray(Charsets.UTF_8)
        val hostIdBytes = hostId.toByteArray(Charsets.UTF_8)
        val devIdBytes = deviceId.toByteArray(Charsets.UTF_8)
        return concat(
            domain,
            lengthPrefixLE(hostIdBytes),
            lengthPrefixLE(devIdBytes),
            hostPub,
            clientPub,
            serverNonce,
            clientNonce,
            clientEphPub,
        )
    }

    fun buildWSTranscript2(t1: ByteArray, serverEphPub: ByteArray): ByteArray = concat(t1, serverEphPub)

    fun deriveWSSessionKeys(sharedSecret: ByteArray, t2: ByteArray): WSSessionKeys {
        val salt = sha256(t2)
        val okm = hkdfSha256(
            ikm = sharedSecret,
            salt = salt,
            info = "poundcontrol-ws-keys-v2".toByteArray(Charsets.UTF_8),
            length = 96,
        )
        return WSSessionKeys(
            clientMacKey = okm.copyOfRange(0, 32),
            serverMacKey = okm.copyOfRange(32, 64),
            assetMacKey = okm.copyOfRange(64, 96),
        )
    }

    fun computeFrameMAC(macKey: ByteArray, dir: String, seq: Long, payload: String): ByteArray {
        val msg = "$dir:$seq:$payload".toByteArray(Charsets.UTF_8)
        return hmacSha256(macKey, msg)
    }

    private fun lengthPrefixLE(elem: ByteArray): ByteArray {
        val out = ByteArray(8 + elem.size)
        ByteBuffer.wrap(out, 0, 8).order(ByteOrder.LITTLE_ENDIAN).putLong(elem.size.toLong())
        elem.copyInto(out, 8)
        return out
    }

    private fun concat(vararg parts: ByteArray): ByteArray {
        var total = 0
        for (p in parts) total += p.size
        val out = ByteArray(total)
        var offset = 0
        for (p in parts) {
            p.copyInto(out, offset)
            offset += p.size
        }
        return out
    }

    private fun modP(a: BigInteger): BigInteger {
        val r = a.remainder(P)
        return if (r.signum() >= 0) r else r.add(P)
    }

    private fun invP(a: BigInteger): BigInteger = a.modPow(P.subtract(BigInteger.valueOf(2)), P)

    private fun bytesToBigIntLE(bytes: ByteArray): BigInteger {
        val be = ByteArray(bytes.size)
        for (i in bytes.indices) {
            be[bytes.size - 1 - i] = bytes[i]
        }
        return BigInteger(1, be)
    }

    private fun bigIntToBytesLE(n: BigInteger, len: Int): ByteArray {
        val out = ByteArray(len)
        var v = n
        val mask = BigInteger.valueOf(0xff)
        for (i in 0 until len) {
            out[i] = v.and(mask).toInt().toByte()
            v = v.shiftRight(8)
        }
        return out
    }

    private fun clampEdSeedHash(h: ByteArray): BigInteger {
        val aBytes = h.copyOfRange(0, 32)
        aBytes[0] = (aBytes[0].toInt() and 248).toByte()
        aBytes[31] = (aBytes[31].toInt() and 127).toByte()
        aBytes[31] = (aBytes[31].toInt() or 64).toByte()
        return bytesToBigIntLE(aBytes)
    }

    private fun pointAdd(p1: EdPoint, p2: EdPoint): EdPoint {
        val a = modP(p1.y.subtract(p1.x).multiply(p2.y.subtract(p2.x)))
        val b = modP(p1.y.add(p1.x).multiply(p2.y.add(p2.x)))
        val c = modP(p1.t.multiply(TWO_D).multiply(p2.t))
        val dVal = modP(p1.z.shiftLeft(1).multiply(p2.z))
        val e = modP(b.subtract(a))
        val f = modP(dVal.subtract(c))
        val g = modP(dVal.add(c))
        val h = modP(b.add(a))
        return EdPoint(modP(e.multiply(f)), modP(g.multiply(h)), modP(f.multiply(g)), modP(e.multiply(h)))
    }

    private fun pointMul(p: EdPoint, scalar: BigInteger): EdPoint {
        var q = POINT_ZERO
        var base = p
        var k = scalar
        while (k.signum() > 0) {
            if (k.testBit(0)) {
                q = pointAdd(q, base)
            }
            k = k.shiftRight(1)
            if (k.signum() > 0) {
                base = pointAdd(base, base)
            }
        }
        return q
    }

    private fun pointEqual(p1: EdPoint, p2: EdPoint): Boolean {
        return modP(p1.x.multiply(p2.z)) == modP(p2.x.multiply(p1.z)) &&
            modP(p1.y.multiply(p2.z)) == modP(p2.y.multiply(p1.z))
    }

    private fun pointEncode(p: EdPoint): ByteArray {
        val zInv = invP(p.z)
        val x = modP(p.x.multiply(zInv))
        val y = modP(p.y.multiply(zInv))
        val out = bigIntToBytesLE(y, 32)
        if (x.testBit(0)) {
            out[31] = (out[31].toInt() or 0x80).toByte()
        }
        return out
    }

    private fun pointDecode(bytes: ByteArray): EdPoint {
        require(bytes.size == 32) { "Invalid Edwards25519 point length" }
        val copy = bytes.copyOf()
        val xSign = (copy[31].toInt() ushr 7) and 1
        copy[31] = (copy[31].toInt() and 0x7f).toByte()
        val y = bytesToBigIntLE(copy)
        require(y < P) { "Non-canonical Edwards25519 y-coordinate" }
        val y2 = modP(y.multiply(y))
        val u = modP(y2.subtract(BigInteger.ONE))
        val v = modP(D.multiply(y2).add(BigInteger.ONE))
        val v2 = modP(v.multiply(v))
        val v3 = modP(v2.multiply(v))
        val v7 = modP(v3.multiply(v3).multiply(v))
        val exp = P.subtract(BigInteger.valueOf(5)).shiftRight(3)
        var x = modP(u.multiply(v3).multiply(modP(u.multiply(v7)).modPow(exp, P)))
        var vx2 = modP(v.multiply(x).multiply(x))
        if (vx2 != u) {
            if (vx2 == modP(u.negate())) {
                x = modP(x.multiply(SQRT_M1))
                vx2 = modP(v.multiply(x).multiply(x))
            }
            require(vx2 == u) { "Point not on Edwards25519 curve" }
        }
        require(!(x.signum() == 0 && xSign == 1)) { "Invalid negative zero point coordinate" }
        if ((if (x.testBit(0)) 1 else 0) != xSign) {
            x = modP(P.subtract(x))
        }
        return EdPoint(x, y, BigInteger.ONE, modP(x.multiply(y)))
    }

    private fun x25519ScalarMult(kBytes: ByteArray, uBytes: ByteArray): ByteArray {
        val kArr = kBytes.copyOf()
        kArr[0] = (kArr[0].toInt() and 248).toByte()
        kArr[31] = (kArr[31].toInt() and 127).toByte()
        kArr[31] = (kArr[31].toInt() or 64).toByte()
        val k = bytesToBigIntLE(kArr)

        val uArr = uBytes.copyOf()
        uArr[31] = (uArr[31].toInt() and 127).toByte()
        val u = bytesToBigIntLE(uArr)

        val x1 = u
        var x2 = BigInteger.ONE
        var z2 = BigInteger.ZERO
        var x3 = u
        var z3 = BigInteger.ONE
        var swap = 0

        for (t in 254 downTo 0) {
            val kt = if (k.testBit(t)) 1 else 0
            swap = swap xor kt
            if (swap != 0) {
                val tx = x2
                x2 = x3
                x3 = tx
                val tz = z2
                z2 = z3
                z3 = tz
            }
            swap = kt

            val a = modP(x2.add(z2))
            val aa = modP(a.multiply(a))
            val b = modP(x2.subtract(z2))
            val bb = modP(b.multiply(b))
            val e = modP(aa.subtract(bb))
            val c = modP(x3.add(z3))
            val dM = modP(x3.subtract(z3))
            val da = modP(dM.multiply(a))
            val cb = modP(c.multiply(b))
            val sumDacb = modP(da.add(cb))
            val diffDacb = modP(da.subtract(cb))
            x3 = modP(sumDacb.multiply(sumDacb))
            z3 = modP(x1.multiply(modP(diffDacb.multiply(diffDacb))))
            x2 = modP(aa.multiply(bb))
            z2 = modP(e.multiply(aa.add(modP(A24.multiply(e)))))
        }

        if (swap != 0) {
            val tx = x2
            x2 = x3
            x3 = tx
            val tz = z2
            z2 = z3
            z3 = tz
        }

        val res = modP(x2.multiply(invP(z2)))
        return bigIntToBytesLE(res, 32)
    }
}
