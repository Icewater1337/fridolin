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
import java.security.KeyFactory
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.MessageDigest
import java.security.PrivateKey
import java.security.Signature
import java.security.SecureRandom
import java.security.interfaces.ECPublicKey
import java.security.spec.ECGenParameterSpec
import java.security.spec.ECPoint
import java.security.spec.ECPublicKeySpec
import java.util.Base64
import java.util.concurrent.TimeUnit
import javax.crypto.KeyAgreement

/**
 * Uploads transcripts to Fridolin through its site (docs/PLAUD.md, "Through your site"), when
 * the app was built on Fridolin's dashboard. The app pairs once, with the code it was built
 * with, using a key pair made in the Android keystore: the private key never leaves it. Each
 * upload is a JWT (ES256) signed with that key, naming the recording and the transcript's
 * SHA-256. Everything here blocks: call from Dispatchers.IO.
 *
 * Voice mode asks Fridolin a question the same way ([ask]) and fetches the answer ([reply]),
 * which comes back encrypted to a second keystore key, one for key agreement only ([VoiceCrypto]).
 *
 * Updates (docs/PLAUD.md, "Updates"): the app asks for Fridolin's current build ([release]),
 * saying how it is itself, and fetches a newer one ([fetchApp]), encrypted to that same key, since
 * the build carries Plaud's developer keys and passes through the site ([UpdateCrypto]).
 */
object Fridolin {
    private const val ALIAS = "fridolin-device"
    private const val VOICE_ALIAS = "fridolin-voice"
    private val b64 = Base64.getUrlEncoder().withoutPadding()
    private val http = OkHttpClient.Builder().callTimeout(60, TimeUnit.SECONDS).build()
    // A build is megabytes: it may take minutes on a slow network, as long as bytes keep coming.
    private val download = http.newBuilder().callTimeout(15, TimeUnit.MINUTES).readTimeout(60, TimeUnit.SECONDS).build()

    /** An answer other than 2xx from the site, with its reason. */
    class Refused(val code: Int, message: String) : Exception(message)

    val enabled get() = BuildConfig.SITE_URL.isNotEmpty()

    /** The id Fridolin gave this phone, or null before pairing. */
    fun device(ctx: Context): String? = prefs(ctx).getString("device", null)

    /** A new id for a voice question. */
    fun newQuestionId(): String = b64.encodeToString(ByteArray(18).also { SecureRandom().nextBytes(it) })

    /** Where a voice question stands: "waiting", "done" (text is the answer, null when it had none), "failed" or "unknown". */
    data class Reply(val state: String, val text: String? = null, val error: String? = null)

    /** Sends a voice question, pairing first if need be; its answer comes with [reply]. Throws when it did not arrive. */
    fun ask(ctx: Context, rid: String, question: ByteArray): String {
        val device = device(ctx) ?: pair(ctx)
        val now = System.currentTimeMillis() / 1000
        val sha256 = b64.encodeToString(MessageDigest.getInstance("SHA-256").digest(question))
        val jwt = sign(
            JSONObject().put("iss", device).put("aud", "fridolin:voice").put("iat", now).put("exp", now + 300)
                .put("rid", rid).put("sha256", sha256).put("enc", voiceJwk())
        )
        return unpairedOn401(ctx) {
            post("/_fridolin/devices/voice/ask", question.toRequestBody("text/plain; charset=utf-8".toMediaType()), jwt).optString("state")
        }
    }

    /** The answer to question rid once there; the site holds the request up to 15 seconds while it is not. */
    fun reply(ctx: Context, rid: String): Reply {
        val device = device(ctx) ?: throw IllegalStateException("not paired")
        val now = System.currentTimeMillis() / 1000
        val jwt = sign(JSONObject().put("iss", device).put("aud", "fridolin:voice-reply").put("iat", now).put("exp", now + 300).put("rid", rid))
        val r = unpairedOn401(ctx) { post("/_fridolin/devices/voice/reply", ByteArray(0).toRequestBody(null), jwt) }
        val state = r.optString("state")
        val text = r.optJSONObject("reply")?.let { open(rid, it) }
        return Reply(state, text, r.optString("error").takeIf { it.isNotEmpty() })
    }

    private fun <T> unpairedOn401(ctx: Context, call: () -> T): T = try {
        call()
    } catch (e: Refused) {
        // Unpaired on the dashboard: the next try pairs again, which works with a new build.
        if (e.code == 401) prefs(ctx).edit().remove("device").apply()
        throw e
    }

    /**
     * Fridolin's current build of the app, or null while it has none, pairing first if need be.
     * The request says which version this app is, which key signs it and how its last update
     * went ([report]), which Fridolin's dashboard shows.
     */
    fun release(ctx: Context, own: Updater.Own, report: UpdateReport?): Release? {
        val device = device(ctx) ?: pair(ctx)
        val now = System.currentTimeMillis() / 1000
        val claims = JSONObject().put("iss", device).put("aud", "fridolin:app-release").put("iat", now).put("exp", now + 300)
            .put("ver", own.code).put("vname", own.name)
        own.cert?.let { claims.put("cert", it) }
        report?.let { r ->
            claims.put("upd", JSONObject().put("code", r.code).put("state", r.state).put("at", r.at / 1000).apply { r.error?.let { put("error", it) } })
        }
        val r = unpairedOn401(ctx) { post("/_fridolin/devices/app/release", ByteArray(0).toRequestBody(null), sign(claims)) }
        val rel = r.optJSONObject("release") ?: return null
        return Release(rel.getLong("version_code"), rel.optString("version_name"), rel.getString("sha256"), rel.getString("cert_sha256"), rel.optLong("size"))
    }

    /**
     * Fetches the build whose SHA-256 is [sha256] into [out], opening it as it arrives with the
     * agreement key, which never leaves the keystore; returns the SHA-256 of what it wrote, hex.
     */
    fun fetchApp(ctx: Context, sha256: String, out: java.io.OutputStream): String {
        val device = device(ctx) ?: throw IllegalStateException("not paired")
        val now = System.currentTimeMillis() / 1000
        val jwt = sign(JSONObject().put("iss", device).put("aud", "fridolin:app-apk").put("iat", now).put("exp", now + 300)
            .put("apk", sha256).put("enc", voiceJwk()))
        val req = Request.Builder().url(BuildConfig.SITE_URL.trimEnd('/') + "/_fridolin/devices/app/apk")
            .post(ByteArray(0).toRequestBody(null)).header("Authorization", "Bearer $jwt").build()
        return unpairedOn401(ctx) {
            download.newCall(req).execute().use { r ->
                if (!r.isSuccessful) {
                    val json = runCatching { JSONObject(r.body!!.string()) }.getOrNull()
                    throw Refused(r.code, "HTTP ${r.code}" + (json?.optString("error")?.takeIf { it.isNotEmpty() }?.let { ": $it" } ?: ""))
                }
                val input = r.body!!.byteStream()
                val header = UpdateCrypto.header(input)
                UpdateCrypto.open(agree(header.epk), header, sha256.chunked(2).map { it.toInt(16).toByte() }.toByteArray(), input, out)
            }
        }
    }

    /** The ECDH secret of the agreement key and [epk], an uncompressed P-256 point. */
    private fun agree(epk: ByteArray): ByteArray {
        val (private, public) = voiceKeys()
        val point = ECPoint(BigInteger(1, epk.copyOfRange(1, 33)), BigInteger(1, epk.copyOfRange(33, 65)))
        val theirs = KeyFactory.getInstance("EC").generatePublic(ECPublicKeySpec(point, public.params))
        return KeyAgreement.getInstance("ECDH", "AndroidKeyStore").run {
            init(private)
            doPhase(theirs, true)
            generateSecret()
        }
    }

    /** Opens a sealed answer with the voice key, which never leaves the keystore. */
    private fun open(rid: String, sealed: JSONObject): String {
        val d = Base64.getUrlDecoder()
        val epk = sealed.getJSONObject("epk")
        val secret = agree(byteArrayOf(4) + d.decode(epk.getString("x")) + d.decode(epk.getString("y")))
        return VoiceCrypto.open(secret, rid, d.decode(sealed.getString("iv")), d.decode(sealed.getString("ct")))
    }

    private fun voiceKeys(): Pair<PrivateKey, ECPublicKey> {
        val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        if (!ks.containsAlias(VOICE_ALIAS)) {
            KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore").apply {
                initialize(
                    KeyGenParameterSpec.Builder(VOICE_ALIAS, KeyProperties.PURPOSE_AGREE_KEY)
                        .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                        .build()
                )
            }.generateKeyPair()
        }
        return (ks.getKey(VOICE_ALIAS, null) as PrivateKey) to (ks.getCertificate(VOICE_ALIAS).publicKey as ECPublicKey)
    }

    private fun voiceJwk(): JSONObject = jwkOf(voiceKeys().second)

    /** Uploads one transcript, pairing first if need be. Throws when it did not arrive. */
    fun upload(ctx: Context, name: String, text: ByteArray) {
        val device = device(ctx) ?: pair(ctx)
        val now = System.currentTimeMillis() / 1000
        val sha256 = b64.encodeToString(MessageDigest.getInstance("SHA-256").digest(text))
        val jwt = sign(
            JSONObject().put("iss", device).put("aud", "fridolin:plaud-transcript")
                .put("iat", now).put("exp", now + 300).put("name", name).put("sha256", sha256)
        )
        unpairedOn401(ctx) { post("/_fridolin/devices/plaud/transcripts", text.toRequestBody("text/plain; charset=utf-8".toMediaType()), jwt) }
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

    private fun jwk(): JSONObject = jwkOf(keys().second)

    private fun jwkOf(key: ECPublicKey): JSONObject {
        val w = key.w
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
