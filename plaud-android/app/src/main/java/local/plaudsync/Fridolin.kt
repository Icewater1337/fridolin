package local.plaudsync

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONObject
import java.math.BigInteger
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.Signature
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.util.Base64
import java.util.concurrent.TimeUnit

/**
 * Uploads transcripts to Fridolin through its site (docs/PLAUD.md, "Through your site"), when
 * the app was built on Fridolin's dashboard. The app pairs once, with the code it was built
 * with, using a key pair made in the Android keystore: the private key never leaves it. Each
 * upload is a JWT (ES256) signed with that key, naming the recording and the transcript's
 * SHA-256. Everything here blocks: call from Dispatchers.IO.
 */
object Fridolin {
    private const val ALIAS = "fridolin-device"
    private val b64 = Base64.getUrlEncoder().withoutPadding()
    private val http = OkHttpClient.Builder().callTimeout(60, TimeUnit.SECONDS).build()

    /** An answer other than 2xx from the site, with its reason. */
    class Refused(val code: Int, message: String) : Exception(message)

    val enabled get() = BuildConfig.SITE_URL.isNotEmpty()

    /** The id Fridolin gave this phone, or null before pairing. */
    fun device(ctx: Context): String? = prefs(ctx).getString("device", null)

    /** Uploads one transcript, pairing first if need be. Throws when it did not arrive. */
    fun upload(ctx: Context, name: String, text: ByteArray) {
        val device = device(ctx) ?: pair(ctx)
        val now = System.currentTimeMillis() / 1000
        val sha256 = b64.encodeToString(MessageDigest.getInstance("SHA-256").digest(text))
        val jwt = sign(
            JSONObject().put("iss", device).put("aud", "fridolin:plaud-transcript")
                .put("iat", now).put("exp", now + 300).put("name", name).put("sha256", sha256)
        )
        try {
            post("/_fridolin/devices/plaud/transcripts", text.toRequestBody("text/plain; charset=utf-8".toMediaType()), jwt)
        } catch (e: Refused) {
            // Unpaired on the dashboard: the next try pairs again, which works with a new build.
            if (e.code == 401) prefs(ctx).edit().remove("device").apply()
            throw e
        }
    }

    private fun pair(ctx: Context): String {
        val now = System.currentTimeMillis() / 1000
        val jwt = sign(
            JSONObject().put("aud", "fridolin:pair").put("iat", now).put("exp", now + 300)
                .put("code", BuildConfig.PAIRING_CODE).put("key", jwk())
        )
        val id = post("/_fridolin/devices/pair", JSONObject().put("assertion", jwt).toString().toRequestBody("application/json".toMediaType()), null)
            .getString("device")
        prefs(ctx).edit().putString("device", id).apply()
        AppLog.i("paired with Fridolin")
        return id
    }

    /** The site's errors are Fridolin's own words; any other body (a proxy's page) is left out. */
    private fun post(path: String, body: RequestBody, bearer: String?): JSONObject {
        val req = Request.Builder().url(BuildConfig.SITE_URL.trimEnd('/') + path).post(body)
            .apply { if (bearer != null) header("Authorization", "Bearer $bearer") }.build()
        http.newCall(req).execute().use { r ->
            val json = runCatching { JSONObject(r.body!!.string()) }.getOrNull()
            if (!r.isSuccessful) throw Refused(r.code, "HTTP ${r.code}" + (json?.optString("error")?.takeIf { it.isNotEmpty() }?.let { ": $it" } ?: ""))
            return json ?: JSONObject()
        }
    }

    private fun prefs(ctx: Context) = ctx.getSharedPreferences("fridolin", Context.MODE_PRIVATE)

    private fun keys(): Pair<PrivateKey, ECPublicKey> {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        if (!ks.containsAlias(ALIAS)) {
            KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore").apply {
                initialize(
                    KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_SIGN)
                        .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                        .setDigests(KeyProperties.DIGEST_SHA256)
                        .build()
                )
            }.generateKeyPair()
        }
        return (ks.getKey(ALIAS, null) as PrivateKey) to (ks.getCertificate(ALIAS).publicKey as ECPublicKey)
    }

    private fun jwk(): JSONObject {
        val w = keys().second.w
        return JSONObject().put("kty", "EC").put("crv", "P-256")
            .put("x", b64.encodeToString(bytes32(w.affineX))).put("y", b64.encodeToString(bytes32(w.affineY)))
    }

    private fun sign(claims: JSONObject): String {
        val input = b64.encodeToString("""{"alg":"ES256","typ":"JWT"}""".toByteArray()) + "." +
            b64.encodeToString(claims.toString().toByteArray())
        val der = Signature.getInstance("SHA256withECDSA").run {
            initSign(keys().first)
            update(input.toByteArray())
            sign()
        }
        return input + "." + b64.encodeToString(joseSignature(der))
    }

    /** ECDSA's DER signature, SEQUENCE { INTEGER r, INTEGER s }, as JWS wants it: r then s, 32 bytes each. */
    internal fun joseSignature(der: ByteArray): ByteArray {
        var i = 2 + if (der[1].toInt() and 0x80 != 0) der[1].toInt() and 0x7f else 0
        fun int(): BigInteger {
            check(der[i].toInt() == 2) { "not an ECDSA signature" }
            val len = der[i + 1].toInt()
            return BigInteger(1, der.copyOfRange(i + 2, i + 2 + len)).also { i += 2 + len }
        }
        return bytes32(int()) + bytes32(int())
    }

    /** A non-negative number as 32 bytes, big-endian. */
    private fun bytes32(n: BigInteger): ByteArray {
        val b = n.toByteArray() // may have a leading 0, or be shorter
        return ByteArray(32).also { b.copyInto(it, maxOf(0, 32 - b.size), maxOf(0, b.size - 32)) }
    }
}
