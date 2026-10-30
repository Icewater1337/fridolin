package local.plaudsync

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInfo
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.net.Uri
import android.provider.Settings
import org.json.JSONObject
import java.io.File
import java.security.MessageDigest

/**
 * The app updates itself when Fridolin has a newer build (docs/PLAUD.md, "Updates"). At check-ins
 * [SyncService] asks for Fridolin's release ([check]), saying how this app is and how its last
 * update went. A newer build signed with this app's own key is fetched, encrypted for this phone,
 * and checked: its SHA-256, and that Android reads it as this app, at that version, signed with
 * the same key. Once the sync is idle, it goes to Android's PackageInstaller ([install]) without
 * asking where Android lets it: once this app installed itself, from then on. Where Android still
 * asks (the first update, or "Install unknown apps" off for the app), a notification asks the
 * owner. What the app has (pairing, recordings and transcripts waiting, settings, keys) is in its
 * own storage and keystore, which an update keeps.
 */
object Updater {
    const val EXTRA_CODE = "code"
    private const val CHANNEL = "updates"
    private const val NOTE = 2

    /** This app: its versionCode and versionName, and its signer's SHA-256, hex. */
    data class Own(val code: Long, val name: String, val cert: String?)

    /** Set while the app's screen is shown: Android's question then opens at once. */
    @Volatile
    var foreground = false

    private fun prefs(ctx: Context) = ctx.getSharedPreferences("update", Context.MODE_PRIVATE)
    private fun dir(ctx: Context) = File(ctx.noBackupFilesDir, "update").apply { mkdirs() }
    private fun apk(ctx: Context) = File(dir(ctx), "app.apk")

    fun own(ctx: Context): Own {
        @Suppress("DEPRECATION")
        val info = ctx.packageManager.getPackageInfo(ctx.packageName, PackageManager.GET_SIGNING_CERTIFICATES or PackageManager.GET_SIGNATURES)
        return Own(info.longVersionCode, info.versionName ?: "", signer(info))
    }

    /** The SHA-256 of the certificate [info] is signed with, hex. */
    private fun signer(info: PackageInfo): String? {
        @Suppress("DEPRECATION")
        val certs = info.signingInfo?.apkContentsSigners ?: info.signatures
        return certs?.firstOrNull()?.let { hex(MessageDigest.getInstance("SHA-256").digest(it.toByteArray())) }
    }

    private fun hex(b: ByteArray) = b.joinToString("") { "%02x".format(it) }

    fun state(ctx: Context) = prefs(ctx).run {
        UpdateState(getLong("ready", 0), getLong("bad", 0), getLong("confirm", 0),
            getLong("installing", 0), getLong("installing_at", 0), getLong("failed", 0), getLong("failed_at", 0))
    }

    fun report(ctx: Context): UpdateReport? = prefs(ctx).getString("report", null)?.let { s ->
        runCatching {
            val j = JSONObject(s)
            UpdateReport(j.getLong("code"), j.getString("state"), j.optString("error").takeIf { it.isNotEmpty() }, j.optLong("at"))
        }.getOrNull()
    }

    private fun setReport(ctx: Context, r: UpdateReport) {
        val j = JSONObject().put("code", r.code).put("state", r.state).put("at", r.at)
        r.error?.let { j.put("error", it.take(300)) }
        prefs(ctx).edit().putString("report", j.toString()).apply()
        AppLog.i("update to ${r.code}: ${r.state}" + (r.error?.let { ": $it" } ?: ""))
    }

    private fun report(ctx: Context, code: Long, state: String, error: String? = null) =
        setReport(ctx, UpdateReport(code, state, error, System.currentTimeMillis()))

    /** What the app's screen says of updates, or null when there is nothing to say. */
    fun describe(ctx: Context): String? {
        val r = report(ctx) ?: return null
        return "Update to ${r.code}: " + when (r.state) {
            UpdateReport.WAITING -> "downloaded, installs once the sync is idle"
            UpdateReport.INSTALLING -> "installing"
            UpdateReport.CONFIRM -> "tap the notification to install it"
            UpdateReport.NOT_ALLOWED -> "allow app updates (button below)"
            UpdateReport.INSTALLED -> "installed"
            else -> "failed, ${r.error ?: "see log"}"
        }
    }

    /** Whether Android lets this app install its updates ("Install unknown apps"). */
    fun allowed(ctx: Context) = ctx.packageManager.canRequestPackageInstalls()

    /** The settings screen where the owner allows it. */
    fun allowIntent(ctx: Context) = Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:${ctx.packageName}"))

    /**
     * At a check-in: tells Fridolin how this app is, and fetches a newer build it has. [forced]:
     * the app was opened, or Fetch now, which asks at once. Blocks: call from Dispatchers.IO.
     */
    fun check(ctx: Context, forced: Boolean) {
        if (!Fridolin.enabled) return
        val own = own(ctx)
        val p = prefs(ctx)
        val now = System.currentTimeMillis()
        // A new version of the app says so at once.
        if (!UpdatePolicy.due(p.getLong("checked", 0), now, forced || p.getLong("checked_as", 0) != own.code)) return
        p.edit().putLong("checked", now).putLong("checked_as", own.code).apply()
        settle(ctx, own)
        val rel = try {
            Fridolin.release(ctx, own, report(ctx))
        } catch (e: Exception) {
            return AppLog.i("update: asking Fridolin for its build failed: ${e.message}")
        }
        when (UpdatePolicy.fetch(own.code, own.cert, rel, state(ctx))) {
            UpdatePolicy.Fetch.NONE -> {}
            UpdatePolicy.Fetch.OTHER_KEY -> if (report(ctx)?.let { it.code == rel!!.code && it.state == UpdateReport.FAILED } != true) {
                report(ctx, rel!!.code, UpdateReport.FAILED, "the build is signed with another key than this app, so Android would not install it over it")
            }
            UpdatePolicy.Fetch.DOWNLOAD -> fetch(ctx, rel!!, own)
        }
    }

    /** What the last report was about is done once this app is that version or newer. */
    private fun settle(ctx: Context, own: Own) {
        val r = report(ctx) ?: return
        if (r.code > own.code || r.state == UpdateReport.INSTALLED) return
        setReport(ctx, UpdateReport(r.code, UpdateReport.INSTALLED, null, System.currentTimeMillis()))
        dir(ctx).listFiles()?.forEach { it.delete() }
        prefs(ctx).edit().remove("ready").remove("confirm").apply()
        ctx.getSystemService(NotificationManager::class.java).cancel(NOTE)
    }

    /** Fetches [rel] and keeps it as ready once it is what Fridolin named, this app, signed with its key. */
    private fun fetch(ctx: Context, rel: Release, own: Own) {
        dir(ctx).listFiles()?.forEach { it.delete() }
        prefs(ctx).edit().remove("ready").apply()
        val part = File(dir(ctx), "app.part")
        AppLog.i("update: fetching ${rel.name} (${rel.code}), ${rel.size / 1024} KB, from Fridolin")
        val sum = try {
            part.outputStream().use { Fridolin.fetchApp(ctx, rel.sha256, it) }
        } catch (e: Exception) {
            part.delete()
            return report(ctx, rel.code, UpdateReport.FAILED, "fetching it failed: ${e.message}") // tried again at the next check
        }
        @Suppress("DEPRECATION")
        val info = ctx.packageManager.getPackageArchiveInfo(part.path, PackageManager.GET_SIGNING_CERTIFICATES or PackageManager.GET_SIGNATURES)
        val problem = when {
            sum != rel.sha256 -> "its SHA-256 is not the one Fridolin named"
            info == null -> "Android cannot read it"
            info.packageName != ctx.packageName -> "it is another app (${info.packageName})"
            info.longVersionCode != rel.code -> "it is version ${info.longVersionCode}, not ${rel.code}"
            own.cert == null || signer(info) != own.cert -> "it is signed with another key than this app"
            else -> null
        }
        if (problem != null) {
            part.delete()
            prefs(ctx).edit().putLong("bad", rel.code).apply()
            return report(ctx, rel.code, UpdateReport.FAILED, "the build fetched is not installed: $problem")
        }
        if (!part.renameTo(apk(ctx))) return report(ctx, rel.code, UpdateReport.FAILED, "could not keep the build fetched")
        prefs(ctx).edit().putLong("ready", rel.code).apply()
        report(ctx, rel.code, UpdateReport.WAITING)
    }

    /** Whether a build that was fetched waits to be installed now ([UpdatePolicy.install]). */
    fun due(ctx: Context, forced: Boolean) =
        apk(ctx).exists() && UpdatePolicy.install(own(ctx).code, state(ctx), System.currentTimeMillis(), forced)

    /**
     * Hands the build that was fetched to Android, asking not to ask the owner. Call it while the
     * sync is idle: an update ends the app's process, and the new version starts the sync again.
     */
    fun install(ctx: Context) {
        val code = state(ctx).ready
        val apk = apk(ctx)
        if (!allowed(ctx)) {
            // Once per build; the app's screen says so meanwhile.
            if (report(ctx)?.let { it.code == code && it.state == UpdateReport.NOT_ALLOWED } == true) return
            report(ctx, code, UpdateReport.NOT_ALLOWED)
            return notify(ctx, "Allow Plaud Sync to install its updates",
                "Fridolin has a new version. Tap, then allow this app to install unknown apps.", allowIntent(ctx))
        }
        try {
            val installer = ctx.packageManager.packageInstaller
            val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
                setAppPackageName(ctx.packageName)
                setSize(apk.length())
                setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
            }
            val id = installer.createSession(params)
            installer.openSession(id).use { s ->
                s.openWrite("app.apk", 0, apk.length()).use { out ->
                    apk.inputStream().use { it.copyTo(out) }
                    s.fsync(out)
                }
                prefs(ctx).edit().putLong("installing", code).putLong("installing_at", System.currentTimeMillis()).apply()
                report(ctx, code, UpdateReport.INSTALLING)
                // Android fills in the status, so the intent must be mutable; it is explicit.
                val done = PendingIntent.getBroadcast(ctx, id, Intent(ctx, UpdateReceiver::class.java).putExtra(EXTRA_CODE, code),
                    PendingIntent.FLAG_MUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
                s.commit(done.intentSender)
            }
        } catch (e: Exception) {
            failed(ctx, code, "handing it to Android failed: ${e.message}")
        }
    }

    /** Android asks the owner to confirm: on the screen when it is open, else in a notification. */
    fun confirm(ctx: Context, code: Long, ask: Intent) {
        prefs(ctx).edit().putLong("confirm", code).apply()
        report(ctx, code, UpdateReport.CONFIRM)
        ask.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (foreground && runCatching { ctx.startActivity(ask) }.isSuccess) return
        notify(ctx, "Plaud Sync update ready", "Tap to install the new version from Fridolin.", ask)
    }

    fun failed(ctx: Context, code: Long, why: String) {
        prefs(ctx).edit().putLong("failed", code).putLong("failed_at", System.currentTimeMillis()).remove("installing").apply()
        report(ctx, code, UpdateReport.FAILED, why)
    }

    /** Installed: in practice the new version settles it at its first check, since the update ends this process. */
    fun installed(ctx: Context, code: Long) {
        report(ctx, code, UpdateReport.INSTALLED)
        ctx.getSystemService(NotificationManager::class.java).cancel(NOTE)
    }

    private fun notify(ctx: Context, title: String, text: String, intent: Intent) {
        val nm = ctx.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "App updates", NotificationManager.IMPORTANCE_DEFAULT))
        val tap = PendingIntent.getActivity(ctx, NOTE, intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        nm.notify(NOTE, Notification.Builder(ctx, CHANNEL)
            .setSmallIcon(android.R.drawable.stat_sys_download_done)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(tap)
            .setAutoCancel(true)
            .setOnlyAlertOnce(true)
            .build())
    }
}
