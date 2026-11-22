package com.mrpoundsign.pounddeck

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class DeckUrlValidatorTest {

    @Test
    fun normalizeAcceptsValidHttpAndBareLanIp() {
        assertEquals(
            "http://192.168.1.42:9120/#pair=123456&fp=A1B2-C3D4-E5F6",
            DeckUrlValidator.normalizeUrl("http://192.168.1.42:9120/#pair=123456&fp=A1B2-C3D4-E5F6"),
        )
        assertEquals(
            "http://192.168.1.42:9120/?q=1#frag",
            DeckUrlValidator.normalizeUrl("http://192.168.1.42:9120/?q=1#frag"),
        )
        assertEquals(
            "http://192.168.1.42:9120/",
            DeckUrlValidator.normalizeUrl("192.168.1.42"),
        )
        assertEquals(
            "http://192.168.1.42:8080/",
            DeckUrlValidator.normalizeUrl("192.168.1.42:8080"),
        )
        assertEquals(
            "https://deck.local/",
            DeckUrlValidator.normalizeUrl("  https://deck.local  "),
        )
    }

    @Test
    fun normalizeRejectsDangerousOrMalformedSchemes() {
        assertNull(DeckUrlValidator.normalizeUrl(""))
        assertNull(DeckUrlValidator.normalizeUrl("javascript:alert(1)"))
        assertNull(DeckUrlValidator.normalizeUrl("file:///etc/passwd"))
        assertNull(DeckUrlValidator.normalizeUrl("data:text/html,<h1>x</h1>"))
        assertNull(DeckUrlValidator.normalizeUrl("ftp://192.168.1.10/"))
        assertNull(DeckUrlValidator.normalizeUrl("http://user:pass@192.168.1.10:9120/"))
        assertNull(DeckUrlValidator.normalizeUrl("not a url"))
    }

    @Test
    fun baseOriginUrlStripsPairTokensAndIsSameHostEnforcesPort() {
        val base = DeckUrlValidator.baseOriginUrl("http://192.168.1.42:9120/?pair=secret_token")
        assertEquals("http://192.168.1.42:9120/", base)
        assertEquals(
            "http://192.168.1.42:9120/",
            DeckUrlValidator.baseOriginUrl("http://192.168.1.42:9120/#pair=123456&fp=A1B2-C3D4-E5F6"),
        )

        assertTrue(DeckUrlValidator.isSameHost("http://192.168.1.42:9120/api/auth/status", base!!))
        assertFalse(DeckUrlValidator.isSameHost("http://192.168.1.42:9999/", base))
        assertFalse(DeckUrlValidator.isSameHost("https://192.168.1.42:9120/", base))
        assertFalse(DeckUrlValidator.isSameHost("http://evil.example:9120/", base))
    }
}
