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
import android.text.InputType
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.RadioButton
import android.widget.RadioGroup
import android.widget.ScrollView
import android.widget.Switch
import android.widget.TextView
import java.io.File

/** Status, a few buttons and the log. All the work happens in [SyncService]. */
class MainActivity : Activity() {

    private val blePermissions = arrayOf(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT)
    private lateinit var statusView: TextView
    private lateinit var logView: TextView
    private lateinit var allowUpdates: Button
    private val own by lazy { Updater.own(this) }

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
            if (bleAllowed()) {
                SyncService.start(this, SyncService.ACTION_REFRESH)
                SyncService.checkIn(this, Trigger.SETTINGS) // voice mode checks in more often
            }
            render()
        }
        showVoice(VoicePrefs.enabled(this))

        statusView = TextView(this).apply { textSize = 16f }
        // Android lets the app install Fridolin's builds only once this is allowed (Updater.kt).
        allowUpdates = button("Allow app updates") { startActivity(Updater.allowIntent(this)) }
        logView = TextView(this).apply { typeface = Typeface.MONOSPACE; textSize = 11f; setTextIsSelectable(true) }
        setContentView(ScrollView(this).apply {
            fitsSystemWindows = true
            addView(LinearLayout(this@MainActivity).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(pad, pad, pad, pad)
                addView(statusView)
                if (Fridolin.enabled) {
                    addView(allowUpdates)
                    addView(voice)
                    addView(ask)
                    addView(speakLocked)
                }
                addView(button("Fetch from NotePin now") { if (bleAllowed()) SyncService.checkIn(this@MainActivity, Trigger.SYNC_NOW) })
                addView(syncSetting())
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
        Updater.foreground = true
        AppLog.onChange = { runOnUiThread { render() } }
        if (bleAllowed()) SyncService.checkIn(this, Trigger.OPENED)
        render()
    }

    override fun onPause() {
        Updater.foreground = false
        AppLog.onChange = null
        super.onPause()
    }

    private fun bleAllowed() = blePermissions.all { checkSelfPermission(it) == PackageManager.PERMISSION_GRANTED }

    private fun startServiceIfAllowed() {
        if (bleAllowed()) SyncService.start(this)
    }

    /** Sync: instant / smart / every N minutes, with what each costs the pin (docs/PLAUD.md, "The pin's battery"). */
    private fun syncSetting(): View {
        val modes = listOf(
            SyncMode.SMART to "Smart (default): checks in every ${SyncPolicy.SMART_MINUTES} min, and when you open the app, plug in or unlock the phone",
            SyncMode.INTERVAL to "Every N minutes, and when you open the app",
            SyncMode.INSTANT to "Instant: stays connected, recordings arrive seconds after they stop (uses more pin battery)",
        )
        val minutes = EditText(this).apply {
            inputType = InputType.TYPE_CLASS_NUMBER
            hint = "Minutes (${SyncPolicy.MIN_MINUTES}–${SyncPolicy.MAX_MINUTES})"
            setText(SyncPrefs.minutes(this@MainActivity).toString())
            setOnFocusChangeListener { _, focused -> if (!focused) saveMinutes(this) }
            setOnEditorActionListener { v, _, _ -> saveMinutes(v as EditText); false }
        }
        val group = RadioGroup(this)
        modes.forEachIndexed { i, (mode, label) ->
            group.addView(RadioButton(this).apply { id = 100 + i; text = label; isChecked = SyncPrefs.mode(this@MainActivity) == mode })
        }
        fun showMinutes() {
            minutes.visibility = if (SyncPrefs.mode(this) == SyncMode.INTERVAL) View.VISIBLE else View.GONE
        }
        group.setOnCheckedChangeListener { _, id ->
            val mode = modes[id - 100].first
            if (mode == SyncPrefs.mode(this)) return@setOnCheckedChangeListener
            SyncPrefs.setMode(this, mode)
            showMinutes()
            if (bleAllowed()) SyncService.checkIn(this, Trigger.SETTINGS)
        }
        showMinutes()
        val note = TextView(this).apply {
            textSize = 12f
            text = "A connected NotePin cannot sleep, so instant runs its battery down fast. " +
                "Smart and every N minutes connect only to fetch, then let it sleep: a recording can wait on the pin until the " +
                "next check-in. With voice mode on, they check in at least every ${SyncPolicy.VOICE_MINUTES} min; " +
                "Fetch now (here and in the notification) gets a question off the pin at once."
        }
        return LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            addView(TextView(this@MainActivity).apply { text = "Sync with the NotePin"; textSize = 16f })
            addView(group)
            addView(minutes)
            addView(note)
        }
    }

    private fun saveMinutes(field: EditText) {
        val n = field.text.toString().toIntOrNull() ?: return field.setText(SyncPrefs.minutes(this).toString())
        val before = SyncPrefs.minutes(this)
        SyncPrefs.setMinutes(this, n)
        field.setText(SyncPrefs.minutes(this).toString())
        if (SyncPrefs.minutes(this) != before && bleAllowed()) SyncService.checkIn(this, Trigger.SETTINGS)
    }

    private fun render() {
        val unrestricted = getSystemService(PowerManager::class.java).isIgnoringBatteryOptimizations(packageName)
        statusView.text = buildString {
            appendLine(if (bleAllowed()) SyncService.status else "Bluetooth permission missing: allow 'Nearby devices'")
            SyncPrefs.battery(this@MainActivity)?.let { b ->
                val at = java.text.SimpleDateFormat("dd.MM. HH:mm", java.util.Locale.getDefault()).format(java.util.Date(b.at))
                appendLine("NotePin battery: ${b.level}%${if (b.charging == true) ", charging" else ""} (at $at)")
            }
            appendLine("Background: ${if (unrestricted) "allowed" else "NOT allowed, tap the button below"}")
            appendLine("Transcripts: ${if (BuildConfig.API_KEY.isEmpty()) "off (no api_key in build)" else "on"}")
            if (Fridolin.enabled) {
                val waiting = File(filesDir, "outbox").list()?.size ?: 0
                appendLine("Fridolin: ${if (Fridolin.device(this@MainActivity) == null) "not paired yet" else "paired"}" +
                    if (waiting > 0) ", $waiting transcript(s) waiting to be sent" else "")
            }
            if (Fridolin.enabled && VoicePrefs.enabled(this@MainActivity)) appendLine("Voice mode: on")
            appendLine("App: ${own.name} (${own.code})")
            if (Fridolin.enabled) {
                Updater.describe(this@MainActivity)?.let { appendLine(it) }
                if (!Updater.allowed(this@MainActivity)) appendLine("App updates: NOT allowed, tap the button below")
            }
            append("Files: Documents/PlaudSync")
        }
        allowUpdates.visibility = if (Updater.allowed(this)) View.GONE else View.VISIBLE
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
            .setMessage("Unbind the NotePin from this app so it can be added to the Plaud app again? Keep it close by: the app connects to it first.")
            .setPositiveButton("Unpair") { _, _ -> SyncService.start(this, SyncService.ACTION_UNPAIR) }
            .setNegativeButton("Cancel", null)
            .show()
    }
}
