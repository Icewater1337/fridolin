package local.plaudsync

import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Request
import okhttp3.RequestBody
import okhttp3.RequestBody.Companion.toRequestBody
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest
import java.util.Base64
import java.util.concurrent.TimeUnit

/** Plaud developer platform (https://docs.plaud.ai). Everything here blocks: call from Dispatchers.IO. */
object PlaudApi {
    const val DOMAIN = "platform-us.plaud.ai"
    private const val BASE = "https://$DOMAIN/developer/api"

    // The device's pairing lock is derived from this id. Never change it while a device is bound.
    private const val USER_ID = "plaud-sync-owner"

    private val JSON = "application/json".toMediaType()
    private val http = OkHttpClient.Builder()
        .readTimeout(2, TimeUnit.MINUTES)
        .writeTimeout(2, TimeUnit.MINUTES)
        .build()

    /** client credentials → partner token → user token (max lifetime 24 h). */
    fun userToken(): String {
        val basic = Base64.getEncoder()
            .encodeToString("${BuildConfig.CLIENT_ID}:${BuildConfig.CLIENT_SECRET}".toByteArray())
        val partner = call(
            Request.Builder().url("$BASE/oauth/partner/access-token")
                .header("Authorization", "Basic $basic")
                .post("".toRequestBody("application/x-www-form-urlencoded".toMediaType()))
        ).getString("access_token")
        return call(
            Request.Builder().url("$BASE/open/partner/users/access-token")
                .header("Authorization", "Bearer $partner")
                .post(JSONObject().put("user_id", USER_ID).put("expires_in", 86400).body())
        ).getString("access_token")
    }

    /** Cloud half of pairing; [action] is "bind" or "unbind". Idempotent on Plaud's side. */
    fun binding(token: String, action: String, sn: String) {
        call(
            Request.Builder().url("$BASE/open/partner/sdk/$action")
                .header("Authorization", "Bearer $token")
                .post(JSONObject().put("type", deviceType(sn)).put("sn", sn).body())
        )
    }

    fun deviceType(sn: String) = if (sn.startsWith("881")) "notepro" else "notepins"

    /** Upload [mp3] to Plaud storage, transcribe it, return the transcript as plain text. */
    fun transcribe(token: String, mp3: File): String {
        val bytes = mp3.readBytes()
        val auth = "Bearer $token"
        val up = call(
            Request.Builder().url("$BASE/open/partner/files/upload/generate-presigned-urls")
                .header("Authorization", auth)
                .post(JSONObject().put("filesize", bytes.size).put("filetype", "mp3").body())
        )
        val chunk = up.getInt("ChunkSize")
        val parts = up.getJSONArray("Parts")
        val etags = JSONArray()
        for (i in 0 until parts.length()) {
            val part = parts.getJSONObject(i)
            val from = i * chunk
            // Presigned S3 PUT: no auth header and no content type
            val put = Request.Builder().url(part.getString("PresignedUrl"))
                .put(bytes.copyOfRange(from, minOf(from + chunk, bytes.size)).toRequestBody())
                .build()
            val etag = http.newCall(put).execute().use { r ->
                check(r.isSuccessful) { "S3 part ${i + 1}: HTTP ${r.code}" }
                r.header("ETag").orEmpty().replace("\"", "")
            }
            etags.put(JSONObject().put("PartNumber", part.getInt("PartNumber")).put("ETag", etag))
        }
        val md5 = MessageDigest.getInstance("MD5").digest(bytes).joinToString("") { "%02x".format(it) }
        val fileUrl = call(
            Request.Builder().url("$BASE/open/partner/files/upload/complete-upload")
                .header("Authorization", auth)
                .post(
                    JSONObject().put("file_id", up.getString("FileId")).put("upload_id", up.getString("UploadId"))
                        .put("part_list", etags).put("filetype", "mp3").put("file_md5", md5).body()
                )
        ).getString("DownloadUrl")

        val params = JSONObject()
            .put("transcribe", JSONObject().put("language", "auto").put("model", "plaud-fast-whisper"))
            .put("vad", JSONObject().put("decode_silence", false))
            .put("diarization", JSONObject().put("enabled", true))
        val id = call(
            withApiKey("$BASE/open/partner/ai/transcriptions/")
                .post(JSONObject().put("file_url", fileUrl).put("params", params).body())
        ).getString("transcription_id")

        // ponytail: fixed 5 s polling, gives up after 1 h; the mp3 stays pending and is retried later
        repeat(720) {
            Thread.sleep(5_000)
            val task = call(withApiKey("$BASE/open/partner/ai/transcriptions/$id").get())
            when (val status = task.optString("status").uppercase()) {
                "SUCCESS" -> return format(task.getJSONObject("data"))
                "FAILURE", "REVOKED" -> error("transcription $id: $status")
            }
        }
        error("transcription $id timed out")
    }

    /**
     * "[mm:ss - mm:ss] Speaker 1: text" per segment, as Plaud's own transcripts read (Fridolin
     * takes the recording's length from the last end); plain text if there are no segments.
     */
    private fun format(data: JSONObject): String {
        val segs = data.optJSONArray("results") ?: data.optJSONArray("segments")
        if (segs == null || segs.length() == 0) return data.optString("text")
        fun mmss(sec: Double) = sec.toInt().let { "%02d:%02d".format(it / 60, it % 60) }
        return (0 until segs.length()).joinToString("\n") { i ->
            val s = segs.getJSONObject(i)
            val who = s.optString("speaker_id").ifEmpty { s.optString("speaker") }
            val start = s.optDouble("start", 0.0)
            "[%s - %s] %s%s".format(mmss(start), mmss(s.optDouble("end", start)), if (who.isEmpty()) "" else "$who: ", s.optString("text").trim())
        }
    }

    private fun withApiKey(url: String) = Request.Builder().url(url)
        .header("X-Client-Id", BuildConfig.CLIENT_ID)
        .header("X-Client-Api-Key", BuildConfig.API_KEY)

    private fun JSONObject.body(): RequestBody = toString().toRequestBody(JSON)

    /** Throws on non-2xx with method, path and status only; bodies may hold recording data. */
    private fun call(builder: Request.Builder): JSONObject {
        val req = builder.build()
        return http.newCall(req).execute().use { r ->
            check(r.isSuccessful) { "${req.method} ${req.url.encodedPath}: HTTP ${r.code}" }
            // org.json's parse error quotes the whole body, which then would reach the log
            runCatching { JSONObject(r.body!!.string()) }
                .getOrElse { error("${req.method} ${req.url.encodedPath}: the answer is not JSON") }
        }
    }
}
