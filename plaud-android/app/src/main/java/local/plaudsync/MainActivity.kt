package local.plaudsync

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.content.Intent
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.net.Uri
import android.os.Bundle
import android.os.PowerManager
import android.provider.Settings
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Switch
import android.widget.TextView
import java.io.File

/** Status, a few buttons and the log. All the work happens in [SyncService]. */
class MainActivity : Activity() {

    private val blePermissions = arrayOf(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT)
    private lateinit var statusView: TextView
    private lateinit var logView: TextView

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        AppLog.init(this)
        val pad = (16 * resources.displayMetrics.density).toInt()
        fun button(label: String, onClick: () -> Unit) =
            Button(this).apply { text = label; setOnClickListener { onClick() } }
        fun switch(label: String, on: Boolean, onChange: (Boolean) -> Unit) =
            Switch(this).apply { text = label; isChecked = on; setOnCheckedChangeListener { _, v -> onChange(v) } }

        // Voice mode needs a build from Fridolin's dashboard: it asks through the site.
        val ask = button("Ask Fridolin") { startActivity(Intent(this, AskActivity::class.java)) }
        val speakLocked = switch("Speak answers while the phone is locked", VoicePrefs.speakLocked(this)) {
            VoicePrefs.setSpeakLocked(this, it)
        }
        fun showVoice(on: Boolean) {
            ask.visibility = if (on) android.view.View.VISIBLE else android.view.View.GONE
            speakLocked.visibility = ask.visibility
        }
        val voice = switch("Voice mode: recordings are questions, answers are read aloud", VoicePrefs.enabled(this)) {
            VoicePrefs.setEnabled(this, it)
            AppLog.i(if (it) "voice mode on" else "voice mode off")
            showVoice(it)
            if (bleAllowed()) SyncService.start(this, SyncService.ACTION_REFRESH)
            render()
        }
        showVoice(VoicePrefs.enabled(this))

        statusView = TextView(this).apply { textSize = 16f }
        logView = TextView(this).apply { typeface = Typeface.MONOSPACE; textSize = 11f; setTextIsSelectable(true) }
        setContentView(ScrollView(this).apply {
            fitsSystemWindows = true
            addView(LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(pad, pad, pad, pad)
                addView(statusView)
                if (Fridolin.enabled) {
                    addView(voice)
                    addView(ask)
                    addView(speakLocked)
                }
                addView(button("Pair NotePin") { SyncService.start(this@MainActivity, SyncService.ACTION_PAIR) })
                addView(button("Allow running in background") { askBatteryExemption() })
                addView(button("Share log") { shareLog() })
                addView(button("Unpair (give back to Plaud app)") { confirmUnpair() })
                addView(logView)
            })
        })
        requestPermissions(blePermissions + Manifest.permission.POST_NOTIFICATIONS, 1)
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<out String>, grantResults: IntArray) {
        startServiceIfAllowed()
        render()
    }

    override fun onResume() {
        super.onResume()
        AppLog.onChange = { runOnUiThread { render() } }
        startServiceIfAllowed()
        render()
    }

    override fun onPause() {
        AppLog.onChange = null
        super.onPause()
    }

    private fun bleAllowed() = blePermissions.all { checkSelfPermission(it) == PackageManager.PERMISSION_GRANTED }

    private fun startServiceIfAllowed() {
        if (bleAllowed()) SyncService.start(this)
    }

    private fun render() {
        val unrestricted = getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName)
        statusView.text = buildString {
            appendLine(if (bleAllowed()) SyncService.status else "Bluetooth permission missing: allow 'Nearby devices'")
            appendLine("Background: ${if (unrestricted) "allowed" else "NOT allowed, tap the button below"}")
            appendLine("Transcripts: ${if (BuildConfig.API_KEY.isEmpty()) "off (no api_key in build)" else "on"}")
            if (Fridolin.enabled) {
                val waiting = File(filesDir, "outbox").list()?.size ?: 0
                appendLine("Fridolin: ${if (Fridolin.device(this@MainActivity) == null) "not paired yet" else "paired"}" +
                    if (waiting > 0) ", $waiting transcript(s) waiting to be sent" else "")
            }
            if (Fridolin.enabled && VoicePrefs.enabled(this@MainActivity)) appendLine("Voice mode: on")
            append("Files: Documents/PlaudSync")
        }
        logView.text = AppLog.tail(200).asReversed().joinToString("\n")
    }

    private fun askBatteryExemption() {
        if (getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName)) return
        startActivity(Intent(Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS, Uri.parse("package:$packageName")))
    }

    private fun shareLog() {
        val send = Intent(Intent.ACTION_SEND).setType("text/plain")
            .putExtra(Intent.EXTRA_TEXT, AppLog.tail(500).joinToString("\n"))
        startActivity(Intent.createChooser(send, "Share log"))
    }

    private fun confirmUnpair() {
        AlertDialog.Builder(this)
            .setMessage("Unbind the NotePin from this app so it can be added to the Plaud app again? It must be connected right now.")
            .setPositiveButton("Unpair") { _, _ -> SyncService.start(this, SyncService.ACTION_UNPAIR) }
            .setNegativeButton("Cancel", null)
            .show()
    }
}
