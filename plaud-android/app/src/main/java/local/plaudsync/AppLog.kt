package local.plaudsync

import android.content.Context
import android.util.Log
import java.io.File
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/** Log kept in a file so background behaviour can be checked after the fact. No recording content here. */
object AppLog {
    private var file: File? = null
    private val time = SimpleDateFormat("MM-dd HH:mm:ss", Locale.US)

    @Volatile
    var onChange: (() -> Unit)? = null

    fun init(ctx: Context) {
        if (file != null) return
        file = File(ctx.filesDir, "log.txt")
        // Crashes on SDK threads still kill the app; keep the cause so Share log shows it
        val crash = Thread.getDefaultUncaughtExceptionHandler()
        Thread.setDefaultUncaughtExceptionHandler { t, e ->
            runCatching { i("crash: ${e.stackTraceToString().lines().take(8).joinToString(" | ")}") }
            crash?.uncaughtException(t, e)
        }
    }

    @Synchronized
    fun i(msg: String) {
        Log.i("PlaudSync", msg)
        file?.let { f ->
            if (f.length() > 512_000) f.writeText(f.readLines().takeLast(2000).joinToString("\n", postfix = "\n"))
            f.appendText("${time.format(Date())} $msg\n")
        }
        onChange?.invoke()
    }

    /** Debug detail for logcat only; it stays out of the file the user shares. */
    fun d(msg: String) {
        Log.d("PlaudSync", msg)
    }

    @Synchronized
    fun tail(n: Int): List<String> = file?.takeIf { it.exists() }?.readLines()?.takeLast(n).orEmpty()
}
