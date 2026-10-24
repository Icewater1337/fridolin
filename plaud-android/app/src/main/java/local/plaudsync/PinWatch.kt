package local.plaudsync

import android.annotation.SuppressLint
import android.bluetooth.BluetoothManager
import android.bluetooth.le.ScanCallback
import android.bluetooth.le.ScanFilter
import android.bluetooth.le.ScanResult
import android.bluetooth.le.ScanSettings
import android.content.Context

/**
 * A low-power scan for the one pin, by its address, while the app is not connected to it. It costs
 * the pin nothing (it advertises anyway) and keeps running with the screen off, which the SDK's
 * unfiltered scan does not. [onSeen] gets the pin's advertisement bytes (manufacturer data) each
 * time the phone reports it: batched every [REPORT_DELAY_MS] where the Bluetooth chip can batch.
 */
@SuppressLint("MissingPermission") // only started once Nearby devices is granted
class PinWatch(private val ctx: Context, private val onSeen: (ByteArray) -> Unit) {

    companion object {
        private const val REPORT_DELAY_MS = 30_000L
    }

    private var watching: String? = null

    private val callback = object : ScanCallback() {
        override fun onScanResult(callbackType: Int, result: ScanResult) = seen(result)
        override fun onBatchScanResults(results: List<ScanResult>) {
            results.lastOrNull()?.let { seen(it) }
        }
        override fun onScanFailed(errorCode: Int) {
            AppLog.i("watch scan failed ($errorCode)")
            watching = null
        }
    }

    private fun seen(r: ScanResult) {
        val data = r.scanRecord?.manufacturerSpecificData
        val bytes = if (data != null && data.size() > 0) data.valueAt(0) ?: ByteArray(0) else ByteArray(0)
        onSeen(bytes)
    }

    /**
     * Starts, or starts again (Android makes a scan that ran 30 minutes opportunistic, so each
     * check-in restarts it).
     */
    fun start(mac: String) {
        stop()
        val adapter = ctx.getSystemService(BluetoothManager::class.java)?.adapter ?: return
        if (!adapter.isEnabled) return
        val scanner = adapter.bluetoothLeScanner ?: return
        val settings = ScanSettings.Builder()
            .setScanMode(ScanSettings.SCAN_MODE_LOW_POWER)
            .apply { if (adapter.isOffloadedScanBatchingSupported) setReportDelay(REPORT_DELAY_MS) }
            .build()
        try {
            scanner.startScan(listOf(ScanFilter.Builder().setDeviceAddress(mac).build()), settings, callback)
            watching = mac
        } catch (e: Exception) {
            AppLog.i("watch scan failed: ${e.message}")
        }
    }

    fun stop() {
        if (watching == null) return
        watching = null
        try {
            ctx.getSystemService(BluetoothManager::class.java)?.adapter?.bluetoothLeScanner?.stopScan(callback)
        } catch (e: Exception) {
            AppLog.i("stopping the watch scan failed: ${e.message}")
        }
    }
}
