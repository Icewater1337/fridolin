package local.plaudsync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertThrows
import org.junit.Test
import java.math.BigInteger
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPrivateKeySpec
import java.security.spec.ECPublicKeySpec
import java.util.Base64
import javax.crypto.KeyAgreement

/** Opens the answer the bridge's TestVoiceSealVector seals (internal/plaud/voice_test.go). */
class VoiceCryptoTest {
    private val phoneKey = "c9afa9d845ba75166b5c215767b1d6934e50c3db36e89b127b8a622b120f6721"
    private val epkX = "6sbOU2oH2ezKauk0mRkYD0CP6y6WYs4p1lDM1M7w9as"
    private val epkY = "7Xc3SI93jdrsFoTSCAxC4tcIBgH0CS5FiaP0AOrdW9c"
    private val iv = "00112233445566778899aabb"
    private val rid = "q_0123456789abcdef"
    private val ct = "nG2tTagAHfiwF61tyW2ejORNK8zm0wyhGK1M6dz0FP-YiLDZuyobJqDiq6GXqX82iCFxXKupFQUWwEXYVTT1olRb9WM"
    private val text = "Morgen um neun: Zahnarzt. Tomorrow at nine: dentist."

    private fun hex(s: String) = s.chunked(2).map { it.toInt(16).toByte() }.toByteArray()

    /** The ECDH secret as the keystore makes it on the phone, here with a software key. */
    private fun secret(): ByteArray {
        val params = (KeyPairGenerator.getInstance("EC").apply { initialize(ECGenParameterSpec("secp256r1")) }
            .generateKeyPair().public as ECPublicKey).params
        val kf = KeyFactory.getInstance("EC")
        val mine = kf.generatePrivate(ECPrivateKeySpec(BigInteger(1, hex(phoneKey)), params))
        val d = Base64.getUrlDecoder()
        val theirs = kf.generatePublic(ECPublicKeySpec(ECPoint(BigInteger(1, d.decode(epkX)), BigInteger(1, d.decode(epkY))), params))
        return KeyAgreement.getInstance("ECDH").run { init(mine); doPhase(theirs, true); generateSecret() }
    }

    @Test
    fun opensWhatTheBridgeSealed() {
        assertEquals(text, VoiceCrypto.open(secret(), rid, hex(iv), Base64.getUrlDecoder().decode(ct)))
    }

    @Test
    fun anAnswerOpensOnlyAsItsOwnQuestions() {
        assertThrows(Exception::class.java) { VoiceCrypto.open(secret(), "q_another_question", hex(iv), Base64.getUrlDecoder().decode(ct)) }
    }

    @Test
    fun hkdfMatchesRfc5869WithoutSalt() {
        // RFC 5869, test case 3: SHA-256, no salt, no info.
        val okm = VoiceCrypto.hkdf(ByteArray(22) { 0x0b }, ByteArray(0), 42)
        assertEquals("8da4e775a563c18f715f802a063c5a31b8a11f5c5ee1879ec3454e5f3c738d2d9d201395faa4b61a96c8",
            okm.joinToString("") { "%02x".format(it) })
    }
}
