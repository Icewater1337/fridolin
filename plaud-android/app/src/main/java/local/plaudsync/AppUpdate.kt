package local.plaudsync

import java.io.IOException
import java.io.InputStream
import java.io.OutputStream
import java.nio.ByteBuffer
import java.security.MessageDigest
import javax.crypto.Cipher
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

// Updates of the app (docs/PLAUD.md, "Updates"): what decides them and what opens a build, pure
// so that it is tested on the JVM. The Android side is in Updater.kt.

/** The current build of the app, as Fridolin names it: its SHA-256 and its signer's, hex. */
data class Release(val code: Long, val name: String, val sha256: String, val cert: String, val size: Long)

/** How the app's last update went, as it tells Fridolin (internal/plaud/app_release.go, updateStates). */
data class UpdateReport(val code: Long, val state: String, val error: String? = null, val at: Long = 0) {
    companion object {
        const val WAITING = "waiting"
        const val INSTALLING = "installing"
        const val CONFIRM = "confirm"
        const val NOT_ALLOWED = "not_allowed"
        const val INSTALLED = "installed"
        const val FAILED = "failed"
    }
}

/**
 * Where updates stand, by versionCode (0: none): the build downloaded and checked ([ready]), one
 * that failed its checks and is never fetched again ([bad]), one Android waits to have confirmed
 * on the phone ([confirm]), and the last one handed to Android ([installing]) or refused by it
 * ([failed]), with when.
 */
data class UpdateState(
    val ready: Long = 0,
    val bad: Long = 0,
    val confirm: Long = 0,
    val installing: Long = 0,
    val installingAt: Long = 0,
    val failed: Long = 0,
    val failedAt: Long = 0,
)

/** When to ask Fridolin for its release, whether to fetch it and whether to install what was fetched. */
object UpdatePolicy {
    /** At most this often at check-ins; opening the app, or a new version of it, asks at once. */
    const val CHECK_EVERY_MS = 10 * 60_000L
    /** A build handed to Android that neither installed nor failed by then is handed over again. */
    const val INSTALLING_MS = 10 * 60_000L
    /** A build Android refused is tried again after this long, or when the app is opened. */
    const val RETRY_MS = 6 * 3600_000L

    fun due(lastCheck: Long, now: Long, forced: Boolean) = forced || now - lastCheck >= CHECK_EVERY_MS || now < lastCheck

    enum class Fetch { NONE, OTHER_KEY, DOWNLOAD }

    /** Whether to fetch [rel], for an app at versionCode [own] signed with [ownCert]. */
    fun fetch(own: Long, ownCert: String?, rel: Release?, st: UpdateState): Fetch = when {
        rel == null || rel.code <= own -> Fetch.NONE
        // Android installs an update only when the same key signed it.
        ownCert != null && rel.cert != ownCert -> Fetch.OTHER_KEY
        rel.code == st.bad || rel.code == st.ready -> Fetch.NONE
        else -> Fetch.DOWNLOAD
    }

    /** Whether to hand the build that is [UpdateState.ready] to Android now. [forced]: the app was opened. */
    fun install(own: Long, st: UpdateState, now: Long, forced: Boolean): Boolean = when {
        st.ready <= own -> false
        st.installing == st.ready && now - st.installingAt in 0 until INSTALLING_MS -> false
        // The tap on the phone it waits for is the owner's; opening the app asks again.
        st.confirm == st.ready && !forced -> false
        st.failed == st.ready && !forced && now - st.failedAt in 0 until RETRY_MS -> false
        else -> true
    }
}

/**
 * Opens a build the bridge sealed for this phone (internal/plaud/app_release.go, sealApp): the
 * ECDH secret of the phone's agreement key and the build's one-off key goes through HKDF-SHA256
 * (no salt, [INFO]) to an AES-256-GCM key. The build comes in pieces of [CHUNK] bytes, each sealed
 * with the nonce prefix and its index, and with the build's SHA-256, its index and whether it is
 * the last as associated data: a piece out of order, from another build, or a build cut off at a
 * piece's end does not open.
 */
object UpdateCrypto {
    const val INFO = "fridolin app update v1"
    const val CHUNK = 64 * 1024
    private const val TAG = 16

    /** What comes before the pieces: the one-off public key (an uncompressed P-256 point) and the nonce prefix. */
    class Header(val epk: ByteArray, val prefix: ByteArray)

    fun header(input: InputStream): Header {
        val b = readUpTo(input, 65 + 8)
        if (b.size < 65 + 8 || b[0] != 4.toByte()) throw IOException("the app Fridolin sent has no header")
        return Header(b.copyOfRange(0, 65), b.copyOfRange(65, 73))
    }

    /**
     * Writes the build to [out] as its pieces open, and returns its SHA-256, hex. Throws when a
     * piece was not sealed for this secret and build ([sha256]), or the stream is cut off.
     */
    fun open(secret: ByteArray, header: Header, sha256: ByteArray, input: InputStream, out: OutputStream, chunk: Int = CHUNK): String {
        val key = SecretKeySpec(VoiceCrypto.hkdf(secret, INFO.toByteArray()), "AES")
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        val md = MessageDigest.getInstance("SHA-256")
        var cur = readUpTo(input, chunk + TAG)
        if (cur.isEmpty()) throw IOException("the app Fridolin sent is empty")
        var i = 0
        while (true) {
            // Only a piece shorter than a whole one, or one nothing follows, is the last.
            val next = if (cur.size == chunk + TAG) readUpTo(input, chunk + TAG) else ByteArray(0)
            val last = next.isEmpty()
            val index = ByteBuffer.allocate(4).putInt(i).array()
            c.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, header.prefix + index))
            c.updateAAD(sha256 + index + byteArrayOf(if (last) 1 else 0))
            val piece = c.doFinal(cur)
            md.update(piece)
            out.write(piece)
            if (last) return md.digest().joinToString("") { "%02x".format(it) }
            cur = next
            i++
        }
    }

    /** Up to [n] bytes, fewer only at the end of [input]. */
    private fun readUpTo(input: InputStream, n: Int): ByteArray {
        val b = ByteArray(n)
        var got = 0
        while (got < n) {
            val r = input.read(b, got, n - got)
            if (r < 0) break
            got += r
        }
        return if (got == n) b else b.copyOf(got)
    }
}
