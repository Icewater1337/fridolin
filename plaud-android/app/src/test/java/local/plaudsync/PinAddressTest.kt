package local.plaudsync

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class PinAddressTest {
    @Test
    fun theFirstAddressIsNoChange() {
        assertFalse(PinAddress().changed("AA:01"))
    }

    @Test
    fun aRotationIsReportedOnceNoMatterHowOftenItIsSeen() {
        val a = PinAddress()
        a.changed("AA:01")
        assertTrue(a.changed("AA:02"))
        assertFalse(a.changed("AA:02"))
        assertFalse(a.changed("AA:02"))
        assertTrue(a.changed("AA:03"))
    }

    @Test
    fun anUnreadableAddressChangesNothing() {
        val a = PinAddress()
        a.changed("AA:01")
        assertFalse(a.changed(null))
        assertFalse(a.changed("AA:01"))
    }
}
