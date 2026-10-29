package local.plaudsync

/**
 * The pin is told apart by its serial number, never by its Bluetooth address, which the pin
 * rotates (a private address). This only notices a new address so it is noted once: [changed]
 * is true the first time an address differs from the last one seen, and never for the same one
 * again, so a rotation can neither repeat in the log nor have any say in connecting.
 */
class PinAddress {
    private var last: String? = null

    fun changed(address: String?): Boolean {
        if (address == null || address == last) return false
        val had = last != null
        last = address
        return had
    }
}
