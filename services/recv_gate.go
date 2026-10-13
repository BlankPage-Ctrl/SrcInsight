package services

import (
	tspkg "srcinsight/services/resolve/ts"
)

// recvMode and friends wrap the ts ablation gate for receiver resolution.

// recvMode reports the raw SRCINSIGHT_RECV mode.
func recvMode() string {
	return tspkg.RecvMode()
}

// recvForwardEnabled checks whether the in-batch receiver pass runs.
func recvForwardEnabled() bool {
	return tspkg.RecvForwardEnabled()
}

// recvReverseEnabled checks whether reverse promotion runs.
func recvReverseEnabled() bool {
	return tspkg.RecvReverseEnabled()
}
