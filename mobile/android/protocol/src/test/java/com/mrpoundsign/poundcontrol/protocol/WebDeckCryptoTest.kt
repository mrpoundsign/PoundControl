package com.mrpoundsign.poundcontrol.protocol

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class WebDeckCryptoTest {
    @Test
    fun ed25519MatchesRfc8032Vector1() {
        // RFC 8032 Section 7.1 TEST 1 (empty message)
        val seed = hexToBytes("9d61b19deffd5a60ba844af492ec2cc44449c5697b326919703bac031cae7f60")
        val expectedPub = hexToBytes("d75a980182b10ab7d54bfed3c964073a0ee172f3daa62325af021a68f707511a")
        val expectedSig = hexToBytes(
            "e5564300c360ac729086e2cc806e828a84877f1eb8e5d974d873e06522490155" +
                "5fb8821590a33bacc61e39701cf9b46bd25bf5f0595bbe24655141438e7a100b",
        )

        val pub = WebDeckCrypto.ed25519PublicKeyFromSeed(seed)
        assertArrayEquals(expectedPub, pub)

        val sig = WebDeckCrypto.ed25519Sign(seed, ByteArray(0))
        assertArrayEquals(expectedSig, sig)
        assertTrue(WebDeckCrypto.ed25519Verify(pub, ByteArray(0), sig))

        val tampered = sig.copyOf().apply { this[0] = (this[0].toInt() xor 0x01).toByte() }
        assertFalse(WebDeckCrypto.ed25519Verify(pub, ByteArray(0), tampered))
        assertFalse(WebDeckCrypto.ed25519Verify(pub, "x".toByteArray(), sig))
    }

    @Test
    fun x25519MatchesRfc7748VectorAndRejectsLowOrder() {
        // RFC 7748 Section 6.1
        val alicePriv = hexToBytes("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")
        val bobPub = hexToBytes("de9edb7d7b7dc1b4d35b61c2ece435373f8343c85b78674dadfc7e146f882b4f")
        val expectedShared = hexToBytes("4a5d9d5ba4ce2de1728e3bf480350f25e07e21c947d19e3376f09b3c1e161742")

        val shared = WebDeckCrypto.x25519SharedSecret(alicePriv, bobPub)
        assertArrayEquals(expectedShared, shared)

        var threw = false
        try {
            WebDeckCrypto.x25519SharedSecret(alicePriv, ByteArray(32))
        } catch (_: IllegalArgumentException) {
            threw = true
        }
        assertTrue(threw)
    }

    @Test
    fun wsTranscriptAndSessionKeysMatchGoLayout() {
        val hostId = "host-1"
        val deviceId = "dev-2"
        val hostPub = ByteArray(32) { (it + 1).toByte() }
        val clientPub = ByteArray(32) { (it + 40).toByte() }
        val sNonce = ByteArray(32) { (it + 80).toByte() }
        val cNonce = ByteArray(32) { (it + 120).toByte() }
        val cEph = ByteArray(32) { (it + 160).toByte() }
        val sEph = ByteArray(32) { (it + 200).toByte() }

        val t1 = WebDeckCrypto.buildWSTranscript1(hostId, deviceId, hostPub, clientPub, sNonce, cNonce, cEph)
        val expectedLen = "poundcontrol-ws-v2".length + 8 + hostId.length + 8 + deviceId.length + 32 * 5
        assertEquals(expectedLen, t1.size)

        // Check little-endian 8-byte length prefix of hostId ("host-1" = 6).
        val domainLen = "poundcontrol-ws-v2".length
        assertEquals(6, t1[domainLen].toInt() and 0xff)
        assertEquals(0, t1[domainLen + 1].toInt() and 0xff)

        val t2 = WebDeckCrypto.buildWSTranscript2(t1, sEph)
        assertEquals(t1.size + 32, t2.size)

        val shared = ByteArray(32) { 7 }
        val keys1 = WebDeckCrypto.deriveWSSessionKeys(shared, t2)
        val keys2 = WebDeckCrypto.deriveWSSessionKeys(shared, t2)
        assertArrayEquals(keys1.clientMacKey, keys2.clientMacKey)
        assertArrayEquals(keys1.serverMacKey, keys2.serverMacKey)
        assertArrayEquals(keys1.assetMacKey, keys2.assetMacKey)
        assertFalse(keys1.clientMacKey.contentEquals(keys1.serverMacKey))

        val macA = WebDeckCrypto.computeFrameMAC(keys1.clientMacKey, "c2s", 1L, """{"type":"ping"}""")
        val macB = WebDeckCrypto.computeFrameMAC(keys1.clientMacKey, "c2s", 2L, """{"type":"ping"}""")
        assertEquals(32, macA.size)
        assertFalse(macA.contentEquals(macB))
    }

    private fun hexToBytes(hex: String): ByteArray {
        val out = ByteArray(hex.length / 2)
        for (i in out.indices) {
            out[i] = hex.substring(i * 2, i * 2 + 2).toInt(16).toByte()
        }
        return out
    }
}
