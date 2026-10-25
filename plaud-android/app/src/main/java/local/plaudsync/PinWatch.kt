package local.plaudsync

import android.annotation.SuppressLint
import android.bluetooth.BluetoothManager
import android.bluetooth.le.ScanCallback
import android.bluetooth.le.ScanFilter
import android.bluetooth.le.ScanResult
import android.bluetooth.le.ScanSettings
import android.content.Context
import com.tinnotech.penblesdk.entity.BleDevice

/**
 * The app's own scan for Plaud devices, filtered by the Bluetooth company id in their advertisement
 * (what the SDK calls the manufacturer code). Android keeps a filtered scan running with the screen
 * off, which it does not for the SDK's unfiltered one, and the filter does not depend on the pin's
 * address. Each sighting is parsed with the SDK's own parser into the [BleDevice] the SDK connects
 * to, so a connection always goes to the address the pin uses now. [onSeen] gets that device and the
 * advertisement's bytes (manufacturer data); which pin it is, the caller checks by serial number.
 *
 * Two speeds: [start] with fast = false watches at low power between check-ins (batched every
 * [REPORT_DELAY_MS] where the Bluetooth chip can batch); fast = true looks for the pin at the start
 * of a check-in, for a few seconds.
 */
@SuppressLint("MissingPermission") // only started once Nearby devices is granted
class PinWatch(private val ctx: Context, private val onSeen: (BleDevice, ByteArray) -> Unit) {

    companion object {
        private const val REPORT_DELAY_MS = 30_000L

        /**
         * The SDK's advertisement parser, the one its own scan uses (an obfuscated name in the
         * SDK build that app/build.gradle pins by hash; a new SDK may rename it, which then fails
         * the build, not the app).
         */
        private fun parse(r: ScanResult): BleDevice? = runCatching { com.plaud.sdk.proto.u4.a(r) }.getOrNull()
    }

    private var watching = false

    private val callback = object : ScanCallback() {
        override fun onScanResult(callbackType: Int, result: ScanResult) = seen(result)
        override fun onBatchScanResults(results: List<ScanResult>) {
            // Several sightings of one pin come in a batch; the newest of each address counts
            results.associateBy { it.device?.address }.values.forEach { seen(it) }
        }
        override fun onScanFailed(errorCode: Int) {
            AppLog.i("watch scan failed ($errorCode)")
            watching = false
        }
    }

    private fun seen(r: ScanResult) {
        val dev = parse(r) ?: return
        if (dev.getSerialNumber().isNullOrEmpty() || dev.getMacAddress().isNullOrEmpty()) return
        val data = r.scanRecord?.manufacturerSpecificData
        val bytes = if (data != null && data.size() > 0) data.valueAt(0) ?: ByteArray(0) else ByteArray(0)
        onSeen(dev, bytes)
    }

    /**
     * Starts, or starts again (Android makes a scan that ran 30 minutes opportunistic, so each
     * check-in restarts it). Returns whether the scan runs.
     */
    fun start(manufacturer: Int, fast: Boolean): Boolean {
        stop()
        val adapter = ctx.getSystemService(BluetoothManager::class.java)?.adapter ?: return false
        if (!adapter.isEnabled) return false
        val scanner = adapter.bluetoothLeScanner ?: return false
        val settings = ScanSettings.Builder()
            .setScanMode(if (fast) ScanSettings.SCAN_MODE_LOW_LATENCY else ScanSettings.SCAN_MODE_LOW_POWER)
            .apply { if (!fast && adapter.isOffloadedScanBatchingSupported) setReportDelay(REPORT_DELAY_MS) }
            .build()
        // Empty data: any advertisement with this company id matches
        val filter = ScanFilter.Builder().setManufacturerData(manufacturer, ByteArray(0)).build()
        return try {
            scanner.startScan(listOf(filter), settings, callback)
            watching = true
            true
        } catch (e: Exception) {
            AppLog.i("watch scan failed: ${e.message}")
            false
        }
    }

    fun stop() {
        if (!watching) return
        watching = false
        try {
            ctx.getSystemService(BluetoothManager::class.java)?.adapter?.bluetoothLeScanner?.stopScan(callback)
        } catch (e: Exception) {
            AppLog.i("stopping the watch scan failed: ${e.message}")
        }
    }
}
