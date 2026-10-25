package local.plaudsync

import android.content.Context
import android.os.Parcel
import android.util.Base64
import com.tinnotech.penblesdk.entity.BleDevice

/** The sync setting ([SyncMode]) and what the app remembers about the pin between check-ins. */
object SyncPrefs {
    private fun prefs(ctx: Context) = ctx.getSharedPreferences("plaudsync", Context.MODE_PRIVATE)

    fun mode(ctx: Context) = SyncMode.of(prefs(ctx).getString("sync_mode", null))
    fun setMode(ctx: Context, m: SyncMode) = prefs(ctx).edit().putString("sync_mode", m.key).apply()

    /** N of "every N minutes". */
    fun minutes(ctx: Context) = SyncPolicy.minutes(prefs(ctx).getInt("sync_minutes", SyncPolicy.DEFAULT_MINUTES))
    fun setMinutes(ctx: Context, n: Int) = prefs(ctx).edit().putInt("sync_minutes", SyncPolicy.minutes(n)).apply()

    /** The pin's battery as it last reported it: percent, charging (null: not said), when. */
    data class Battery(val level: Int, val charging: Boolean?, val at: Long)

    fun battery(ctx: Context): Battery? = prefs(ctx).run {
        val level = getInt("battery_level", -1)
        if (level < 0) null else Battery(level, if (contains("battery_charging")) getBoolean("battery_charging", false) else null, getLong("battery_at", 0))
    }

    fun setBattery(ctx: Context, b: Battery) = prefs(ctx).edit().apply {
        putInt("battery_level", b.level).putLong("battery_at", b.at)
        if (b.charging != null) putBoolean("battery_charging", b.charging) else remove("battery_charging")
    }.apply()

    fun advertSignal(ctx: Context) = prefs(ctx).run { AdvertSignal(getInt("advert_tries", 0), getInt("advert_hits", 0)) }
    fun setAdvertSignal(ctx: Context, s: AdvertSignal) =
        prefs(ctx).edit().putInt("advert_tries", s.tries).putInt("advert_hits", s.hits).apply()

    /**
     * The pin as the SDK last found it, so a check-in connects straight away instead of scanning
     * first (the SDK's scan has no filter, and Android holds such scans while the screen is off).
     * Kept as a Parcel: if a later SDK or Android cannot read it back, the app scans again.
     */
    fun device(ctx: Context, sn: String?): BleDevice? {
        val s = prefs(ctx).getString("device", null) ?: return null
        return runCatching {
            val bytes = Base64.decode(s, Base64.NO_WRAP)
            val p = Parcel.obtain()
            try {
                p.unmarshall(bytes, 0, bytes.size)
                p.setDataPosition(0)
                BleDevice.CREATOR.createFromParcel(p)
            } finally {
                p.recycle()
            }
        }.getOrNull()?.takeIf { sn != null && it.getSerialNumber() == sn && it.getMacAddress() != null }
    }

    fun setDevice(ctx: Context, dev: BleDevice?) {
        if (dev == null) return prefs(ctx).edit().remove("device").apply()
        val p = Parcel.obtain()
        val bytes = try {
            dev.writeToParcel(p, 0)
            p.marshall()
        } finally {
            p.recycle()
        }
        prefs(ctx).edit().putString("device", Base64.encodeToString(bytes, Base64.NO_WRAP)).apply()
    }
}
