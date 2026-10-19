// Package brain owns runtime identity, pinned folders, per-chat queues,
// history ordering and live/retired run lifecycle. Command composition supplies
// immutable settings, process execution, delivery and host-policy callbacks.
// A forced stop freezes every live and retiring queue and waits for process
// cancellation acknowledgements without waiting on delivery.
package brain
