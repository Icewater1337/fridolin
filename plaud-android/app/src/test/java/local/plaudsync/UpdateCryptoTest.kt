package local.plaudsync

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import java.io.ByteArrayInputStream
import java.io.ByteArrayOutputStream
import java.math.BigInteger
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.MessageDigest
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPrivateKeySpec
import java.security.spec.ECPublicKeySpec
import javax.crypto.KeyAgreement

/** Opens the build the bridge's TestSealAppVector seals (internal/plaud/app_release_test.go), in pieces of 16 bytes. */
class UpdateCryptoTest {
    private val phoneKey = "c9afa9d845ba75166b5c215767b1d6934e50c3db36e89b127b8a622b120f6721"
    private val text = "Plaud Sync 0.7 (8), an APK in pieces."
    private val sealed = "04eac6ce536a07d9ecca6ae9349919180f408feb2e9662ce29d650ccd4cef0f5abed7737488f778ddaec1684d2080c42e2d7080601f4092e4589a3f400eadd5bd7" +
        "0011223344556677" +
        "323fdb34acf0778a8cbb57d1dc717bad2ca68c066f46bdd8203866d38d3d2834ab1e8141b57f5408db4173fcfe09712057d05c2bd627deaa0bcb2e41fbb32f556d13e4e1a08eee7eb493d9d2af74f0cd59b239b7aa"

    private fun hex(s: String) = s.chunked(2).map { it.toInt(16).toByte() }.toByteArray()
    private val sha256 = MessageDigest.getInstance("SHA-256").digest(text.toByteArray())

    /** The ECDH secret as the keystore makes it on the phone, here with a software key. */
    private fun secret(epk: ByteArray): ByteArray {
        val params = (KeyPairGenerator.getInstance("EC").apply { initialize(ECGenParameterSpec("secp256r1")) }
            .generateKeyPair().public as ECPublicKey).params
        val kf = KeyFactory.getInstance("EC")
        val mine = kf.generatePrivate(ECPrivateKeySpec(BigInteger(1, hex(phoneKey)), params))
        val point = ECPoint(BigInteger(1, epk.copyOfRange(1, 33)), BigInteger(1, epk.copyOfRange(33, 65)))
        val theirs = kf.generatePublic(ECPublicKeySpec(point, params))
        return KeyAgreement.getInstance("ECDH").run { init(mine); doPhase(theirs, true); generateSecret() }
    }

    private fun open(bytes: ByteArray, sum: ByteArray = sha256): Pair<String, ByteArray> {
        val input = ByteArrayInputStream(bytes)
        val header = UpdateCrypto.header(input)
        val out = ByteArrayOutputStream()
        return UpdateCrypto.open(secret(header.epk), header, sum, input, out, chunk = 16) to out.toByteArray()
    }

    @Test
    fun opensWhatTheBridgeSealed() {
        val (sum, out) = open(hex(sealed))
        assertEquals(text, String(out))
        assertEquals(sha256.joinToString("") { "%02x".format(it) }, sum)
    }

    @Test
    fun aBuildCutOffAtAPiecesEndDoesNotOpen() {
        // The header, then the first two pieces of 16 bytes and their tags: the second was not sealed as the last.
        assertThrows(Exception::class.java) { open(hex(sealed).copyOf(73 + 2 * 32)) }
    }

    @Test
    fun aBuildOpensOnlyAsTheBuildItNames() {
        assertThrows(Exception::class.java) { open(hex(sealed), MessageDigest.getInstance("SHA-256").digest("another build".toByteArray())) }
    }

    @Test
    fun aChangedPieceDoesNotOpen() {
        val b = hex(sealed)
        b[73 + 40] = (b[73 + 40].toInt() xor 1).toByte()
        assertThrows(Exception::class.java) { open(b) }
    }

    @Test
    fun noHeaderNoBuild() {
        assertThrows(Exception::class.java) { UpdateCrypto.header(ByteArrayInputStream(ByteArray(10))) }
        assertArrayEquals(hex("0011223344556677"), UpdateCrypto.header(ByteArrayInputStream(hex(sealed))).prefix)
    }
}
