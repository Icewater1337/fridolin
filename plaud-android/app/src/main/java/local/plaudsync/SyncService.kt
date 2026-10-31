package local.plaudsync

import android.app.AlarmManager
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
import android.os.SystemClock
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
 * Foreground service that pulls every recording off the NotePin: device → MP3 in
 * Documents/PlaudSync → delete on device → Plaud transcription → .txt next to the MP3.
 *
 * How often it connects is the sync setting ([SyncMode], [SyncPolicy]): in instant mode the
 * BLE link stays up and a recording is fetched as soon as it stops; otherwise the app checks in
 * (connect → sync → disconnect) on a timer and on events, so the pin can sleep in between
 * (docs/PLAUD.md, "The pin's battery").
 *
 * In voice mode (docs/PLAUD.md, "Voice mode") a recording goes, whole, to Fridolin's voice brain
 * instead, which takes it as a question or as a recording to file; what is said to the phone's
 * speech recognizer ([AskActivity]) is a question. The answer is read aloud ([Speaker]).
 *
 * At check-ins it also asks Fridolin for a newer build of the app, and installs one once nothing
 * would be cut off by it ([Updater]; docs/PLAUD.md, "Updates").
 */
class SyncService : Service() {

    companion object {
        const val ACTION_PAIR = "pair"
        const val ACTION_UNPAIR = "unpair"
        const val ACTION_ASK = "ask"
        const val ACTION_STOP_SPEECH = "stop_speech"
        const val ACTION_SKIP = "skip"
        const val ACTION_REFRESH = "refresh"
        const val ACTION_CHECK_IN = "check_in"
        const val EXTRA_TEXT = "text"
        const val EXTRA_TRIGGER = "trigger"

        /** Sightings this soon after the watch scan starts set the baseline advertisement (see [onSeen]). */
        private const val ADVERT_SETTLE_MS = 60_000L
        /** How long a check-in looks for the pin before it counts as not in range. */
        private const val SEEK_MS = 20_000L

        /** How long the app waits for the answer to a voice question. */
        private const val ANSWER_WAIT_MS = 30 * 60_000L

        @Volatile
        var status = "Not running"
            private set

        fun start(ctx: Context, action: String? = null) {
            ctx.startForegroundService(Intent(ctx, SyncService::class.java).setAction(action))
        }

        /** Asks for a check-in with the pin; [SyncPolicy.checkInOn] decides whether it happens now. */
        fun checkIn(ctx: Context, trigger: Trigger) {
            ctx.startForegroundService(Intent(ctx, SyncService::class.java).setAction(ACTION_CHECK_IN).putExtra(EXTRA_TRIGGER, trigger.key))
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

    // Plaud's user token, renewed by its age on a clock that runs while the phone sleeps
    private val tokens = PlaudToken(SystemClock::elapsedRealtime) {
        PlaudApi.userToken().also { t ->
            AppLog.i("token renewed")
            scope.launch { if (sdkReady) NiceBuildSdk.setPartnerToken(t) } // the SDK keeps a copy
        }
    }
    private var sdkReady = false
    private var pairing = false
    private var connecting = false
    private var connected = false
    private var exporting: Long? = null
    private var recordingSid: Long? = null
    private var listAgain = false
    private val queue = ArrayDeque<Long>()
    private var pairJob: Job? = null
    private var searching = false
    private var searchTimeout: Job? = null
    private var connectTimeout: Job? = null
    private var idleJob: Job? = null
    private var cloudBound = false
    // Check-ins (SyncPolicy): the pin as last found, misses in a row, when the last one started
    private var device: BleDevice? = null
    private val pinAddress = PinAddress()
    private val stall = StallWatch(System::currentTimeMillis)
    private var runJob: Job? = null
    private var pulled = 0
    private var misses = 0
    private var lastCheckIn = -SyncPolicy.UNLOCK_GAP_MS
    private var nextCheckIn = 0L
    private var leaving = false
    private var unpairing = false
    // The watch scan (PinWatch) and what it learns from the pin's advertisement (AdvertSignal)
    private lateinit var watch: PinWatch
    private var watchStarted = 0L
    private var seenSinceWatch = false
    private var seeking = false
    // The last miss saw no pin at all (rather than one that would not connect)
    private var lastMissUnseen = false
    private var advertBaseline: ByteArray? = null
    private var advertCheckIn = false
    private var advertHit = false
    private val alarms by lazy { getSystemService(AlarmManager::class.java) }
    private val checkInAlarm by lazy {
        PendingIntent.getForegroundService(this, 20,
            Intent(this, SyncService::class.java).setAction(ACTION_CHECK_IN).putExtra(EXTRA_TRIGGER, Trigger.TIMER.key),
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }
    private val transcribing = RunAgain()
    private val uploading = RunAgain()
    private val updating = RunAgain()
    // A check for updates, and an install, that the app's opening asked for, still to come
    private var updateForced = false
    private var installForced = false
    private val pendingDir by lazy { File(filesDir, "pending").apply { mkdirs() } }
    // Transcripts waiting to reach Fridolin (Fridolin.kt), when the app was built for it
    private val outboxDir by lazy { File(filesDir, "outbox").apply { mkdirs() } }
    private val exportDir by lazy { File(cacheDir, "export").apply { mkdirs() } }
    // Voice mode: <id>.ask (a question) and <id>.pin (a recording) wait to be sent, <id>.wait for
    // the answer ([VoiceQueue])
    private val voiceDir by lazy { File(filesDir, "voice").apply { mkdirs() } }
    private val asking = Mutex()
    private val awaiting = mutableSetOf<String>()
    private lateinit var speaker: Speaker
    private val unlocked = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            speaker.resume()
            refreshNotification()
            trigger(Trigger.UNLOCKED)
        }
    }
    private val charging = object : BroadcastReceiver() {
        override fun onReceive(context: Context?, intent: Intent?) {
            trigger(Trigger.CHARGING)
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
        registerReceiver(charging, IntentFilter(Intent.ACTION_POWER_CONNECTED))
        watch = PinWatch(this) { dev, adv -> scope.launch { onSeen(dev, adv) } }
        AppLog.i("service started")
        runJob = scope.launch { run() }
        scope.launch { watchdog() }
    }

    /**
     * Nothing may leave the sync standing still without a word. Every minute the state is looked at:
     * the same busy state for too long (a connect, scan or download the SDK never answers, an
     * error that left a flag set), an alarm that never came, or a start-up that died, and the
     * sync is reset and begun again, with a line in the log saying so.
     */
    private suspend fun watchdog() {
        while (true) {
            delay(60_000)
            val now = System.currentTimeMillis()
            val busy = connecting || searching || connected || exporting != null
            val key: String
            val limit: Long
            when {
                !sdkReady -> { key = "start"; limit = 10 * 60_000L; if (runJob?.isActive == true) continue }
                exporting != null -> { key = "exporting $exporting ${queue.size}"; limit = 45 * 60_000L }
                connected && SyncPolicy.stayConnected(SyncPrefs.mode(this), recordingSid != null, unpairing) -> continue
                busy -> { key = "busy $connecting $searching $connected ${queue.size} $leaving"; limit = 8 * 60_000L }
                savedSn != null && nextCheckIn > 0 && now > nextCheckIn + 5 * 60_000L -> { key = "overdue $nextCheckIn"; limit = 5 * 60_000L }
                else -> { key = "idle"; limit = Long.MAX_VALUE }
            }
            if (!stall.stalled(key, limit)) continue
            AppLog.i("watchdog: the sync stood still ($key), starting over")
            setStatus("Sync stalled, restarted ${clock(now)}")
            if (!sdkReady) {
                runJob = scope.launch { run() }
                continue
            }
            runCatching { PlaudDeviceAgent.stopScan() }
            runCatching { PlaudDeviceAgent.disconnect() }
            endSearch()
            watch.stop()
            connectTimeout?.cancel()
            idleJob?.cancel()
            connecting = false; connected = false; leaving = false
            exporting = null; recordingSid = null; listAgain = false
            queue.clear()
            if (wakeLock.isHeld) wakeLock.release()
            checkIn(Trigger.TIMER)
        }
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        when (intent?.action) {
            ACTION_PAIR -> {
                pairing = true
                device = null
                AppLog.i("pairing: looking for a NotePin S / Note Pro")
                scan()
            }
            ACTION_CHECK_IN -> {
                val t = Trigger.of(intent.getStringExtra(EXTRA_TRIGGER))
                trigger(t)
                retryWaiting()
                scope.launch { updates(t == Trigger.OPENED || t == Trigger.SYNC_NOW) }
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
        alarms.cancel(checkInAlarm)
        watch.stop()
        unregisterReceiver(unlocked)
        unregisterReceiver(charging)
        speaker.shutdown()
        if (wakeLock.isHeld) wakeLock.release()
        super.onDestroy()
    }

    private suspend fun run() {
        val token = firstToken()
        // SDK quirk (see Plaud's template app): its partner API defaults to the JP host and
        // ignores customDomain, so the RSA key fetch fails unless we point it at our region.
        NiceBuildSdk.getPartnerApiManager().updateBaseUrl("https://${PlaudApi.DOMAIN}")
        PlaudDeviceAgent.listener = listener
        PlaudDeviceAgent.initSDK(applicationContext, token, PlaudApi.DOMAIN)
        PlaudDeviceAgent.setBleLogLevel(android.util.Log.INFO)
        sdkReady = true
        AppLog.i("sync: ${describe(SyncPrefs.mode(this))}")
        checkIn(Trigger.TIMER)
        transcribePending()
        resumeQuestions()
        scope.launch {
            while (true) {
                uploadPending()
                delay(15 * 60_000L) // what failed (offline, say) is tried again
            }
        }
    }

    /**
     * What waits for Plaud or Fridolin is tried again with each check-in, whether or not it
     * reaches the pin: its alarm wakes the phone, while a coroutine's delay counts only the time
     * the phone is awake. The token is renewed here too once it is old, so the SDK's copy stays
     * current.
     */
    private fun retryWaiting() {
        if (!sdkReady) return
        scope.launch {
            withContext(Dispatchers.IO) { runCatching { tokens.get() } }
            transcribePending()
        }
    }

    /**
     * Asks Fridolin for a newer build of the app, fetches it, and installs it if the sync is idle
     * now; otherwise once it is ([installUpdate]). [forced]: the app was opened, or Fetch now.
     */
    private suspend fun updates(forced: Boolean) {
        if (!Fridolin.enabled) return
        updateForced = updateForced || forced
        updating.run {
            val f = updateForced
            updateForced = false
            withContext(Dispatchers.IO) {
                runCatching { Updater.check(this@SyncService, f) }.onFailure { AppLog.i("update: ${it.message}") }
            }
            // The check-in the opening started may make the sync busy: the install it asked for waits for it.
            installForced = installForced || f
            installUpdate()
        }
    }

    /**
     * Installs a build that was fetched, unless that would cut something off: the update ends this
     * process. Not while the app looks for the pin, connects to it, fetches from it or the pin
     * records, nor while an answer is read aloud; the link itself may stay up in instant mode, and
     * the new version connects again. Called again whenever the sync goes idle.
     */
    private fun installUpdate() {
        if (!Fridolin.enabled || connecting || searching || leaving || exporting != null || queue.isNotEmpty() || recordingSid != null || speaker.busy) return
        if (connected && SyncPrefs.mode(this) != SyncMode.INSTANT) return
        val forced = installForced
        installForced = false
        scope.launch(Dispatchers.IO) {
            if (!Updater.due(this@SyncService, forced)) return@launch
            AppLog.i("update: the sync is idle, installing")
            Updater.install(this@SyncService)
        }
    }

    /** The token the SDK starts with, asked for until Plaud gives one. */
    private suspend fun firstToken(): String {
        var wait = 10_000L
        while (true) {
            try {
                return withContext(Dispatchers.IO) { tokens.get() }
            } catch (e: Exception) {
                AppLog.i("token failed (${e.message}), retrying in ${wait / 1000}s")
            }
            delay(wait)
            wait = minOf(wait * 2, 600_000L)
        }
    }

    // --- connection -------------------------------------------------------------------------

    private val voice get() = Fridolin.enabled && VoicePrefs.enabled(this)

    private fun describe(m: SyncMode) = when (m) {
        SyncMode.INSTANT -> "instant, the link stays up"
        SyncMode.SMART -> "smart, a check-in every ${SyncPolicy.SMART_MINUTES} min and on events"
        SyncMode.INTERVAL -> "every ${SyncPrefs.minutes(this)} min"
    } + if (m != SyncMode.INSTANT && voice) " (voice mode: at least every ${SyncPolicy.VOICE_MINUTES} min)" else ""

    /** An event that may start a check-in; returns whether it did. */
    private fun trigger(t: Trigger): Boolean {
        if (!sdkReady || pairing) return false
        val mode = SyncPrefs.mode(this)
        if (t == Trigger.SETTINGS) AppLog.i("sync: ${describe(mode)}")
        if (connected) {
            when (t) {
                Trigger.SYNC_NOW -> requestFileList()
                Trigger.SETTINGS -> if (exporting == null && queue.isEmpty()) syncDone()
                else -> {}
            }
            return false
        }
        if (connecting || searching) return false
        if (!SyncPolicy.checkInOn(t, mode, SystemClock.elapsedRealtime() - lastCheckIn)) return false
        if (t != Trigger.TIMER) AppLog.i("checking in with the NotePin (${t.key})")
        checkIn(t)
        return true
    }

    /** Connect → sync → (unless [SyncPolicy.stayConnected]) disconnect. */
    private fun checkIn(t: Trigger) {
        if (!sdkReady || connected || connecting || searching) return
        val sn = savedSn ?: return setStatus("Not paired: open the app and tap Pair")
        alarms.cancel(checkInAlarm)
        watch.stop()
        lastCheckIn = SystemClock.elapsedRealtime()
        advertCheckIn = t == Trigger.ADVERT
        advertHit = false
        wakeLock.acquire(3 * 60_000L)
        val dev = device ?: SyncPrefs.device(this, sn)?.also { device = it }
        // Look for the pin with the app's own scan (it runs with the screen off) and connect to the
        // address it uses now; the SDK's scan only when the company id is not known yet
        val code = dev?.getManufacturerCode()?.takeIf { it >= 0 }
        if (code != null && watch.start(code, fast = true)) seek() else scan()
    }

    private fun seek() {
        setStatus("Looking for NotePin")
        searching = true
        seeking = true
        searchTimeout?.cancel()
        searchTimeout = scope.launch {
            delay(SEEK_MS)
            if (seeking) missed(seen = false)
        }
    }

    private fun scan() {
        if (!sdkReady || connected || connecting) return
        if (savedSn == null && !pairing) return setStatus("Not paired: open the app and tap Pair")
        setStatus(if (pairing) "Pairing: searching" else "Searching for NotePin")
        try {
            PlaudDeviceAgent.startScan()
        } catch (e: Exception) {
            AppLog.i("scan failed: ${e.message}")
            return missed(seen = false)
        }
        searching = true
        // Should the SDK never report its scan over, the search still ends and the next one is planned
        searchTimeout?.cancel()
        searchTimeout = scope.launch {
            delay(90_000)
            if (searching) {
                runCatching { PlaudDeviceAgent.stopScan() }
                missed(seen = false)
            }
        }
    }

    private fun connect(dev: BleDevice) {
        val sn = dev.getSerialNumber()
        device = dev
        endSearch()
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
                withContext(Dispatchers.Main) {
                    if (connecting) {
                        connecting = false
                        missed(seen = true)
                    }
                }
            }
        }
        connectTimeout?.cancel()
        connectTimeout = scope.launch {
            delay(60_000)
            if (connecting && !connected) {
                connecting = false
                AppLog.i("connect timed out")
                PlaudDeviceAgent.disconnect()
                missed(seen = true)
            }
        }
    }

    private fun onConnectState(state: Int) {
        if (state == 1) return // GATT link only; success is bleBind(status 0) after the E2EE handshake
        val wasConnecting = connecting && !connected
        val wasConnected = connected
        connecting = false
        connected = false
        exporting = null
        recordingSid = null
        listAgain = false
        queue.clear()
        idleJob?.cancel()
        when {
            wasConnecting -> {
                AppLog.i("connect failed (state $state)" + if (pairing) ". If this repeats, the NotePin is probably still bound to the Plaud app" else ", will retry")
                missed(seen = true)
            }
            wasConnected && leaving -> {
                leaving = false
                idle()
            }
            wasConnected -> {
                AppLog.i("disconnected")
                if (wakeLock.isHeld) wakeLock.release()
                // Out of range for a moment, say: try again soon, the pin may hold a recording
                scheduleCheckIn(SyncPolicy.DROPPED_RETRY_MS)
                setStatus("Link lost, trying again shortly")
            }
        }
    }

    private fun onBind(sn: String?, status: Int) {
        if (status != 0 || sn == null) return AppLog.i("device bind failed (status $status)")
        connecting = false
        connected = true
        pairing = false
        pairJob?.cancel()
        connectTimeout?.cancel()
        if (misses > 0) AppLog.i("NotePin found again")
        misses = 0
        if (savedSn != sn) {
            savedSn = sn
            SyncPrefs.setAdvertSignal(this, AdvertSignal())
            AppLog.i("paired with ${sn.take(3)}…${sn.takeLast(4)}")
        } else {
            AppLog.i("connected")
        }
        device?.let { SyncPrefs.setDevice(this, it) }
        if (unpairing) return unpair()
        setStatus("Connected")
        if (!cloudBound) {
            cloudBound = true
            scope.launch(Dispatchers.IO) {
                runCatching { PlaudApi.binding(tokens.get(), "bind", sn) }.onFailure { AppLog.i("cloud bind: ${it.message}") }
            }
        }
        PlaudDeviceAgent.syncTime()
        PlaudDeviceAgent.refreshRecordState() // → bleRecordStateRefreshed → file list once idle
        // Should the pin not answer, the link still goes once nothing is happening on it
        idleJob = scope.launch {
            while (true) {
                delay(2 * 60_000L)
                if (exporting == null && queue.isEmpty() && !leaving) syncDone()
            }
        }
    }

    /** Nothing left to fetch: let the pin go, unless [SyncPolicy.stayConnected] keeps the link. */
    private fun syncDone() {
        AppLog.i("sync done: $pulled recording(s) pulled")
        pulled = 0
        if (wakeLock.isHeld) wakeLock.release()
        if (advertCheckIn) learnAdvert(advertHit)
        if (SyncPolicy.stayConnected(SyncPrefs.mode(this), recordingSid != null, unpairing)) {
            installUpdate()
            return setStatus(if (recordingSid != null) "Connected, NotePin is recording" else "Connected, everything synced")
        }
        if (leaving) return
        leaving = true
        setStatus("Synced, letting the NotePin sleep")
        scope.launch {
            delay(3_000) // the last delete reaches the pin
            if (!connected || exporting != null || queue.isNotEmpty() || recordingSid != null) {
                leaving = false
                return@launch
            }
            PlaudDeviceAgent.disconnect()
            delay(10_000)
            if (leaving && connected) onConnectState(0) // the SDK said nothing: count it as gone
        }
    }

    /** Disconnected after a sync: wait for the next check-in. */
    private fun idle() {
        if (connected || connecting) return
        if (savedSn == null) return setStatus("Not paired: open the app and tap Pair")
        misses = 0
        val wait = SyncPolicy.interval(SyncPrefs.mode(this), SyncPrefs.minutes(this), voice)
            ?: return checkIn(Trigger.SETTINGS) // instant mode: the link belongs up
        scheduleCheckIn(wait)
        setStatus("Synced ${clock(System.currentTimeMillis())}, next check ${clock(nextCheckIn)}")
        installUpdate()
    }

    private fun endSearch() {
        searching = false
        searchTimeout?.cancel()
        if (seeking) {
            seeking = false
            watch.stop()
        }
    }

    /**
     * A check-in that did not reach the pin: wait longer each time ([SyncPolicy.backoff]), and watch
     * for it. [seen]: the pin was there but did not connect, so its showing up again says nothing.
     */
    private fun missed(seen: Boolean) {
        endSearch()
        if (wakeLock.isHeld) wakeLock.release()
        advertCheckIn = false // out of reach says nothing about the advertisement
        if (pairing) {
            pairJob?.cancel()
            pairJob = scope.launch { delay(30_000); scan() }
            return
        }
        misses++
        lastMissUnseen = !seen
        val wait = SyncPolicy.backoff(SyncPrefs.mode(this), SyncPrefs.minutes(this), voice, misses)
        if (seen || misses == 1 || misses % 10 == 0) {
            AppLog.i((if (seen) "NotePin seen but not connected ($misses): the Plaud app may still hold it, or the link failed."
                else "NotePin not seen ($misses): out of range?") +
                " Next try in ${wait / 1000}s" + if (seen) "" else ", or when it is back in range")
        }
        scheduleCheckIn(wait)
        setStatus("NotePin not in range, next try ${clock(nextCheckIn)}" + if (unpairing) " (to unpair it)" else "")
        installUpdate()
    }

    /** A wake-up alarm (Doze lets it through, inexactly), and the watch scan meanwhile. */
    private fun scheduleCheckIn(ms: Long) {
        nextCheckIn = System.currentTimeMillis() + ms
        alarms.setAndAllowWhileIdle(AlarmManager.ELAPSED_REALTIME_WAKEUP, SystemClock.elapsedRealtime() + ms, checkInAlarm)
        val code = device?.getManufacturerCode()?.takeIf { it >= 0 }
        if (code == null || !SyncPolicy.watch(SyncPrefs.mode(this), misses > 0)) return watch.stop()
        watchStarted = SystemClock.elapsedRealtime()
        seenSinceWatch = false
        advertBaseline = null
        watch.start(code, fast = false)
    }

    /**
     * The app's scan saw a Plaud device. If it is the pin and a check-in looks for it, connect to
     * it. Between check-ins, after a miss that saw no pin, it is back in range. Otherwise a change in
     * its advertisement may mean a recording started or stopped: Plaud documents no such signal, so
     * [AdvertSignal] learns whether it holds for this pin.
     */
    private fun onSeen(dev: BleDevice, adv: ByteArray) {
        if (savedSn == null || dev.getSerialNumber() != savedSn) return
        if (seeking) {
            // The pin is matched by serial number above; a rotated address is only noted, never acted on
            if (pinAddress.changed(dev.getMacAddress())) AppLog.d("the NotePin's Bluetooth address changed")
            return connect(dev)
        }
        if (connected || connecting || leaving || searching) return
        device = dev
        val since = SystemClock.elapsedRealtime() - watchStarted
        if (misses > 0) {
            if (seenSinceWatch || !lastMissUnseen) return
            seenSinceWatch = true
            trigger(Trigger.IN_RANGE)
            return
        }
        val base = advertBaseline
        advertBaseline = adv
        if (base == null || since < ADVERT_SETTLE_MS || base.contentEquals(adv)) return
        if (!SyncPrefs.advertSignal(this).trusted) return
        val changed = SyncPolicy.changedBytes(base, adv)
        if (trigger(Trigger.ADVERT)) AppLog.i("the NotePin's advertisement changed (bytes ${changed.joinToString(",")})")
    }

    private fun learnAdvert(hit: Boolean) {
        advertCheckIn = false
        val before = SyncPrefs.advertSignal(this)
        val after = before.tried(hit)
        SyncPrefs.setAdvertSignal(this, after)
        AppLog.i(if (hit) "the advertisement change came with a recording" else "the advertisement change brought nothing new")
        if (before.trusted && !after.trusted) AppLog.i("this NotePin's advertisement changes say nothing about recordings: no longer watched for that")
    }

    private fun onBattery(level: Int, charging: Boolean?) {
        if (level !in 0..100) return
        SyncPrefs.setBattery(this, SyncPrefs.Battery(level, charging, System.currentTimeMillis()))
        AppLog.i("NotePin battery $level%" + if (charging == true) ", charging" else "")
        refreshNotification()
    }

    private fun clock(ms: Long) = SimpleDateFormat("HH:mm", Locale.getDefault()).format(Date(ms))

    private fun unpair() {
        val sn = savedSn ?: return AppLog.i("unpair: nothing paired")
        // Without the BLE depair the device stays locked to this app, so it needs a live link
        if (!connected) {
            unpairing = true
            AppLog.i("unpair: connecting to the NotePin first, keep it close by")
            if (!connecting) checkIn(Trigger.SYNC_NOW)
            return
        }
        AppLog.i("unpairing")
        unpairing = false
        cloudBound = false
        savedSn = null
        device = null
        SyncPrefs.setDevice(this, null)
        alarms.cancel(checkInAlarm)
        watch.stop()
        scope.launch {
            withContext(Dispatchers.IO) {
                runCatching { PlaudApi.binding(tokens.get(), "unbind", sn) }.onFailure { AppLog.i("cloud unbind: ${it.message}") }
            }
            PlaudDeviceAgent.depair(false)
            delay(3_000)
            leaving = true
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
        AppLog.i("files listed: ${onDevice.size} on device")
        val fresh = onDevice
            .filter { it !in synced && it != exporting && it != recordingSid && it !in queue }
            .sorted()
        if (fresh.isNotEmpty()) {
            AppLog.i("${fresh.size} new recording(s) on device")
            advertHit = true
        }
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
            return syncDone()
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
                // Kept until transcribed. Copied under another name first: a transcription pass
                // running meanwhile takes only whole .mp3 files.
                val part = file.copyTo(File(pendingDir, "$name.mp3.part"), overwrite = true)
                check(part.renameTo(File(pendingDir, "$name.mp3"))) { "could not keep $name.mp3 for transcription" }
                file.delete()
            }
            pulled++
            AppLog.i("saved $name.mp3 ($kb KB)")
            PlaudDeviceAgent.getChargingState() // → bleChargingState: the pin's battery, logged with each recording
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

    /** Transcribes what waits, then sends it on; a call while that runs makes it run again ([RunAgain]). */
    private suspend fun transcribePending() = transcribing.run { transcribePass() }

    /**
     * Transcribes the recordings in pending/, oldest first, those saved meanwhile too, up to the
     * first that fails (the next check-in tries it again); then sends the transcripts and questions.
     */
    private suspend fun transcribePass() {
        if (BuildConfig.API_KEY.isEmpty()) {
            val waiting = pendingMp3s().size
            if (waiting > 0) AppLog.i("no api_key in this build, $waiting transcript(s) waiting")
            return
        }
        while (true) {
            val mp3 = pendingMp3s().firstOrNull() ?: break
            AppLog.i("transcribing ${mp3.name}")
            try {
                val text = withContext(Dispatchers.IO) { transcribe(mp3) }
                val name = mp3.nameWithoutExtension
                withContext(Dispatchers.IO) {
                    saveToDocuments("$name.txt", "text/plain") { it.write(text.toByteArray()) }
                    // In voice mode a recording goes to the voice brain whole, which takes it as a
                    // question or as a recording to file, as it hears it, and answers aloud.
                    if (Fridolin.enabled && VoicePrefs.enabled(this@SyncService)) {
                        if (SpeechText.question(text).isBlank()) AppLog.i("voice: nothing said in $name, so nothing sent")
                        else writePin(name, text)
                    } else if (Fridolin.enabled) File(outboxDir, "$name.txt").writeBytes(text.toByteArray())
                }
                AppLog.i("transcript saved: $name.txt")
                if (!mp3.delete()) {
                    AppLog.i("could not remove ${mp3.name} from the transcription queue")
                    break
                }
            } catch (e: PlaudApi.TranscriptionFailed) {
                // Not tried again with every check-in, and not in the way of the recordings after it
                AppLog.i("Plaud could not transcribe ${mp3.name} (${e.message}): skipped, the audio is in Documents/PlaudSync")
                if (!mp3.renameTo(File(pendingDir, "${mp3.name}.failed"))) break
            } catch (e: Exception) {
                AppLog.i("transcription of ${mp3.name} failed, will retry: ${e.message}")
                break
            }
        }
        uploadPending()
        // In voice mode the loop above wrote questions: ask them now, not at the next start
        if (Fridolin.enabled) sendQuestions()
    }

    private fun pendingMp3s() = pendingDir.listFiles { f -> f.name.endsWith(".mp3") }.orEmpty().sortedBy { it.name }

    /** Plaud's transcript of [mp3]. A token Plaud refuses is renewed, and the call tried once more. */
    private fun transcribe(mp3: File): String = try {
        PlaudApi.transcribe(tokens.get(), mp3)
    } catch (e: PlaudApi.HttpError) {
        if (e.code != 401) throw e
        AppLog.i("Plaud refused the token: renewing it")
        tokens.invalidate()
        PlaudApi.transcribe(tokens.get(), mp3)
    }

    /** Sends the transcripts in the outbox to Fridolin, oldest first; what fails waits for the next try. */
    private suspend fun uploadPending() {
        if (Fridolin.enabled) uploading.run { uploadPass() }
    }

    private suspend fun uploadPass() {
        for (txt in outboxDir.listFiles().orEmpty().sortedBy { it.name }) {
            try {
                withContext(Dispatchers.IO) { Fridolin.upload(this@SyncService, txt.nameWithoutExtension, txt.readBytes()) }
                txt.delete()
                AppLog.i("sent ${txt.name} to Fridolin")
            } catch (e: Fridolin.Refused) {
                // Longer than Fridolin takes: trying again would not change that, and it must not
                // hold up the transcripts after it.
                if (e.code != 413) {
                    AppLog.i("sending ${txt.name} to Fridolin failed, will retry: ${e.message}")
                    break
                }
                txt.delete()
                AppLog.i("${txt.name} is too long for Fridolin, not sent: it is in Documents/PlaudSync")
            } catch (e: Exception) {
                AppLog.i("sending ${txt.name} to Fridolin failed, will retry: ${e.message}")
                break
            }
        }
    }

    // --- voice mode ---------------------------------------------------------------------------

    /** Keeps a question until it is sent (so a restart or no network loses nothing). */
    private fun writeQuestion(text: String) {
        if (text.isBlank()) return
        File(voiceDir, Fridolin.newQuestionId() + "." + VoiceQueue.ASK).writeText(text)
    }

    /** Keeps a recording from the pin, [name] its start time, until it is sent. */
    private fun writePin(name: String, transcript: String) {
        File(voiceDir, Fridolin.newQuestionId() + "." + VoiceQueue.PIN).writeText(VoiceQueue.pin(name, transcript))
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

    /**
     * Sends the questions and recordings from the pin waiting to go, oldest first. When Fridolin
     * cannot be reached, what is left waits for the next try; what it refuses for good
     * ([VoiceQueue.refused]) holds up nothing: a recording goes as a transcript instead, to the
     * Plaud brain, and a question is dropped.
     */
    private suspend fun sendQuestions() {
        if (!asking.tryLock()) return
        var asTranscripts = false
        try {
            val waiting = voiceDir.listFiles { f -> f.extension == VoiceQueue.ASK || f.extension == VoiceQueue.PIN }.orEmpty()
            for (q in waiting.sortedBy { it.lastModified() }) {
                val rid = q.nameWithoutExtension
                val pin = if (q.extension == VoiceQueue.PIN) withContext(Dispatchers.IO) { VoiceQueue.readPin(q.readText()) } else null
                if (q.extension == VoiceQueue.PIN && pin == null) {
                    AppLog.i("voice: ${q.name} holds no recording, dropped")
                    q.delete()
                    continue
                }
                try {
                    withContext(Dispatchers.IO) {
                        if (pin != null) Fridolin.ask(this@SyncService, rid, pin.transcript.toByteArray(), pin.name)
                        else Fridolin.ask(this@SyncService, rid, q.readBytes())
                    }
                    val wait = File(voiceDir, "$rid.wait")
                    q.renameTo(wait)
                    AppLog.i(if (pin != null) "voice: sent ${pin.name} to Fridolin" else "voice: asked Fridolin")
                    awaitAnswer(wait)
                } catch (e: Exception) {
                    if (e !is Fridolin.Refused || !VoiceQueue.refused(e.code)) {
                        AppLog.i("voice: sending failed, will retry: ${e.message}")
                        scope.launch { delay(60_000); sendQuestions() }
                        break
                    }
                    if (pin != null) {
                        withContext(Dispatchers.IO) { File(outboxDir, "${pin.name}.txt").writeBytes(pin.transcript.toByteArray()) }
                        asTranscripts = true
                        AppLog.i("voice: Fridolin did not take ${pin.name} (${e.message}), sending it as a transcript")
                    } else AppLog.i("voice: Fridolin refused the question (${e.message}), dropped it")
                    q.delete()
                }
            }
        } finally {
            asking.unlock()
        }
        if (asTranscripts) uploadPending()
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
                if (connected || connecting || !searching) return@launch
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
            scope.launch { if (searching && !seeking) missed(seen = false) }
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
                if (!isRecording) return@launch requestFileList()
                AppLog.i("device is recording; will sync when it stops")
                if (advertCheckIn) learnAdvert(true)
                syncDone() // the link stays while it records
            }
        }

        override fun bleFileList(files: List<BleFile>) {
            scope.launch { onFileList(files) }
        }

        override fun bleChargingState(isCharging: Boolean, level: Int) {
            scope.launch { onBattery(level, isCharging) }
        }

        override fun blePowerChange(power: Int, oldPower: Int) {
            scope.launch { onBattery(power, null) }
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

    /**
     * The ongoing notification; between check-ins it offers Fetch now. In voice mode it says so and
     * offers Ask, Stop and Skip. Never an answer's text.
     */
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
        val fetch = Notification.Action.Builder(null, "Fetch now",
            PendingIntent.getService(this, 13, Intent(this, SyncService::class.java).setAction(ACTION_CHECK_IN)
                .putExtra(EXTRA_TRIGGER, Trigger.SYNC_NOW.key), PendingIntent.FLAG_IMMUTABLE)).build()
        val offerFetch = savedSn != null && SyncPrefs.mode(this) != SyncMode.INSTANT
        if (!VoicePrefs.enabled(this) || !::speaker.isInitialized) {
            if (offerFetch) b.addAction(fetch)
            return b.build()
        }
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
        } else if (offerFetch) {
            b.addAction(fetch) // a question just recorded on the pin need not wait for the next check-in
        }
        return b.build()
    }
}
