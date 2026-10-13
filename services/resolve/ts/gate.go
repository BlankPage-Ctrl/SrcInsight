package ts

import "os"

// RecvMode gates receiver-type resolution via SRCINSIGHT_RECV.
func RecvMode() string {
	return os.Getenv("SRCINSIGHT_RECV")
}

func RecvForwardEnabled() bool {
	return RecvMode() != "0"
}

func RecvReverseEnabled() bool {
	m := RecvMode()
	return m != "0" && m != "forward"
}
