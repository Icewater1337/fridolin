package local.plaudsync

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.BroadcastReceiver
import android.content.ContentValues
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.ServiceInfo
import android.os.Environment
import android.os.IBinder
import android.os.PowerManager
import android.provider.MediaStore
import com.tinnotech.penblesdk.entity.BleDevice
import com.tinnotech.penblesdk.entity.BleFile
import kotlinx.coroutines.CoroutineExceptionHandler
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.cancel
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.withContext
import kotlinx.coroutines.withTimeoutOrNull
import sdk.NiceBuildSdk
import sdk.PlaudDeviceAgent
import sdk.PlaudDeviceAgentListener
import sdk.audio.AudioExportFormat
import sdk.audio.AudioExporter
import java.io.File
import java.io.OutputStream
import java.text.SimpleDateFormat
import java.util.Date
import java.util.Locale

/**
 * Foreground service that keeps the BLE link to the NotePin open and pulls every recording
 * off the device as soon as it stops: device → MP3 in Documents/PlaudSync → delete on device
 * → Plaud transcription → .txt next to the MP3.
 *
 * In voice mode (docs/PLAUD.md, "Voice mode") a recording is a question for Fridolin instead,
 * as is what is said to the phone's speech recognizer ([AskActivity]); the answer is read aloud
 * ([Speaker]).
 */
class SyncService : Service() {

    companion object {
        const val ACTION_PAIR = "pair"
        const val ACTION_UNPAIR = "unpair"
        const val ACTION_ASK = "ask"
        const val ACTION_STOP_SPEECH = "stop_speech"
        const val ACTION_SKIP = "skip"
        const val ACTION_REFRESH = "refresh"
        const val EXTRA_TEXT = "text"

        /** How long the app waits for the answer to a voice question. */
        private const val ANSWER_WAIT_MS = 30 * 60_000L

        @Volatile
        var status = "Not running"
            private set

        fun start(ctx: Context, action: String? = null) {
            ctx.startForegroundService(Intent(ctx, SyncService::class.java).setAction(action))
        }

        /** Asks Fridolin text by voice mode. */
        fun ask(ctx: Context, text: String) {
            ctx.startForegroundService(Intent(ctx, SyncService::class.java).setAction(ACTION_ASK).putExtra(EXTRA_TEXT, text))
        }
    }

    // Every SDK callback hops onto the main thread, so the state below needs no locking.
    // An SDK throw inside a coroutine is logged instead of killing the app.
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main + CoroutineExceptionHandler { _, e ->
        AppLog.i("error: $e")
        setStatus("Error, see log")
    })
    private val prefs by lazy { getSharedPreferences("plaudsync", MODE_PRIVATE) }
    private var savedSn: String?
        get() = prefs.getString("sn", null)
        set(v) = prefs.edit().putString("sn", v).apply()

    private var token = ""
    private var sdkReady = false
    private var pairing = false
    private var connecting = false
    private var connected = false
    private var exporting: Long? = null
    private var recordingSid: Long? = null
    private var listAgain = false
    private val queue = ArrayDeque<Long>()
    private var rescanJob: Job? = null
    private val transcribing = Mutex()
    private val uploading = Mutex()
    private val pendingDir by lazy { File(filesDir, "pending").apply { mkdirs() } }
    // Transcripts waiting to reach Fridolin (Fridolin.kt), when the app was built for it
    private val outboxDir by lazy { File(filesDir, "outbox").apply { mkdirs() } }
    private val exportDir by lazy { File(cacheDir, "export").apply { mkdirs() } }
    // Voice questions: <id>.ask waits to be sent, <id>.wait for its answer
    private val voiceDir by lazy { File(filesDir, "voice").apply { mkdirs() } }
    private val asking = Mutex()
    private val awaiting = mutableSetOf<String>()
    private lateinit var speaker: Speaker
    private val unlocked = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            speaker.resume()
            refreshNotification()
        }
    }
    private val wakeLock by lazy {
        getSystemService(PowerManager::class.java)
            .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "plaudsync:export")
            .apply { setReferenceCounted(false) }
    }

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onCreate() {
        super.onCreate()
        AppLog.init(this)
        getSystemService(NotificationManager::class.java).createNotificationChannel(
            NotificationChannel("sync", "Plaud sync", NotificationManager.IMPORTANCE_LOW)
        )
        startForeground(1, notification("Starting"), ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE)
        speaker = Speaker(this) { refreshNotification() }
        registerReceiver(unlocked, IntentFilter(Intent.ACTION_USER_PRESENT))
        AppLog.i("service started")
        scope.launch { run() }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_PAIR -> {
                pairing = true
                AppLog.i("pairing: looking for a NotePin S / Note Pro")
                scan()
            }
            ACTION_UNPAIR -> unpair()
            ACTION_ASK -> intent.getStringExtra(EXTRA_TEXT)?.let { queueQuestion(it) }
            ACTION_STOP_SPEECH -> speaker.stop()
            ACTION_SKIP -> speaker.skip()
            ACTION_REFRESH -> refreshNotification()
        }
        return START_STICKY
    }

    override fun onDestroy() {
        AppLog.i("service stopped")
        scope.cancel()
        unregisterReceiver(unlocked)
        speaker.shutdown()
        if (wakeLock.isHeld) wakeLock.release()
        super.onDestroy()
    }

    private suspend fun run() {
        token = renewToken()
        // SDK quirk (see Plaud's template app): its partner API defaults to the JP host and
        // ignores customDomain, so the RSA key fetch fails unless we point it at our region.
        NiceBuildSdk.getPartnerApiManager().updateBaseUrl("https://${PlaudApi.DOMAIN}")
        PlaudDeviceAgent.listener = listener
        PlaudDeviceAgent.initSDK(applicationContext, token, PlaudApi.DOMAIN)
        PlaudDeviceAgent.setBleLogLevel(android.util.Log.INFO)
        sdkReady = true
        scan()
        transcribePending()
        resumeQuestions()
        scope.launch {
            while (true) {
                uploadPending()
                delay(15 * 60_000L) // what failed (offline, say) is tried again
            }
        }
        while (true) {
            delay(12 * 3600_000L) // user tokens live 24 h
            token = renewToken()
            NiceBuildSdk.setPartnerToken(token)
            transcribePending()
        }
    }

    private suspend fun renewToken(): String {
        var wait = 10_000L
        while (true) {
            try {
                return withContext(Dispatchers.IO) { PlaudApi.userToken() }.also { AppLog.i("token renewed") }
            } catch (e: Exception) {
                AppLog.i("token failed (${e.message}), retrying in ${wait / 1000}s")
            }
            delay(wait)
            wait = minOf(wait * 2, 600_000L)
        }
    }

    // --- connection -------------------------------------------------------------------------

    private fun scan() {
        if (!sdkReady || connected || connecting) return
        if (savedSn == null && !pairing) return setStatus("Not paired: open the app and tap Pair")
        setStatus(if (pairing) "Pairing: searching" else "Searching for NotePin")
        try {
            PlaudDeviceAgent.startScan()
        } catch (e: Exception) {
            AppLog.i("scan failed: ${e.message}")
            rescanLater(60_000)
        }
    }

    private fun rescanLater(ms: Long) {
        rescanJob?.cancel()
        rescanJob = scope.launch { delay(ms); scan() }
    }

    private fun connect(dev: BleDevice) {
        val sn = dev.getSerialNumber()
        connecting = true
        setStatus("Connecting")
        scope.launch(Dispatchers.IO) {
            try {
                // initSDK fetches the partner RSA keys asynchronously; the handshake needs them
                withTimeoutOrNull(10_000) { while (!NiceBuildSdk.isPartnerDataReady()) delay(200) }
                if (!NiceBuildSdk.signAndStoreDeviceSn(PlaudApi.deviceType(sn), sn)) AppLog.i("SN signing failed")
                PlaudDeviceAgent.connectBleDevice(dev)
            } catch (e: Exception) {
                AppLog.i("connect failed: ${e.message}")
                withContext(Dispatchers.Main) { connecting = false; rescanLater(30_000) }
            }
        }
        scope.launch {
            delay(60_000)
            if (connecting && !connected) {
                AppLog.i("connect timed out")
                PlaudDeviceAgent.disconnect()
                connecting = false
                rescanLater(5_000)
            }
        }
    }

    private fun onConnectState(state: Int) {
        if (state == 1) return // GATT link only; success is bleBind(status 0) after the E2EE handshake
        if (connecting && !connected) {
            AppLog.i("handshake failed (state $state). If this repeats, the NotePin is probably still bound to the Plaud app")
        } else if (connected) {
            AppLog.i("disconnected")
        }
        connecting = false
        connected = false
        exporting = null
        listAgain = false
        queue.clear()
        if (wakeLock.isHeld) wakeLock.release()
        rescanLater(5_000)
    }

    private fun onBind(sn: String?, status: Int) {
        if (status != 0 || sn == null) return AppLog.i("device bind failed (status $status)")
        connecting = false
        connected = true
        pairing = false
        if (savedSn != sn) {
            savedSn = sn
            AppLog.i("paired with ${sn.take(3)}…${sn.takeLast(4)}")
        } else {
            AppLog.i("connected")
        }
        setStatus("Connected")
        val t = token
        scope.launch(Dispatchers.IO) {
            runCatching { PlaudApi.binding(t, "bind", sn) }.onFailure { AppLog.i("cloud bind: ${it.message}") }
        }
        PlaudDeviceAgent.syncTime()
        PlaudDeviceAgent.refreshRecordState() // → bleRecordStateRefreshed → file list once idle
    }

    private fun unpair() {
        val sn = savedSn ?: return AppLog.i("unpair: nothing paired")
        // Without the BLE depair the device stays locked to this app, so require a live link
        if (!connected) return AppLog.i("unpair: NotePin not connected. Bring it close, wait for 'Connected', retry")
        AppLog.i("unpairing")
        savedSn = null
        val t = token
        scope.launch {
            withContext(Dispatchers.IO) {
                runCatching { PlaudApi.binding(t, "unbind", sn) }.onFailure { AppLog.i("cloud unbind: ${it.message}") }
            }
            PlaudDeviceAgent.depair(false)
            delay(3_000)
            PlaudDeviceAgent.disconnect()
            AppLog.i("unpaired; the NotePin can be added in the Plaud app again")
        }
    }

    // --- sync -------------------------------------------------------------------------------

    /** The device can't list files mid-download, so a request during one is replayed afterwards. */
    private fun requestFileList() {
        if (exporting != null) listAgain = true else if (connected) PlaudDeviceAgent.getFileList()
    }

    private fun onFileList(files: List<BleFile>) {
        val onDevice = files.map { it.sessionId }
        val synced = prefs.getStringSet("synced", emptySet())!!.map { it.toLong() }.toSet()
        // Saved earlier but the delete didn't stick: retry the delete instead of downloading again
        onDevice.filter { it in synced }.forEach { PlaudDeviceAgent.deleteFile(it) }
        prefs.edit().putStringSet("synced", synced.filter { it in onDevice }.map { it.toString() }.toSet()).apply()
        val fresh = onDevice
            .filter { it !in synced && it != exporting && it != recordingSid && it !in queue }
            .sorted()
        if (fresh.isNotEmpty()) AppLog.i("${fresh.size} new recording(s) on device")
        queue.addAll(fresh)
        exportNext()
    }

    private fun exportNext() {
        if (exporting != null || !connected) return
        val sid = queue.removeFirstOrNull() ?: run {
            if (listAgain) {
                listAgain = false
                return PlaudDeviceAgent.getFileList()
            }
            if (wakeLock.isHeld) wakeLock.release()
            return setStatus("Connected, everything synced")
        }
        exporting = sid
        wakeLock.acquire(30 * 60_000L)
        setStatus("Downloading recording")
        PlaudDeviceAgent.exportAudio(sid, exportDir, AudioExportFormat.MP3, 1, object : AudioExporter.ExportCallback {
            override fun onProgress(progress: Int, message: String) {}
            override fun onComplete(outputFile: File) {
                scope.launch { onExported(sid, outputFile) }
            }
            override fun onError(error: String) {
                scope.launch {
                    AppLog.i("download of $sid failed: $error")
                    if (exporting == sid) exporting = null
                    exportNext()
                }
            }
        })
    }

    private suspend fun onExported(sid: Long, file: File) {
        // sessionId is the recording's start time in unix seconds
        val name = SimpleDateFormat("yyyy-MM-dd_HH-mm-ss", Locale.US)
            .format(if (sid > 1_000_000_000) Date(sid * 1000) else Date())
        try {
            val kb = file.length() / 1024
            withContext(Dispatchers.IO) {
                saveToDocuments("$name.mp3", "audio/mpeg") { out -> file.inputStream().use { it.copyTo(out) } }
                file.copyTo(File(pendingDir, "$name.mp3"), overwrite = true) // kept until transcribed
                file.delete()
            }
            AppLog.i("saved $name.mp3 ($kb KB)")
            // Only now is the audio safe on the phone, so free it on the device
            val synced = prefs.getStringSet("synced", emptySet())!! + sid.toString()
            prefs.edit().putStringSet("synced", synced).apply()
            PlaudDeviceAgent.deleteFile(sid)
        } catch (e: Exception) {
            AppLog.i("saving $sid failed, left on device: ${e.message}")
        }
        if (exporting == sid) exporting = null
        exportNext()
        scope.launch { transcribePending() }
    }

    private suspend fun transcribePending() {
        val pending = pendingDir.listFiles().orEmpty().sortedBy { it.name }
        if (pending.isEmpty()) return
        if (BuildConfig.API_KEY.isEmpty()) return AppLog.i("no api_key in this build, ${pending.size} transcript(s) waiting")
        if (!transcribing.tryLock()) return
        try {
            for (mp3 in pending) {
                AppLog.i("transcribing ${mp3.name}")
                try {
                    val t = token
                    val text = withContext(Dispatchers.IO) { PlaudApi.transcribe(t, mp3) }
                    withContext(Dispatchers.IO) {
                        saveToDocuments("${mp3.nameWithoutExtension}.txt", "text/plain") { it.write(text.toByteArray()) }
                        // In voice mode a recording is a question, not a transcript to hand over.
                        if (Fridolin.enabled && VoicePrefs.enabled(this@SyncService)) writeQuestion(SpeechText.question(text))
                        else if (Fridolin.enabled) File(outboxDir, "${mp3.nameWithoutExtension}.txt").writeBytes(text.toByteArray())
                    }
                    mp3.delete()
                    AppLog.i("transcript saved: ${mp3.nameWithoutExtension}.txt")
                } catch (e: Exception) {
                    AppLog.i("transcription of ${mp3.name} failed, will retry: ${e.message}")
                    break
                }
            }
        } finally {
            transcribing.unlock()
        }
        uploadPending()
    }

    /** Sends the transcripts in the outbox to Fridolin, oldest first; what fails waits for the next try. */
    private suspend fun uploadPending() {
        if (!Fridolin.enabled || !uploading.tryLock()) return
        try {
            for (txt in outboxDir.listFiles().orEmpty().sortedBy { it.name }) {
                try {
                    withContext(Dispatchers.IO) { Fridolin.upload(this@SyncService, txt.nameWithoutExtension, txt.readBytes()) }
                    txt.delete()
                    AppLog.i("sent ${txt.name} to Fridolin")
                } catch (e: Exception) {
                    AppLog.i("sending ${txt.name} to Fridolin failed, will retry: ${e.message}")
                    break
                }
            }
        } finally {
            uploading.unlock()
        }
    }

    // --- voice mode ---------------------------------------------------------------------------

    /** Keeps a question until it is sent (so a restart or no network loses nothing). */
    private fun writeQuestion(text: String) {
        if (text.isBlank()) return
        File(voiceDir, Fridolin.newQuestionId() + ".ask").writeText(text)
    }

    private fun queueQuestion(text: String) {
        if (!Fridolin.enabled) return AppLog.i("voice: this build has no site to ask Fridolin through")
        scope.launch {
            withContext(Dispatchers.IO) { writeQuestion(text) }
            AppLog.i("voice: question taken")
            sendQuestions()
        }
    }

    /** After a start: what was asked waits for its answer again, and what was not is sent. */
    private fun resumeQuestions() {
        if (!Fridolin.enabled) return
        voiceDir.listFiles { f -> f.name.endsWith(".wait") }.orEmpty().forEach { awaitAnswer(it) }
        scope.launch { sendQuestions() }
    }

    /** Sends the questions waiting to go, oldest first; what fails waits for the next try. */
    private suspend fun sendQuestions() {
        if (!asking.tryLock()) return
        try {
            for (q in voiceDir.listFiles { f -> f.name.endsWith(".ask") }.orEmpty().sortedBy { it.lastModified() }) {
                val rid = q.nameWithoutExtension
                try {
                    withContext(Dispatchers.IO) { Fridolin.ask(this@SyncService, rid, q.readBytes()) }
                    val wait = File(voiceDir, "$rid.wait")
                    q.renameTo(wait)
                    AppLog.i("voice: asked Fridolin")
                    awaitAnswer(wait)
                } catch (e: Exception) {
                    AppLog.i("voice: asking failed, will retry: ${e.message}")
                    scope.launch { delay(60_000); sendQuestions() }
                    break
                }
            }
        } finally {
            asking.unlock()
        }
        refreshNotification()
    }

    /** Fetches the answer to a question that was sent, until it is there or [ANSWER_WAIT_MS] passed. */
    private fun awaitAnswer(wait: File) {
        val rid = wait.nameWithoutExtension
        if (!awaiting.add(rid)) return
        refreshNotification()
        scope.launch {
            try {
                while (System.currentTimeMillis() - wait.lastModified() < ANSWER_WAIT_MS) {
                    val r = try {
                        withContext(Dispatchers.IO) { Fridolin.reply(this@SyncService, rid) }
                    } catch (e: Exception) {
                        AppLog.i("voice: fetching the answer failed, will retry: ${e.message}")
                        delay(10_000)
                        continue
                    }
                    when (r.state) {
                        "waiting" -> { delay(1_000); continue } // the site held it 15 s already
                        "done" -> if (r.text != null) {
                            AppLog.i("voice: answer received")
                            speaker.say(r.text)
                        } else AppLog.i("voice: Fridolin answered in WhatsApp only")
                        "failed" -> AppLog.i("voice: Fridolin could not answer: ${r.error}")
                        else -> AppLog.i("voice: Fridolin no longer knows the question (restarted?); its answer is in WhatsApp")
                    }
                    return@launch
                }
                AppLog.i("voice: no answer within ${ANSWER_WAIT_MS / 60_000} minutes")
            } finally {
                wait.delete()
                awaiting.remove(rid)
                refreshNotification()
            }
        }
    }

    /**
     * Write into the public Documents/PlaudSync folder (visible to file managers and sync apps).
     * Pending until written: on disk it is ".pending-…" meanwhile, so a sync tool never takes
     * half a file.
     */
    private fun saveToDocuments(name: String, mime: String, write: (OutputStream) -> Unit) {
        val values = ContentValues().apply {
            put(MediaStore.MediaColumns.DISPLAY_NAME, name)
            put(MediaStore.MediaColumns.MIME_TYPE, mime)
            put(MediaStore.MediaColumns.RELATIVE_PATH, "${Environment.DIRECTORY_DOCUMENTS}/PlaudSync")
            put(MediaStore.MediaColumns.IS_PENDING, 1)
        }
        val uri = contentResolver.insert(MediaStore.Files.getContentUri(MediaStore.VOLUME_EXTERNAL_PRIMARY), values)
            ?: error("MediaStore refused $name")
        try {
            contentResolver.openOutputStream(uri)!!.use(write)
        } catch (e: Exception) {
            contentResolver.delete(uri, null, null)
            throw e
        }
        contentResolver.update(uri, ContentValues().apply { put(MediaStore.MediaColumns.IS_PENDING, 0) }, null, null)
    }

    // --- SDK callbacks ----------------------------------------------------------------------

    private val listener = object : PlaudDeviceAgentListener {
        override fun bleScanResult(devices: List<BleDevice>) {
            scope.launch {
                if (connected || connecting) return@launch
                val want = savedSn
                val dev = devices.firstOrNull { d ->
                    val sn = d.getSerialNumber() ?: return@firstOrNull false
                    if (want != null) sn == want else sn.startsWith("881") || sn.startsWith("882")
                } ?: return@launch
                PlaudDeviceAgent.stopScan()
                connect(dev)
            }
        }

        override fun bleScanOverTime() {
            scope.launch { if (!connected && !connecting) rescanLater(30_000) }
        }

        override fun bleConnectState(state: Int) {
            scope.launch { onConnectState(state) }
        }

        override fun bleBind(sn: String?, status: Int, protVersion: Int, timezone: Int) {
            scope.launch { onBind(sn, status) }
        }

        override fun bleRecordStart(sessionId: Long, start: Long, status: Int, scene: Int, startTime: Long, reason: Int) {
            scope.launch {
                recordingSid = sessionId
                AppLog.i("recording started")
            }
        }

        override fun bleRecordStop(sessionId: Long, reason: Int, fileExist: Boolean, fileSize: Long) {
            scope.launch {
                recordingSid = null
                AppLog.i("recording stopped, fetching it")
                delay(1_500)
                requestFileList()
            }
        }

        override fun bleRecordStateRefreshed(isRecording: Boolean, sessionId: Long) {
            scope.launch {
                recordingSid = if (isRecording) sessionId else null
                if (isRecording) AppLog.i("device is recording; will sync when it stops") else requestFileList()
            }
        }

        override fun bleFileList(files: List<BleFile>) {
            scope.launch { onFileList(files) }
        }

        override fun bleDepair(status: Int) {
            AppLog.i("device depair status $status")
        }
    }

    private fun setStatus(s: String) {
        status = s
        refreshNotification()
    }

    private fun refreshNotification() {
        getSystemService(NotificationManager::class.java).notify(1, notification(status))
        AppLog.onChange?.invoke()
    }

    /** The ongoing notification; in voice mode it says so and offers Ask, Stop and Skip. Never an answer's text. */
    private fun notification(text: String): Notification {
        val b = Notification.Builder(this, "sync")
            .setSmallIcon(android.R.drawable.stat_sys_data_bluetooth)
            .setContentTitle("Plaud Sync")
            .setContentText(text)
            .setOngoing(true)
            .setVisibility(Notification.VISIBILITY_PRIVATE)
            .setContentIntent(
                PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
            )
        if (!VoicePrefs.enabled(this) || !::speaker.isInitialized) return b.build()
        b.setContentTitle("Plaud Sync · voice mode")
        b.setSubText(
            when {
                speaker.held -> "Answer ready, unlock to hear it"
                speaker.busy -> "Speaking"
                awaiting.isNotEmpty() -> "Waiting for Fridolin's answer"
                else -> null
            }
        )
        fun action(label: String, action: String, code: Int) = Notification.Action.Builder(null, label,
            PendingIntent.getService(this, code, Intent(this, SyncService::class.java).setAction(action), PendingIntent.FLAG_IMMUTABLE)).build()
        b.addAction(Notification.Action.Builder(null, "Ask",
            PendingIntent.getActivity(this, 10, Intent(this, AskActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK), PendingIntent.FLAG_IMMUTABLE)).build())
        if (speaker.busy) {
            b.addAction(action("Stop", ACTION_STOP_SPEECH, 11))
            b.addAction(action("Skip", ACTION_SKIP, 12))
        }
        return b.build()
    }
}
