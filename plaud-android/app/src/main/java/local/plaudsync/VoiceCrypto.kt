package local.plaudsync

import javax.crypto.Cipher
import javax.crypto.Mac
import javax.crypto.spec.GCMParameterSpec
import javax.crypto.spec.SecretKeySpec

/**
 * Opens an answer the bridge sealed for this phone (voice mode; internal/plaud/voice.go,
 * sealVoiceReply): the ECDH secret of the phone's key and the answer's one-off key goes through
 * HKDF-SHA256 (no salt, [INFO]) to an AES-256-GCM key; the question's id is the associated data,
 * so an answer opens only as the answer to its own question.
 */
object VoiceCrypto {
    const val INFO = "fridolin voice reply v1"

    /** RFC 5869 HKDF with SHA-256 and no salt (a salt of 32 zero bytes). */
    fun hkdf(secret: ByteArray, info: ByteArray, length: Int = 32): ByteArray {
        val mac = Mac.getInstance("HmacSHA256")
        mac.init(SecretKeySpec(ByteArray(32), "HmacSHA256"))
        val prk = mac.doFinal(secret)
        mac.init(SecretKeySpec(prk, "HmacSHA256"))
        var out = ByteArray(0)
        var t = ByteArray(0)
        var i = 1
        while (out.size < length) {
            mac.update(t)
            mac.update(info)
            mac.update(i.toByte())
            t = mac.doFinal()
            out += t
            i++
        }
        return out.copyOf(length)
    }

    /** The answer's text; throws when it was not sealed for this secret and question. */
    fun open(secret: ByteArray, rid: String, iv: ByteArray, ct: ByteArray): String {
        val c = Cipher.getInstance("AES/GCM/NoPadding")
        c.init(Cipher.DECRYPT_MODE, SecretKeySpec(hkdf(secret, INFO.toByteArray()), "AES"), GCMParameterSpec(128, iv))
        c.updateAAD(rid.toByteArray())
        return String(c.doFinal(ct), Charsets.UTF_8)
    }
}
