package com.mrpoundsign.pounddeck

import java.net.URI
import java.util.Locale

object DeckUrlValidator {
    private const val DEFAULT_PORT = 9120

    fun normalizeUrl(raw: String): String? {
        val trimmed = raw.trim()
        if (trimmed.isEmpty() || trimmed.any { it.isWhitespace() }) {
            return null
        }

        val lower = trimmed.lowercase(Locale.ROOT)
        val candidate = when {
            lower.startsWith("http://") || lower.startsWith("https://") -> trimmed
            "://" in trimmed -> return null
            hasDisallowedColonScheme(lower) -> return null
            else -> "http://$trimmed"
        }

        val uri = try {
            URI(candidate)
        } catch (_: Exception) {
            return null
        }

        val scheme = uri.scheme?.lowercase(Locale.ROOT) ?: return null
        if (scheme != "http" && scheme != "https") {
            return null
        }
        val host = uri.host?.takeIf { it.isNotBlank() } ?: return null
        if (!uri.userInfo.isNullOrEmpty()) {
            return null
        }

        val formattedHost = if (':' in host && !host.startsWith("[")) "[$host]" else host
        val explicitPort = uri.port
        val hadScheme = lower.startsWith("http://") || lower.startsWith("https://")
        val port = when {
            explicitPort > 0 -> explicitPort
            !hadScheme -> DEFAULT_PORT
            else -> -1
        }
        val portPart = if (port > 0) ":$port" else ""
        val path = uri.rawPath?.takeIf { it.isNotEmpty() } ?: "/"
        val queryPart = uri.rawQuery?.takeIf { it.isNotEmpty() }?.let { "?$it" } ?: ""

        return "$scheme://$formattedHost$portPart$path$queryPart"
    }

    fun baseOriginUrl(fullUrl: String): String? {
        val normalized = normalizeUrl(fullUrl) ?: return null
        val uri = try {
            URI(normalized)
        } catch (_: Exception) {
            return null
        }
        val scheme = uri.scheme?.lowercase(Locale.ROOT) ?: return null
        val host = uri.host?.takeIf { it.isNotBlank() } ?: return null
        val formattedHost = if (':' in host && !host.startsWith("[")) "[$host]" else host
        val portPart = if (uri.port > 0) ":${uri.port}" else ""
        return "$scheme://$formattedHost$portPart/"
    }

    fun isSameHost(targetUrl: String, allowedBaseUrl: String): Boolean {
        val targetNorm = normalizeUrl(targetUrl) ?: return false
        val baseNorm = normalizeUrl(allowedBaseUrl) ?: return false
        return try {
            val targetUri = URI(targetNorm)
            val baseUri = URI(baseNorm)
            targetUri.scheme.equals(baseUri.scheme, ignoreCase = true) &&
                targetUri.host.equals(baseUri.host, ignoreCase = true) &&
                effectivePort(targetUri) == effectivePort(baseUri)
        } catch (_: Exception) {
            false
        }
    }

    private fun effectivePort(uri: URI): Int {
        if (uri.port > 0) {
            return uri.port
        }
        return if (uri.scheme.equals("https", ignoreCase = true)) 443 else 80
    }

    private fun hasDisallowedColonScheme(lower: String): Boolean {
        val colonIdx = lower.indexOf(':')
        if (colonIdx <= 0) {
            return false
        }
        val prefix = lower.substring(0, colonIdx)
        if (prefix.any { !it.isLetter() && it != '+' && it != '-' && it != '.' }) {
            return false
        }
        // Only treat unambiguous dangerous schemes or non-host prefixes as invalid.
        return prefix in setOf("javascript", "file", "data", "content", "about", "vbscript", "intent", "tel", "mailto")
    }
}
