package org.camfreenest.android;

import java.util.function.BooleanSupplier;
import java.util.function.IntSupplier;
import java.util.function.LongSupplier;

/** Wait for an input slot while draining output and respecting stream resets. */
final class DecoderInputWaiter {
    static final int TIMED_OUT = -1;
    static final int STALE = -2;

    static int waitForInput(IntSupplier poll, Runnable drain, BooleanSupplier current,
                            LongSupplier clock, long timeoutNanos) {
        final long start = clock.getAsLong();
        while (current.getAsBoolean()) {
            drain.run();
            if (!current.getAsBoolean()) return STALE;
            int index = poll.getAsInt();
            if (!current.getAsBoolean()) return STALE;
            if (index >= 0) return index;
            if (clock.getAsLong() - start >= timeoutNanos) return TIMED_OUT;
        }
        return STALE;
    }
}
